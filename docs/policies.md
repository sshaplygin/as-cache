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
