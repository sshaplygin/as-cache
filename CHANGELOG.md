# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and from v0.1.0 the
project follows [semantic versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0]

First release worth using. The library existed before this as a proof that
shadow caching plus Thompson sampling can select an eviction policy at runtime;
this makes it correct under concurrency, cheap enough to deploy, stocked with
seven policies, and - for the first time - measured against published traces.

### Added

- **Policies.** 2Q, Random, TTL and W-TinyLFU alongside the existing LRU and
  LFU, with a shared conformance suite every policy must satisfy.
  - `policies` module: `NewLRU`, `NewLFU`, `NewTwoQueue`, `NewTTL`,
    `NewRandomPolicy`.
  - `policies/arc` module: ARC. Separate because the algorithm is patented by
    IBM (US 6,996,676), so importing `policies` never pulls a patented
    implementation into a build.
  - `policies/tinylfu` module: W-TinyLFU over `maypok86/otter`.
  - `policies.Adapt` adapts any cache that lacks eviction reporting or a
    `Resize` method, which is the shape most cache libraries ship.
- **Sampled shadow caching** (`Settings.ShadowSampleRate`). Shadows track a
  deterministic fraction of the keyspace and shrink to match, so per-operation
  cost stops scaling with the number of policies. Every arm - the active policy
  included - is measured over the same sampled substream, so no arm is judged
  on more evidence than another.
- **Switch stability** (`Settings.MinHitRateImprovement`,
  `SwitchCooldownEpochs`, `MinEpochRequests`), all inactive at their zero value.
- **Advisor mode** (`Settings.ObserveOnly` and `AdaptiveCache.Advice()`). The
  cache behaves exactly like the policy it was built with while measuring every
  alternative against real traffic, and reports which would serve it better and
  by how much. The bandit argument may be nil in this mode.
- **`metrics` module.** Publishes a `Snapshot` through `expvar`, evaluated on
  scrape. Standard library only.
- **`bench` module.** Deterministic workload generators (Zipf, uniform, loop,
  scan, phase-shift), a replay harness, loaders for published traces, and a
  Thompson-sampling bandit. `make evidence` reproduces every number in the
  README.
- **Migration strategies** `MigrationWarm` and `MigrationGradual` alongside the
  default `MigrationCold`.
- Constructor validation with sentinel errors: `ErrNilSettings`,
  `ErrInvalidEpochDuration`, `ErrNilBandit`, `ErrNilPolicy`,
  `ErrDuplicatePolicy`.
- `make release-check`, which catches the two things that block a release and
  show up in no other check: a missing licence, and sibling modules whose
  `replace` directives hide requires that no consumer can resolve.

### Fixed

- **A data race on every policy switch.** `runAdaptiveSelect` read and wrote
  `activePolicy` and ran migration without holding the lock.
- **Lost hit/miss counts.** `CacheWrapper`'s counters were mutated under a read
  lock by concurrent readers.
- **A stale bandit posterior.** The active policy's statistics were never
  reported, so the arm actually serving traffic was judged on nothing, and its
  active-tenure counts leaked into its first epoch as a shadow.
- **`Close` did not wait** for the background goroutine and was not idempotent.
- **`CacheWrapper.Cap` never changed after construction**, so a resized policy
  reported its original capacity forever.
- **LFU accepted entries at zero capacity.** `Resize(0)` followed by `Add`
  stored the entry, because the eviction step was skipped when there was
  nothing to evict. Every other policy holds nothing at zero capacity, and the
  adaptive layer resizes policies on its own, so the odd one out was a trap.
  Found by putting LFU through the shared conformance suite for the first time.
- Promoted `Get`s during a gradual migration were served to the caller but
  counted as misses, skewing both `Stats()` and the bandit toward the policy
  the cache had just left.
- A gradual migration window could stay open indefinitely, holding the outgoing
  policy at full capacity with its values retained and forcing every `Get`
  through the write lock.
- `Resize` during a gradual migration discarded data still awaiting promotion,
  and after a shrink left shadows simulating a larger cache than the real one.
- `Advice` pooled a policy's active tenure with its shadow tenure, so shortly
  after a correct switch it recommended reverting - for a number of epochs
  proportional to how long the cache had been running.

### Changed

- `Stats()` is now cumulative across epochs rather than resetting with the
  per-epoch counters.
- Demoted policies drop their values and shrink to miniature capacity, so a
  shadow costs keys and bookkeeping rather than full values. Six policies cost
  2.65x a single LRU rather than 6x, and 1.32x with sampling.
- The README's blanket "experimental" disclaimer is replaced by a scoped
  when-to-use section backed by measurements.

### Known limitations

- **It will not beat a policy you have already measured.** Adaptive selection
  lands within about a point of the best fixed policy on real traces. Its value
  is the floor it puts under a wrong choice, not the ceiling.
- **Reads still take a lock.** The lock-free read path is deferred: combining
  it with value-dropping needs a retry protocol across every read delegation
  and a breaking change to the `CacheStats` interface.
- **Epochs are wall-clock driven** with no way to step them, so anything
  measuring the bandit is sensitive to timing. The evidence suite is excluded
  from `-race` for this reason.
- **Synthetic workloads mislead.** LFU is the best policy on synthetic Zipf and
  the worst on both large real traces; only real traffic shows the crossover
  where different policies win on different workloads.
- **A sampled shadow's absolute hit rate is not a forecast.** Ranking survives
  sampling at every rate measured, but the absolute figure depends on which
  slice of the keyspace the seed selected.

[0.1.0]: https://github.com/sshaplygin/as-cache/releases/tag/v0.1.0
