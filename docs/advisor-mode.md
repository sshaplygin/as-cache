# Advisor mode and observability

## Advisor mode

The safest way to adopt this library is not to let it switch anything. In
`ObserveOnly` mode the cache behaves exactly like the first policy you give it
-- nothing ever migrates, nothing ever switches -- while every other policy is
measured in the background against your real traffic.

```go
cache, err := ascache.NewAdaptiveCache(
    []ascache.Policy[string, int]{lru, twoQ, tinyLFU},
    nil, // observing needs no bandit
    &ascache.Settings{
        EpochDuration:    time.Minute,
        ObserveOnly:      true,
        ShadowSampleRate: 0.05,
    },
)

// ... later, after real traffic ...
fmt.Println(cache.Advice())
```

```text
On this traffic TwoQueue beats LRU by 3.28 points of hit rate, over 240 epochs.
Rates are estimated from 5.0% of the keyspace.

policy      hit rate         hits       misses
 TwoQueue      59.62%       596200       403800
*LRU           56.34%       563400       436600
 TinyLFU       54.80%       548000       452000

* currently active
```

That answers a question that is otherwise expensive to ask, at no risk: you
learn whether a different eviction policy would serve your traffic better, and
by how much, without changing what your cache does. Acting on the answer is
then your choice -- switch to that policy directly, or turn `ObserveOnly` off
and let the bandit do it.

`Advice()` is safe to call at any time. Check `Epochs` before believing it: a
handful of epochs is not evidence.

Three things about how the report is computed, each of which once made it
misleading:

- **`Epochs` counts reporting epochs, not ticks.** Outside `ObserveOnly`, with
  `EvictPartialCapacityFilling` off, an epoch in which the active policy is not
  yet full measures nothing and is not counted -- a cache that has ticked a
  thousand times behind a half-full policy reports the evidence it actually has.
- **A policy's measurements start over when it changes role.** Its active
  tenure is measured at full capacity over all traffic, its shadow tenure on a
  miniature over the sample, and pooling the two describes neither. Carrying
  them across a switch also let the outgoing policy's long history outweigh the
  incoming one's short one, so straight after a correct switch the report named
  the policy just abandoned as best.
- **Ties are broken by policy type.** Arms performing identically produce the
  same `Best` on every call, rather than whichever one a map iteration happened
  to put first.

## Observability

A cache that changes its own eviction policy needs to be visible in staging.
The `metrics` module turns the cache's accounting into a scrapeable snapshot
and publishes it via `expvar` (standard library only):

```bash
go get github.com/sshaplygin/as-cache/metrics
```

```go
if err := metrics.Publish("cache", myCache); err != nil {
    log.Panic(err)
}
// snapshot now appears in /debug/vars under "cache"
```

`metrics.Take(cache)` returns the same data as a struct if you would rather
feed it somewhere else. The series worth graphing is `active_policy` over
time; the one worth alerting on is `improvement`, which measures how much hit
rate the cache is currently leaving on the table.

For Prometheus, wrap `metrics.Take` in a collector -- how metrics are named and
labelled belongs to your application, not to a cache library, so this package
does not impose a dependency on it.
