## as-cache v0.3.0

A cache that measures eviction policies against your real traffic instead of
asking you to guess which one to use.

This release makes a replay reproducible, and then spends that reproducibility
on a question the previous two releases avoided: not which of this library's
policies is best, but whether reaching for this library beats reaching for
otter or theine.

**It does not.** That result, and the two measurement traps that nearly
published a flattering version of it, are the substance of this release.

### Install

```bash
go get github.com/sshaplygin/as-cache
go get github.com/sshaplygin/as-cache/policies
go get github.com/sshaplygin/as-cache/bandit
```

Requires Go 1.25 or later.

### Where this library actually places

Same workloads, same capacity of 500, replayed through the Go caches people
actually reach for. Reproduce with `make evidence`.

| Workload | otter v2 | theine | ristretto | sturdyc | as-cache |
| --- | --- | --- | --- | --- | --- |
| zipf | **73.19%** | 72.38% | 69.54% | 62.00% | 71.92% |
| uniform | 9.99% | **10.48%** | 9.97% | 9.52% | 10.23% |
| loop | 87.06% | 88.48% | **88.62%** | 45.42% | 68.78% |
| scan | **39.88%** | **39.88%** | 39.45% | 30.01% | 39.24% |
| phase-shift | 78.34% | **78.46%** | 72.53% | 53.19% | 75.06% |

Within a point of the best library on three of five, 3.4 points down on
phase-shift, nearly 20 down on `loop`, and 4 to 15 times slower per operation.
**If you are choosing a cache library and have no particular reason to expect
your traffic to change shape, otter or theine is the better answer.**

What this table does not contain is a workload where a fixed library is
catastrophic, because these five are kind to them: `loop` is the one designed
to defeat LRU, and W-TinyLFU-derived caches handle it. The case for this
library rests on [real traces](evidence.md), where the best fixed policy
changes from trace to trace - 2Q on Twitter and OLTP, W-TinyLFU on P3 and
LIRS, with W-TinyLFU landing second-*worst* on OLTP - and on advisor mode
telling you which one your traffic wants.

### Reproducible replays

**`Settings.EpochRequests`** ends an epoch every N `Get` calls instead of on a
wall clock:

```go
&ascache.Settings{
    EpochRequests: 10_000, // deterministic under replay
}
```

Wall-clock epochs make adaptation a function of machine speed: the same trace
re-evaluates a different number of times when the machine is loaded, and the
hit rate moves with it. This is not theoretical - two runs of the comparison
above disagreed by **12 points** on phase-shift before the switch, and agree to
within half a point after it. `Get` is the unit because hits and misses are
recorded there and nowhere else, so this counts exactly the requests the bandit
is shown. `EpochDuration` may now be zero when this is set; prefer it in
production, where an epoch on a caller's goroutine pays for the migration.

**The `benchclient` module** adapts the cache to the
`Init`/`Get`/`Set`/`Name`/`Close` contract used by Go cache benchmark suites,
`maypok86/benchmarks` in particular. It imports nothing from any suite - the
contract is five methods and Go interfaces are structural - and is configured
for reproducibility: request-counted epochs, a seeded bandit, no sampling.

### Fixed: the bandit ignored its own seed

`SelectPolicy` drew one sample per arm while ranging a map, so Go's randomised
iteration order handed each arm a different draw on every call. The seed fixed
the sequence of numbers, not who received them, and two replays of one trace
through an otherwise fully deterministic cache disagreed by hundreds of hits.
Draws now happen in a stable sorted arm order.

This is the same defect the cache fixed in its own epoch reporting in 0.2.0,
in the module written to make selection reproducible. Anything that consumes
randomness while iterating a map has this bug.

### Two traps that would have published a false result

Both are worth reading if you benchmark caches yourself.

**otter was silently being measured at four times the capacity of its rivals.**
It admits on the caller's goroutine and evicts on a maintenance pass, so a
replay writing flat out leaves it far over its limit: 5000 keys written into a
cache built with `MaximumSize: 500` left **1916** retrievable. The first
version of the table above had otter at 44.31% on uniform traffic where every
other cache served 10% - which reads as a decisive win and was entirely the
extra capacity. With `CleanUp` it is 9.99%. A test now fails if any competitor
drifts far over its stated size, with a threshold taken from five runs of
measured spread rather than a guess.

**The same effect flatters this library's own W-TinyLFU arm.** Measured at a
nominal 500 under read-through replay: 514 entries on `zipf`, 533 on `loop`,
611 on `uniform`. On `uniform` that is the whole story - the arm held 611 keys
of a 5000-key keyspace and served 12.18%, and 611/5000 is 12.2%, so its edge
over the other policies there is capacity rather than eviction. The evidence
tables are read-heavy enough that their rankings stand, but a write-heavy win
by this arm is partly capacity.

### Also in this release

- **`release-check` runs in CI and in `make all`.** It ran in neither, while
  the repository's own notes claimed it was part of `make all`. It is the only
  gate that catches a module nobody outside the repo can install, and what it
  guards against is invisible locally: the `replace` directive that hides an
  unresolvable require is exactly what makes local builds work.
- **The README is split into `docs/`** - design, configuration, policies,
  advisor mode, evidence, benchmarking, fleet. The pitch, the quick start and
  seven chapters of measurement had begun competing for the same first screen.
- **A correction.** An earlier note claiming `hashicorp/golang-lru` v2.0.7 does
  not build was wrong. That was a corrupted local module cache, not an upstream
  defect: the v2.0.6 and v2.0.7 zips contain the same files, and v2.0.7 builds
  against an empty `GOMODCACHE`. `policies` stays on v2.0.6 out of inertia, and
  a consumer whose build resolves v2.0.7 through MVS is fine.

### Known limitations

- **W-TinyLFU is not reproducible**, so `benchclient.DefaultArms` leaves it
  out. Measured directly, one trace replayed three times gave three different
  hit counts and left 527, 504 and 545 entries against a capacity of 500. otter
  evicts asynchronously and reports an approximate size.
  `ArmsWithWindowTinyLFU` includes it where the strongest arm matters more than
  repeatability. Do not "improve" the default by adding the best arm to it.
- **Reads still take a lock**, and the lock-free path is now measured rather
  than assumed: against a policy that takes its own exclusive lock on `Get` -
  which every LRU-family policy does, since a read moves recency - the cache
  layer costs about 25% of per-operation time, not the 72% a contention-free
  policy suggests. That is the ceiling for a change requiring a retry protocol
  across six read delegations, a breaking `CacheStats` change, and a
  `MigrationGradual` that cannot go lock-free at all.
- **It will not beat a policy you have already measured.** Unchanged since
  v0.1.0, and this release is the clearest evidence for it.

### Licence

[Mozilla Public License 2.0](../LICENSE). Every published module carries its
own copy, because a Go module zip contains only its own directory.
