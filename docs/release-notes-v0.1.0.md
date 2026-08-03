## as-cache v0.1.1

A cache that measures eviction policies against your real traffic instead of
asking you to guess which one to use.

Picking a replacement policy is normally a research task you do once, badly, and
never revisit. The cost of getting it wrong is not marginal: on a cyclic access
pattern just larger than the cache, LRU serves a **0%** hit rate where W-TinyLFU
serves **94%**. as-cache runs candidate policies side by side against your own
traffic, and either tells you which one wins or switches to it for you.

This is the first release worth using. The project previously demonstrated that
shadow caching plus Thompson sampling *can* select a policy at runtime; this
release makes it correct under concurrency, cheap enough to deploy, stocked with
seven policies — and, for the first time, measured against published traces.

### Install

```bash
go get github.com/sshaplygin/as-cache
go get github.com/sshaplygin/as-cache/policies      # ready-made policies
```

Requires Go 1.25 or later. The core module has **no dependencies**; policies and
integrations live in companion modules so you only pull in what you use.

### Start by observing

The lowest-risk way to adopt this is not to let it switch anything. In
`ObserveOnly` mode the cache behaves exactly like the first policy you give it,
while every other policy is measured in the background against your traffic:

```go
cache, _ := ascache.NewAdaptiveCache(
    []ascache.Policy[string, int]{lru, twoQ, tinyLFU},
    nil, // observing needs no bandit
    &ascache.Settings{
        EpochDuration:    time.Minute,
        ObserveOnly:      true,
        ShadowSampleRate: 0.05,
    },
)

// ... after real traffic ...
fmt.Println(cache.Advice())
```

```text
On this traffic TwoQueue beats LRU by 3.28 points of hit rate, over 240 epochs.
Rates are estimated from 5.0% of the keyspace.

policy      hit rate         hits       misses
 TwoQueue      59.62%       596200       403800
*LRU           56.34%       563400       436600
 Random        54.80%       548000       452000

* currently active
```

Nothing changes about how your cache behaves. You just learn whether a different
policy would serve your traffic better, and by how much.

### What the measurements say

Reproduce any of this with `make evidence`.

**No single policy wins everywhere.** Across five published traces the best
policy is 2Q on some and W-TinyLFU on others — and on the ARC OLTP trace,
W-TinyLFU, the strongest general-purpose baseline in wide use, lands
*second-worst*.

| Trace | Best fixed | Worst fixed | Adaptive |
| --- | --- | --- | --- |
| Twitter Twemcache cluster052 | 2Q 59.6% | LFU 41.4% | 59.4% |
| ARC OLTP (FAST '03) | 2Q 68.3% | LFU 45.4% | 67.1% |
| ARC P3 (FAST '03) | W-TinyLFU 11.7% | LRU 1.9% | **12.7%** |
| LIRS 2_pools | W-TinyLFU 54.8% | Random 50.1% | 54.4% |
| LIRS loop | W-TinyLFU 45.9% | LRU/LFU 0.0% | 42.5% |

Tuned sensibly, adaptive selection lands within about a point of the best fixed
policy and beats it on one trace — without being told in advance which that is.
Its real value is the floor, not the ceiling: it reliably avoids the policy that
would have been catastrophic for your workload.

**Synthetic benchmarks will mislead you.** Classic LFU is the *best* policy on
synthetic Zipf (73.5%) and the *worst* on both large real traces. Synthetic Zipf
holds popularity stationary, which is exactly LFU's assumption; real traffic
shifts, and stale frequency counts pin entries long after they stop being
useful. If you take one thing from this release, take that.

**Running seven policies does not cost seven caches.** Shadow policies hold keys
and eviction bookkeeping but never values:

| Configuration | Memory | Per-`Get` |
| --- | --- | --- |
| single LRU | 18.5 MiB | 32 ns |
| adaptive, 6 policies | 48.9 MiB (2.65x) | 618 ns |
| adaptive, 6 policies, sampled at 5% | 24.5 MiB (1.32x) | 82 ns |

Zero allocations per `Get` in all three.

### Highlights

- **Seven policies.** LRU, LFU, 2Q, Random, TTL, ARC and W-TinyLFU, each held to
  a shared conformance suite. ARC ships in its own module because the algorithm
  is patented by IBM, so importing `policies` never pulls a patented
  implementation into your build.
- **Sampled shadow caching.** `ShadowSampleRate` has shadows track a
  deterministic fraction of the keyspace and shrink to match, so per-operation
  cost stops scaling with the number of policies.
- **Advisor mode.** `ObserveOnly` plus `Advice()`, as above.
- **Observability.** The `metrics` module publishes a snapshot through `expvar`,
  evaluated on scrape. Standard library only.
- **Correctness.** The policy-switch data race, lost hit/miss counters, a stale
  bandit posterior, a `Close` that neither waited nor was idempotent, and a
  `Cap()` that never changed after construction are all fixed. See the
  [CHANGELOG](../CHANGELOG.md) for the full list.
- **Drop-in.** The API remains a superset of `hashicorp/golang-lru/v2`.

### Known limitations

These are real and stated plainly rather than discovered later.

- **It will not beat a policy you have already measured.** If you know
  W-TinyLFU suits your traffic, use `otter` directly. This library is for when
  you do not know.
- **Reads still take a lock.** The lock-free read path is deferred: combining it
  with value-dropping needs a retry protocol across every read delegation and a
  breaking change to `CacheStats`.
- **Epoch duration matters more than anything else.** Too short and the cache
  spends its life migrating rather than serving — a 2ms epoch on a 20k cache
  costs 8 points of hit rate and 30x the per-operation time. See the README.
- **A sampled shadow's absolute hit rate is not a forecast.** Sampling picks
  the same best policy at every rate measured (5%, 10%, 30%, 50%) with zero
  ranking inversions, but the absolute figure depends on which slice of the
  keyspace the seed selected and can land either side of the true rate. Use it
  to compare arms, not to predict what a policy would achieve.

### Modules

| Module | Contents | External dependencies |
| --- | --- | --- |
| `as-cache` | core cache, bandit interface | none |
| `as-cache/lfu` | O(1) LFU implementation | none |
| `as-cache/policies` | LRU, LFU, 2Q, Random, TTL adapters | `hashicorp/golang-lru/v2` |
| `as-cache/policies/arc` | ARC (patented — see above) | `hashicorp/golang-lru/arc/v2` |
| `as-cache/policies/tinylfu` | W-TinyLFU | `maypok86/otter/v2` |
| `as-cache/metrics` | expvar export | none |

### Licence

[Mozilla Public License 2.0](../LICENSE). File-level copyleft: use it in closed
source freely; modifications to these files, if distributed, are shared back.

### Acknowledgements

The evidence in this release replays traces published by others. Cited in full
in the README; briefly: Yang, Yue & Rashmi (OSDI '20) for the Twitter Twemcache
traces, Megiddo & Modha (FAST '03) for the ARC traces, and Jiang & Zhang
(SIGMETRICS '02) for the LIRS traces. Traces are downloaded by
`./scripts/fetch-traces.sh` and are never redistributed here.
