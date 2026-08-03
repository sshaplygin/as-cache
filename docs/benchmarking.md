# Benchmarking and reproducible replays

## Reproducible replays

`EpochDuration` measures on a wall clock, which is right in production and
wrong for a benchmark: replaying one trace twice re-evaluates a different
number of times on a machine that happens to be busy, so the hit rate moves
between runs and cannot be compared with anything. `EpochRequests` ends an
epoch every N `Get` calls instead, which takes the clock out of the
measurement entirely.

```go
&ascache.Settings{EpochRequests: 10_000} // no EpochDuration: no wall clock at all
```

`Get` is the unit because `Get` is where hits and misses are recorded, so this
counts exactly the requests the bandit is shown. A write-only workload never
ends an epoch, which is correct — there is nothing to compare policies on. The
epoch runs on whichever goroutine makes the Nth `Get`, so that call pays for
the switch and any migration; prefer `EpochDuration` in production, where that
work belongs on the background goroutine. Setting both applies both.

Two things outside the epoch clock also have to hold still, and one of them is
not in your control:

- **Seed the bandit.** `bandit.NewThompson(discount, seed)` takes one.
- **Every arm must be deterministic.** LRU, LFU, 2Q and Random are.
  **W-TinyLFU is not**: otter evicts asynchronously and reports an approximate
  size, so replaying one trace three times against it directly gave three
  different hit counts and left 527, 504 and 545 entries in a cache with a
  capacity of 500. One unstable arm moves which policy the bandit picks, and
  with it the whole replay.

## Benchmark harnesses

The `benchclient` module adapts the cache to the client contract used by Go
cache benchmark suites -- [maypok86/benchmarks](https://github.com/maypok86/benchmarks),
whose hit-ratio simulator and throughput harness both drive a cache through
`Init`/`Get`/`Set`/`Name`/`Close`:

```go
c := &benchclient.Cache[uint64, uint64]{}
c.Init(capacity)
defer c.Close()

if _, ok := c.Get(key); !ok {
    c.Set(key, value)
}
```

It imports nothing from any benchmark suite. The contract is five methods and
Go interfaces are structural, so the adapter satisfies it by shape alone --
which keeps a harness's dependency tree out of this one, and leaves the package
usable by anything wanting the same five methods.

It is configured for reproducibility rather than for the best number:
request-counted epochs, a seeded bandit, and no sampling. `DefaultArms` is LRU,
LFU, 2Q and Random, all of which are deterministic. `ArmsWithWindowTinyLFU`
adds the strongest arm and gives up repeatability to do it — that trade is
yours to make explicitly, which is why it is a second function rather than an
option. ARC is absent for the [patent reason](policies.md#arc-is-a-separate-module);
a harness that wants it can supply its own `Arms`.

## The repository's own suite

`make evidence` replays a suite of deterministic workloads against every policy,
against the adaptive cache, and against competing Go cache libraries. The
generators are in [bench/workload.go](../bench/workload.go) and the results are
written up in [evidence](evidence.md).

`./scripts/fetch-traces.sh` downloads five published traces (nothing is
committed), after which `AS_CACHE_TRACES=... make evidence` replays those too.

Evidence tests are guarded by `testing.Short()` and excluded from `make test`.
Under `-race` epoch pacing changes by roughly 15x and the measurements become
meaningless, so run them through `make evidence` rather than `go test -race`.
