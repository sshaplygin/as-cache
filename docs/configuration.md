# Configuration

Every setting, what it costs, and what the measurements say to set it to.

## Settings

```go
type Settings struct {
    // EpochDuration controls how often the bandit re-evaluates policies.
    EpochDuration time.Duration

    // EpochRequests ends an epoch every N Get calls instead of on a clock.
    // Set one of these two, or both. See docs/benchmarking.md.
    EpochRequests int64

    // EvictPartialCapacityFilling allows switching before the cache is full.
    // When false, the bandit only runs once the active policy reaches capacity.
    EvictPartialCapacityFilling bool

    // MigrationStrategy controls data transfer on policy switch.
    // Default: MigrationCold.
    MigrationStrategy MigrationStrategy

    // ObserveOnly measures every arm without ever switching.
    // See docs/advisor-mode.md.
    ObserveOnly bool

    // ShadowSampleRate has shadows track a fraction of the keyspace.
    // Zero means 1 (no sampling). See "Reducing shadow overhead".
    ShadowSampleRate  float64
    MinShadowCapacity int

    // Switch stability gates; all inactive at zero.
    // See "Keeping switches stable".
    MinHitRateImprovement float64
    SwitchCooldownEpochs  int64
    MinEpochRequests      int64
}
```

## Migration Strategies

| Strategy | Behaviour | Trade-off |
| --- | --- | --- |
| `MigrationCold` (default) | New active policy starts empty | Simple; causes a temporary miss spike |
| `MigrationWarm` | All key/value pairs copied at switch time | No miss spike; O(n) work at switch |
| `MigrationGradual` | Keys promoted on Get; one key drained per Add | Spreads migration cost; window closes at the next epoch at the latest |

## Reducing shadow overhead

Running policies in parallel costs something on every operation: each shadow is
another lookup and another lock. Since a shadow exists only to estimate a hit
rate, and a hit rate can be estimated from a sample, `ShadowSampleRate` lets
shadows track a deterministic fraction of the keyspace instead of mirroring
everything.

```go
&ascache.Settings{
    EpochDuration:    time.Minute,
    ShadowSampleRate: 0.05, // shadows track 5% of keys
}
```

Shadows shrink along with the rate, so each remains a faithful miniature of a
full-size cache rather than an undersized one, and every shadow samples the same
keys so their hit rates stay comparable. The active policy still serves every
key -- only the measurement is sampled, and it is sampled for the active policy
too, so no arm is judged on more evidence than another. `Stats()` continues to
report real, unsampled traffic.

The effect is that per-operation cost stops scaling with the number of policies.
Measured with mutex-backed stub policies on an Apple M1 Max at
`-benchtime=300ms`, so the numbers isolate what the adaptive layer adds rather
than what any particular policy costs:

| Benchmark | shadows | sampling off | rate 0.05 |
| --- | --- | --- | --- |
| `Get` | 1 | 97 ns/op | 35 ns/op |
| `Get` | 3 | 148 ns/op | 38 ns/op |
| `GetParallel` | 1 | 271 ns/op | 181 ns/op |
| `GetParallel` | 3 | 405 ns/op | 185 ns/op |
| `Add` | 1 | 110 ns/op | 52 ns/op |
| `Add` | 3 | 193 ns/op | 59 ns/op |
| `MixedParallel` | 1 | 183 ns/op | 87 ns/op |

Read the `Get` rows down the shadow count. Unsampled, a third shadow costs
another 50ns, because every operation visits every policy. Sampled, going from
one shadow to three costs 3ns -- the fan-out happens on 5% of operations, so
adding a policy is close to free. That is what makes carrying seven arms
practical.

Reproduce with `go test -run '^$' -bench . -benchtime=300ms .`

Sampling is off by default. Very small caches disable it automatically, since a
miniature of a handful of entries measures noise rather than a policy.

Sampling does not distort which policy wins — that was measured directly, see
[evidence](evidence.md#does-sampling-distort-the-comparison). It does distort
the absolute hit rate a shadow reports, so do not quote one as a forecast.

## Keeping switches stable

By default every bandit selection is applied. On noisy traffic two policies that
perform almost identically can trade places every epoch, and each switch costs a
migration. Three settings damp that, all inactive at their zero value:

```go
&ascache.Settings{
    MinHitRateImprovement: 0.02, // require a 2-point hit-rate win to switch
    SwitchCooldownEpochs:  3,    // and at most one switch every 3 epochs
    MinEpochRequests:      500,  // and ignore epochs with thin evidence
}
```

## Tuning, measured

The epoch duration is the setting that matters most, and the failure mode is
not subtle. Measured on the ARC P3 trace with a 20k-entry cache:

| Configuration | Hit rate | ns/op |
| --- | --- | --- |
| 50ms epoch, warm migration | 12.2% | 540 |
| 2ms epoch, warm migration | 4.8% | 13,476 |
| 2ms epoch, cold migration | 0.9% | 580 |

An epoch short enough to trigger frequent switches makes the cache copy its
entire contents on every switch, so it spends its time migrating rather than
serving. Cold migration is worse: it discards the cache at each switch, which
on the OLTP trace costs 28 points.

Rules of thumb:

- Make the epoch long enough that migrating the cache is a small fraction of
  the work done in it, and short enough that the workload sees many epochs.
- Prefer `MigrationWarm`. `MigrationCold` is only reasonable if switches are
  rare.
- The stability gates help on steady traffic and hurt on fast-changing traffic
  -- they cost 37 points on `loop`, which needs to re-adapt constantly.
- `ShadowSampleRate: 0.05` is a reasonable default. Higher rates cost more and
  buy no better ranking.
- Set `EvictPartialCapacityFilling: true` when W-TinyLFU is one of the arms:
  the capacity gate compares `Len()` against `Cap()` for exact equality, and
  otter reports an approximate size.
