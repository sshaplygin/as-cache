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

    // MigrationMaxRequests caps a MigrationGradual window at N Get calls.
    // Zero sets no cap. See "Migration Strategies".
    MigrationMaxRequests int64

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
| `MigrationWarm` | Cached key/value pairs offered to the incoming policy | Transfers values, not eviction history; O(n) work at switch |
| `MigrationGradual` | Keys promoted on Get; one key drained per Add | Spreads migration cost; every `Get` takes the write lock while the window is open, which closes at the next epoch at the latest |

A gradual window serialises reads for as long as it is open. On a long epoch
that can be most of the epoch, so `MigrationMaxRequests` caps it at a number of
`Get` calls: when the cap is reached the window closes and the old policy is
demoted, and any key not promoted by then is gone — a later `Get` for it is a
miss, the same as it would have been under `MigrationCold`. Only `Get` counts,
because `Get` is what takes the write lock; `Add` drains a key per call and
shortens the window anyway. Zero, the default, sets no cap.

A short request cap can end migration before reusable entries transfer.
`TestSwitchWarmupCost` compares those effects in fixed request windows; see
[evidence](evidence.md#what-does-a-switch-cost-right-after-it) for the current
logs and the limits of that comparison.

## Reducing shadow overhead

Running policies in parallel costs something on every operation: each shadow is
another lookup and another lock. Since a shadow exists only to estimate a hit
rate, and a hit rate can be estimated from a sample, `ShadowSampleRate` lets
shadows track a hash-selected subset of the keyspace instead of mirroring
everything. Membership is stable within one cache instance; a new cache gets
a fresh random hash seed.

```go
&ascache.Settings{
    EpochDuration:    time.Minute,
    ShadowSampleRate: 0.05, // shadows track 5% of keys
}
```

Shadows shrink along with the rate, and every shadow samples the same keys.
This gives each arm a comparable input stream, but a miniature can change
reuse patterns and policy rankings. The active policy still serves every
key -- only the measurement is sampled, and it is sampled for the active policy
too, so no arm is judged on more evidence than another. `Stats()` continues to
report real, unsampled traffic.

Sampling reduces how often a request visits every shadow; it does not remove
the dependence on policy count. A sampled key still visits each shadow, so
fan-out work scales with both the sampled request fraction and the number of
arms. Sampling selects keys, and a hot selected key can account for many
requests: 5% of keys need not mean exactly 5% of requests.

The [warm-cache measurements](evidence.md#memory-and-per-operation-cost)
show the observed overhead with eight real policies. To isolate wrapper cost
with stub policies, run `go test -run '^$' -bench . -benchtime=300ms .`.

Sampling is off by default. `MinShadowCapacity` (256 unless you set it) is the
floor on a miniature: when the rate would shrink a shadow below it, the
*effective rate* is raised rather than the capacity alone, and on a cache small
enough that the floor exceeds its nominal size, sampling disables itself. A
miniature of a handful of entries measures noise rather than a policy.

Sampling may change policy rankings and absolute hit-rate estimates, so do not
quote a shadow rate as a forecast. See the
[evidence and its limits](evidence.md#does-sampling-distort-the-comparison).

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

`MinEpochRequests` counts the requests **the bandit sees**, which under
`ShadowSampleRate` are sampled requests: at a rate of 0.05 a threshold of 100
is reached after roughly 2000 real ones. Set it against the sampled stream, not
against your traffic.

## Tuning, measured

The [current P3 tuning experiment](../bench/results/current/README.md#p3-tuning)
compares all four combinations of cold/warm migration and stability gates
on/off, at 10/20/50 request-counted epochs. Three batches of five replays give
fifteen observations per cell; every result is retained. Gates use
`MinHitRateImprovement: 0.02` and `SwitchCooldownEpochs: 3`.

This is an example on one workload, not a production setting recommendation.
The experimental shadow floor is 64 rather than the default 256. The
[trace context table](../bench/results/current/README.md#workload-context)
reports the effective sample rate, which can exceed the requested 5%.

For an experiment:

- Use `EpochRequests` to hold epoch boundaries fixed across replay speeds;
  report every tested setting. The trace matrix includes 10/20/50 epochs,
  so the comparison does not select only the most favorable setting.
- Measure the tradeoff between migration work and adapting quickly enough.
  Warm migration retains values, but does not transfer a policy's learned
  history. Cold migration requires refilling; gradual migration spreads work
  and its request cap can truncate the transfer.
- Treat stability gates as parameters to test. They can avoid switches on
  steady traffic and delay useful switches when the workload changes.
- `ShadowSampleRate: 0.05` is one measured starting point. Validate ranking,
  overhead and sensitivity to the random sample on your workload; the
  synthetic sampling checks do not establish a universally optimal rate.
- Set `EvictPartialCapacityFilling: true` when W-TinyLFU or S3-FIFO could be
  the **active** arm. The gate reads that one policy and compares its `Len()`
  against `Cap()` for exact equality, and neither of those two holds that
  reliably: otter reports an approximate size, and S3-FIFO can drop about a
  tenth of the cache in a single write. When it fires, the whole epoch is
  skipped — no arm is reported and none is reset — so one under-filled active
  policy suspends every measurement, not just its own.
