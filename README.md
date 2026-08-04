# as-cache — Adaptive Selection Cache

[![CI](https://github.com/sshaplygin/as-cache/actions/workflows/ci.yml/badge.svg)](https://github.com/sshaplygin/as-cache/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/sshaplygin/as-cache.svg)](https://pkg.go.dev/github.com/sshaplygin/as-cache)
[![Go Report Card](https://goreportcard.com/badge/github.com/sshaplygin/as-cache)](https://goreportcard.com/report/github.com/sshaplygin/as-cache)
[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](LICENSE)

A Go library that uses a **Multi-Armed Bandit (MAB)** algorithm to select the
cache replacement policy at runtime, measuring candidate policies against your
real traffic instead of asking you to guess.

One policy is **active** and serves every request. The others run as
**shadows**: they see each key, never its value, and answer "would I have had
this?" Once per epoch every arm reports its hit rate, a bandit names the winner,
and the cache switches if the win is worth the migration. Full mechanism in
[docs/design.md](docs/design.md).

## Documentation

| Document | Contents |
| --- | --- |
| [Project site](https://sshaplygin.github.io/as-cache/) | Landing page, plus an interactive explorer of the bandit's decisions on a phase-shift run |
| [Design](docs/design.md) | How it works per request and per epoch, the `Bandit` interface, what is not done |
| [Configuration](docs/configuration.md) | Every `Settings` field, migration strategies, sampling, stability gates, tuning |
| [Policies](docs/policies.md) | The ready-made arms, ARC's patent split, W-TinyLFU's caveats, adapting your own cache |
| [Advisor mode](docs/advisor-mode.md) | `ObserveOnly`, `Advice()`, and the `metrics` module |
| [Evidence](docs/evidence.md) | Every measured claim: policy tables, competing libraries, real traces, sampling fidelity, fleets |
| [Benchmarking](docs/benchmarking.md) | Reproducible replays, `benchclient`, `make evidence` |
| [Running a fleet](docs/fleet.md) | Pooling evidence across replicas through Valkey or Redis |

## Status

Pre-1.0: the API may still change, and nothing here has been run in production
that I know of. What has been done is measurement -- every claim comes from a
reproducible run against published traces, not from intuition, and the
concurrency has been exercised under the race detector and adversarially
reviewed. Read [the evidence](docs/evidence.md) and decide for yourself; the
numbers are there so you do not have to take "experimental" or
"production-ready" on trust.

Three things are worth knowing before adopting it.

**No single policy wins everywhere, and that is the point.** On real traces the
best fixed policy changes: 2Q wins on the Twitter and OLTP traces, W-TinyLFU on
the ARC P3 and LIRS traces -- and on OLTP, W-TinyLFU is second-*worst*. Tuned
sensibly, adaptive selection lands within about a point of the best fixed
policy on most traces and beats it on one, without being told which to pick.

**It is sensitive to configuration.** The same traces with a too-short epoch
lose up to 7 points and cost 30x the per-operation time, because the cache
spends its life migrating rather than serving. Read
[tuning](docs/configuration.md#tuning-measured) before drawing conclusions from
your own numbers.

**Memory costs less than the obvious guess.** Running N policies in parallel
does not multiply memory by N, because shadow policies hold keys and eviction
bookkeeping but never real values -- 2.65x for six policies, 1.32x with
sampling on. Per-operation cost is 32 ns/op for a single LRU against 82 sampled
and 618 unsampled; the [full tables](docs/evidence.md#memory-and-per-operation-cost)
have the details.

### When to use it

- You do not know which policy suits your traffic, and cannot easily find out.
- Your traffic changes shape and you would rather not re-tune.
- You want the measurement more than the switching. `ObserveOnly` mode gives
  you that at zero risk -- see [advisor mode](docs/advisor-mode.md).

### When not to use it

- You have already measured your traffic and know which policy wins. Use that
  policy directly; this library's best case is roughly to match it.
- The hot path is latency-critical at single-digit nanoseconds. Even sampled,
  the adaptive layer costs several times a bare LRU per operation.
- You need a hard memory ceiling. The multiplier is modest but real.
- You cannot give it enough traffic per epoch to measure anything. Arms that
  are within noise of each other reorder run to run, so a cache seeing a
  handful of requests per epoch will pick essentially at random. `Advice()`
  reports `Epochs` so you can tell whether it has seen enough. If the reason
  is that your traffic is spread across many replicas rather than genuinely
  thin, see [running a fleet](docs/fleet.md).
- Your keyspace is small enough to fit in the cache. Every policy scores the
  same when nothing is ever evicted, and you are paying for shadows that can
  never tell you anything.

## Usage

```bash
go get github.com/sshaplygin/as-cache
go get github.com/sshaplygin/as-cache/policies
go get github.com/sshaplygin/as-cache/bandit
```

```go
lru, _ := policies.NewLRU[string, int](10000)
twoQ, _ := policies.NewTwoQueue[string, int](10000)

cache, err := ascache.NewAdaptiveCache(
    []ascache.Policy[string, int]{lru, twoQ},
    bandit.NewThompson(0.9, 1), // discount, seed
    &ascache.Settings{EpochDuration: time.Minute, ShadowSampleRate: 0.05},
)
if err != nil {
    return err
}
defer cache.Close()

cache.Add("k", 1)
v, ok := cache.Get("k")
```

See [examples/basic/main.go](examples/basic/main.go) for a complete runnable
example with an HTTP server and a Thompson Sampling adapter (via
`stitchfix/mab`).

## Policies

| Policy | Constructor | Notes |
| --- | --- | --- |
| LRU | `policies.NewLRU` | via `hashicorp/golang-lru/v2` |
| LFU | `policies.NewLFU` | native O(1) implementation in `lfu/` |
| 2Q | `policies.NewTwoQueue` | scan-resistant |
| Random | `policies.NewRandomPolicy` | the control arm worth beating |
| TTL | `policies.NewTTL` | expiry as well as recency |
| ARC | `policies/arc.NewPolicy` | separate module — patented by IBM |
| W-TinyLFU | `policies/tinylfu.NewPolicy` | separate module; the strongest baseline |

Details and caveats in [docs/policies.md](docs/policies.md).

## AdaptiveCache API

All methods are safe for concurrent use.

| Method | Description |
| --- | --- |
| `Add(key, value) bool` | Add or update a key; returns true if an eviction occurred |
| `Get(key) (V, bool)` | Retrieve a value; records a hit or miss |
| `Contains(key) bool` | Check presence without recording a hit |
| `Peek(key) (V, bool)` | Read value without recording a hit |
| `Remove(key) bool` | Delete a key from all policies |
| `Purge()` | Clear all policies and reset migration state |
| `Keys() []K` | Keys in the active policy |
| `Values() []V` | Values in the active policy |
| `Len() int` | Number of entries in the active policy |
| `Resize(size) int` | Resize all policies; returns total eviction count |
| `Stats() GlobalStats` | Cumulative hit/miss counts for the active policy |
| `Advice() Advice` | Which policy is winning, and by how much |
| `ActivePolicy() PolicyType` | Which policy is currently serving requests |
| `Close() error` | Stop the background epoch goroutine |

## References

- [Cache replacement policies — Wikipedia](https://en.wikipedia.org/wiki/Cache_replacement_policies)
- [Introducing Ristretto — hypermode.com](https://hypermode.com/blog/introducing-ristretto-high-perf-go-cache)
- [Ristretto (dgraph-io)](https://github.com/dgraph-io/ristretto) — inspiration for adaptive selection
- [hashicorp/golang-lru](https://github.com/hashicorp/golang-lru) — LRU/2Q/ARC implementations
- [stitchfix/mab](https://github.com/stitchfix/mab) — Multi-Armed Bandit (Thompson Sampling)
- [redis/go-redis](https://github.com/redis/go-redis) — the client behind the Valkey/Redis store
- [Valkey](https://valkey.io/) — the store the distributed bandit was built against

## License

[Mozilla Public License 2.0](LICENSE).

MPL 2.0 is file-level copyleft. You may use this library in a closed-source
application without opening your own code; if you modify one of *these* files
and distribute the result, that file's source must be made available under the
same licence. Each publishable module carries its own copy of the licence,
because a Go module zip contains only its own directory.
