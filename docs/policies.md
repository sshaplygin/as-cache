# Ready-made policies

The core module has no dependencies. Ready-made arms live in a companion
module, so you pull in a cache library only if you use one:

```bash
go get github.com/sshaplygin/as-cache/policies
```

```go
lru, _ := policies.NewLRU[string, int](10000)
twoQ, _ := policies.NewTwoQueue[string, int](10000)

cache, err := ascache.NewAdaptiveCache(
    []ascache.Policy[string, int]{
        lru,
        twoQ,
        policies.NewRandomPolicy[string, int](10000),
        policies.NewTTL[string, int](10000, 5*time.Minute),
    },
    myBandit,
    &ascache.Settings{EpochDuration: time.Minute, ShadowSampleRate: 0.05},
)
```

| Policy | Constructor | Notes |
| --- | --- | --- |
| LRU | `policies.NewLRU` | `hashicorp/golang-lru/v2` |
| LFU | `policies.NewLFU` | this repository's O(1) LFU; strong on stationary popularity, weak when it shifts |
| 2Q | `policies.NewTwoQueue` | scan-resistant; a scan cannot flush the working set |
| Random | `policies.NewRandomPolicy` | no bookkeeping; the control arm worth beating |
| TTL | `policies.NewTTL` | expiry as well as recency |
| ARC | `policies/arc.NewPolicy` | separate module — see below |
| W-TinyLFU | `policies/tinylfu.NewPolicy` | separate module; the strongest baseline |
| S3-FIFO | `policies/fifo.NewS3FIFOPolicy` | separate module; three FIFO queues, and deterministic |
| SIEVE | `policies/fifo.NewSievePolicy` | same module; one FIFO queue and a sweeping hand |

`Random` is worth keeping in the mix precisely because it assumes nothing: a
policy that cannot beat random on your traffic is not earning its bookkeeping.

How each of these actually performs is measured in [evidence](evidence.md); the
short version is that the winner changes by trace.

## ARC is a separate module

```bash
go get github.com/sshaplygin/as-cache/policies/arc
```

ARC is patented by IBM (US 6,996,676), which is why upstream `hashicorp/golang-lru`
moved it to its own module in v2. This repository keeps that split, so importing
`policies` never pulls a patented implementation into your build and the choice
to use ARC is always explicit. Whether the patent still restricts anything is a
question for you and your counsel.

## W-TinyLFU

```bash
go get github.com/sshaplygin/as-cache/policies/tinylfu
```

Carried in its own module so otter and its dependencies stay out of builds that
do not use it. This is the arm worth including if the question is whether an
adaptive cache beats the state of the art rather than whether it beats LRU.

Note that otter reports an approximate size, so this policy's `Len()` is
approximate. Set `EvictPartialCapacityFilling: true` when using it, since the
capacity gate compares `Len()` against `Cap()` for exact equality.

**It also runs slightly over the capacity it was given, and that flatters it.**
otter admits on the calling goroutine and evicts on a maintenance pass, so the
cache sits above its limit whenever writes arrive faster than maintenance
drains them. Measured at a nominal 500 entries under read-through replay: 514
on `zipf` (1.03x), 533 on `loop` (1.06x), 611 on `uniform` (1.22x) — the
overshoot tracks the write rate, and `uniform` misses on almost every request.
On that workload it is the whole story: the arm held 611 of a 5000-key
keyspace and served 12.18%, and 611/5000 is 12.2%, so its edge over the other
policies there is capacity rather than eviction. Under a pure write flood the
gap is far wider — 1916 entries retained against a limit of 500 — which is why
the competitor harness calls `CleanUp`. Read its wins on write-heavy workloads
with that in mind; on read-heavy ones the overshoot is a few percent and the
comparison is sound.

It is also the one arm that is not deterministic, which matters for
[reproducible replays](benchmarking.md).

## S3-FIFO and SIEVE

```bash
go get github.com/sshaplygin/as-cache/policies/fifo
```

```go
s3, err := fifo.NewS3FIFOPolicy[string, int](10000)
sv, err := fifo.NewSievePolicy[string, int](10000)
```

Both are carried in one module, backed by
[scalalang2/golang-fifo](https://github.com/scalalang2/golang-fifo) (MIT), so
that dependency stays out of builds that do not use these arms. They share a
module because they share a dependency and an adapter, not because they are the
same algorithm.

Neither reorders anything on a cache hit — that omission is what makes both
cheap and scalable — and both are **deterministic**, which is what a
[reproducible replay](benchmarking.md) needs and what W-TinyLFU cannot offer.

### S3-FIFO

S3-FIFO is three static FIFO queues and nothing else — no recency list, and no
reordering on a hit. A new key enters a **small** queue holding a tenth of the
cache. Reaching the tail of that queue is the admission test: a key requested
at least twice since it was admitted moves to the **main** queue, and one that
was not is evicted, with its key kept in a **ghost** queue that holds no
values. A key that comes back while its ghost entry is live skips probation and
is admitted straight to the main queue, which evicts by FIFO-reinsertion over a
counter capped at three.

The observation it is built on is that in real workloads most keys are
requested exactly once, so a policy that keeps them until they age out spends
most of its capacity on keys nobody will read again. Its authors report lower
miss ratios than the LRU-based state of the art across several thousand traces
(Yang, Zhang, Qiu, Yue & Rashmi, *FIFO Queues are All You Need for Cache
Eviction*, SOSP '23).

It is in `benchclient.DefaultArms` for that reason.

### SIEVE

SIEVE is simpler still: **one** FIFO queue and a hand that sweeps it from the
oldest end. Each entry carries a single visited bit, set when it is read. The
hand walks backwards looking for an entry whose bit is clear, clearing the bit
of every entry it steps over, and evicts the first one it finds. No ghost
queue, no counters, no second queue.

The effect is much the same filtering S3-FIFO's small queue performs — a key
requested once is evicted on the hand's first pass, a key requested again
survives one more — reached with far less bookkeeping. Its authors report hit
rates competitive with the state of the art and higher throughput than LRU
(Zhang, Yang, Yue, Vigfusson & Rashmi, *SIEVE is Simpler than LRU*, NSDI '24).

Carrying it *alongside* S3-FIFO rather than instead of it is the point: both
filter one-hit keys, but on different evidence, and SIEVE keeps no ghost queue
at all — so it costs less and sees less. Which of the two wins is a property of
your traffic, which is the argument this whole library rests on.

### What the adapter has to supply, and what that costs

This applies to both arms; they share the adapter.

`golang-fifo` exposes `Set`/`Get`/`Remove`/`Contains`/`Peek`/`Len`/`Purge`/
`Close`. `Cacher` also needs `Keys`, `Values`, `Resize` and `Cap`, and needs
`Add` to report whether it evicted. None of those exist upstream. Three
consequences are worth knowing before you read either arm's numbers.

**`Resize` rebuilds the cache, discarding everything the algorithm has
learned.** The rebuilt cache starts with an empty ghost queue and every
frequency counter at zero: an entry that had earned the main queue must earn it
again, and a key evicted just before the resize loses its second chance. That
is the same cost the [2Q and ARC adapters](#adapting-your-own-cache) pay — but
`AdaptiveCache` resizes a policy every time it is promoted or demoted, so an
arm that changes role often is an arm that is permanently re-learning, and it
under-reports its own hit rate while it does.

**The adapter keeps a second copy of the key set.** There is no way to
enumerate the library's contents, so `Keys` and `Values` are served from an
index the adapter maintains through the library's eviction callback. Keys are
stored twice; values are not. The order that index yields is arbitrary and is
not an eviction order.

**Upstream counts a write as an access.** `Set` on a key already present raises
S3-FIFO's frequency counter, and sets SIEVE's visited bit. Neither paper counts
a write that way, and shadow policies here are driven with `Add`, so an arm on
shadow duty looks more used than it should.

Two smaller notes. The adapter always builds with a TTL of zero, which is
load-bearing: a non-zero TTL starts a background goroutine that would invoke
the eviction callback from a goroutine the adapter never entered, and the
callback deliberately runs without taking the adapter's lock. And the adapter
answers `Len` from its own index rather than calling upstream's, because
upstream's S3-FIFO reads its queues in `Len` without taking its mutex. (Its
SIEVE does lock; answering both the same way keeps the hazard out of reach
rather than depending on which algorithm is wrapped.)

### S3-FIFO's ghost queue is a hard horizon

A key is promoted out of the small queue only if it is requested again **while
it is still there or still in the ghost queue**. On a workload whose reuse
distances are longer than that window, S3-FIFO sends keys round the small queue
forever and never promotes anything — it serves 0.00% on the LIRS `loop` trace,
tied with LRU, LFU, 2Q, TTL and ARC. That is the algorithm working as designed,
and it is why this is not uniformly better than LRU. See
[evidence](evidence.md).

## Adapting your own cache

Any type satisfying `Cacher[K, V]` can be an arm. If your cache does not report
evictions or cannot be resized — as `2Q` and `ARC` do not — wrap it:

```go
cache, err := policies.Adapt[string, int](size, func(size int) (policies.PartialCacher[string, int], error) {
    return mylib.New[string, int](size)
})
```

Note that `Resize` on an adapted cache rebuilds it, discarding whatever
adaptation the algorithm had learned. `AdaptiveCache` resizes shadow policies
when its own capacity changes, so adapted policies are heavier arms to carry
than natively resizable ones.
