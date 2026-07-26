# Data Models Codemap

**Last Updated:** 2026-07-26

Types, their zero values, and the invariants that hold them together. File
structure is in `backend.md`.

## Enums

### PolicyType (`models.go`)

```go
type PolicyType uint

const (
    Undefined PolicyType = iota  // 0 -- zero value, names no policy
    LRU                          // 1
    LFU                          // 2
    TwoQueue                     // 3 -- scan-resistant
    ARC                          // 4 -- patented; adapter is a separate module
    Random                       // 5 -- the control arm worth beating
    TTL                          // 6 -- expiry as well as recency
    TinyLFU                      // 7 -- W-TinyLFU
)
```

Closed enum. Adding a policy means extending it **and** running
`go generate ./...` to regenerate `policytype_string.go`.

`Undefined` is meaningful: a bandit returning it selects nothing, which is how
`observerBandit` works in `ObserveOnly` mode.

### MigrationStrategy (`models.go`)

```go
type MigrationStrategy uint

const (
    MigrationCold    MigrationStrategy = iota  // 0 -- start empty
    MigrationWarm                              // 1 -- copy at switch time
    MigrationGradual                           // 2 -- promote on read, drain on Add
)
```

Every strategy purges the incoming policy first: it arrives from shadow duty
holding zero values, and serving one as real data would break the core
invariant.

Measured cost: `MigrationCold` loses 28 points of hit rate on the ARC OLTP
trace. `MigrationWarm` is the sane default.

## Configuration

### Settings (`settings.go`)

Every field is inactive at its zero value, so `Settings{EpochDuration: d}` is a
complete configuration.

| Field | Type | Zero means |
| --- | --- | --- |
| `EpochDuration` | `time.Duration` | **required**, must be > 0 |
| `EvictPartialCapacityFilling` | `bool` | only switch once the cache is full |
| `MigrationStrategy` | `MigrationStrategy` | `MigrationCold` |
| `MinHitRateImprovement` | `float64` | no improvement gate |
| `SwitchCooldownEpochs` | `int64` | may switch every epoch |
| `MinEpochRequests` | `int64` | no minimum evidence |
| `ShadowSampleRate` | `float64` | 1 -- shadows mirror every key |
| `MinShadowCapacity` | `int` | `DefaultMinShadowCapacity` (256) |
| `ObserveOnly` | `bool` | the cache may switch |

`MinHitRateImprovement` is a **fraction** in [0,1], matching `Advice.Improvement`
(0.02 = two points), not a percentage.

Under `ShadowSampleRate`, `MinEpochRequests` counts *sampled* requests: at rate
0.05 a threshold of 100 is reached after roughly 2000 real requests.

## Constructor errors (`errors.go`)

`NewAdaptiveCache` returns these as sentinels; compare with `errors.Is`.

| Error | Cause |
| --- | --- |
| `ErrEmptyPolicies` | no policies supplied |
| `ErrNilSettings` | `settings` is nil |
| `ErrInvalidEpochDuration` | `EpochDuration` <= 0 (`time.NewTicker` would panic) |
| `ErrNilBandit` | `bandit` is nil and `ObserveOnly` is false |
| `ErrNilPolicy` | a nil entry in the policies slice |
| `ErrDuplicatePolicy` | two policies report the same `PolicyType` |

Validation order matters: settings is checked before the bandit, because a nil
bandit is legal when `settings.ObserveOnly` is set.

## Statistics

```go
type PolicyStats struct{ Hits, Misses int64 }   // one policy
type GlobalStats struct{ Hits, Misses int64 }   // what the cache served
type ShadowStats struct {                       // one epoch report to the bandit
    Policy PolicyType
    Hits, Misses int64
}
```

Three different scopes, easily confused:

| Source | Covers | Sampled? |
| --- | --- | --- |
| `AdaptiveCache.Stats()` | all traffic the cache served, cumulative | never |
| `ShadowStats` (to bandit) | one policy, since its last report | yes, if enabled |
| `PolicyReport` (in `Advice`) | one policy, current role only | yes, if enabled |

`Stats()` is cumulative across epochs because per-policy counters are reset
after each report; the active policy's totals fold into `globalStats` first.

## Advice

```go
type Advice struct {
    Epochs      int64          // REPORTING epochs, not elapsed ticks
    Active      PolicyType
    Best        PolicyType
    Improvement float64        // fraction; 0 when Best == Active
    Sampled     bool
    SampleRate  float64
    Reports     []PolicyReport // best hit rate first, ties broken by PolicyType
}

type PolicyReport struct {
    Policy       PolicyType
    Hits, Misses int64         // current role only
    Active       bool
}
```

Invariants worth knowing before changing this:

- `Reports` is sorted by hit rate with a **deterministic tie-break** on
  `PolicyType`. Ranging a map and sorting stably on rate alone made `Best` flap
  between equally-performing arms on an unchanged cache.
- `tenureStats` is cleared for both policies on a switch. Pooling a policy's
  active tenure with its shadow tenure mixes full capacity over all traffic with
  a miniature over a sample, and lets a long history outweigh a short one -- so
  advice recommended reverting a correct switch.
- `Epochs` counts epochs that measured something. The capacity gate can skip
  measurement indefinitely.

## Metrics snapshot (`metrics` module)

`Snapshot` is JSON-serialised for expvar. `Hits`/`Misses`/`HitRate` are real
unsampled traffic; the per-policy figures inside `Policies` are sampled when
sampling is on. The series worth graphing is `active_policy`; the one worth
alerting on is `improvement`.

## Core invariant

> A caller must never observe a zero value, or any value never stored, as if it
> were real cached data.

Everything below serves it:

- Shadows receive `Add(key, zeroValue)` and hold no real values.
- Every migration purges the incoming policy before it serves anyone.
- Demotion drops values only *after* the policy stops being active.
- Gradual migration promotes a key *before* the counted lookup, so a served
  request is a hit rather than a miss against a zero.

## Capacity model

Each policy has two capacities:

- `nominalCap` -- as the caller built it; restored on promotion.
- `shadowCap` -- `ceil(effectiveRate * nominalCap)`; used while shadowing.

`shadowCapacity` applies the `MinShadowCapacity` floor by raising the effective
*rate*, preserving `shadowCap/nominalCap == rate`. `scaledCapacity` (used by
`Resize`) does **not** apply the floor: the sampler's rate is fixed for the
cache's life, so flooring capacity alone would leave shadows simulating a larger
cache than the real one and bias every arm against the active policy.
