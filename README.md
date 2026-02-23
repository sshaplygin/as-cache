# as-cache — Adaptive Selection Cache

An experimental Go library that uses a **Multi-Armed Bandit (MAB)** algorithm to automatically select the optimal cache replacement policy at runtime.

## Disclaimer

Experimental. Running multiple policies in parallel multiplies memory consumption proportionally to the number of candidate policies.

## Problem

Choosing the right cache replacement algorithm for a workload is a separate research task. This library sidesteps that decision by running candidate policies in parallel (shadow caching), measuring hit/miss rates per epoch, and using Thompson Sampling to pick the winner dynamically.

## Idea

Every epoch the background goroutine:

1. Collects hit/miss statistics from each shadow policy.
2. Feeds them as Beta-distribution parameters into the MAB bandit.
3. Samples from the distributions and switches the active policy to the winner.
4. Shadow caches continue tracking access patterns with zero-value dummy entries so no real data leaks.

Policy migration at switch time is configurable — see [Migration Strategies](#migration-strategies).

## Usage

See [examples/basic/main.go](examples/basic/main.go) for a complete runnable example with an HTTP server and a Thompson Sampling adapter (via `stitchfix/mab`).

## Supported Cache Methods

| Policy | Status | Notes |
| --- | --- | --- |
| LRU | implemented | via `hashicorp/golang-lru/v2` |
| LFU | implemented | native O(1) implementation in `lfu/` |
| 2Q | planned | — |
| ARC | planned | — |
| Random | planned | — |

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
| `ActivePolicy() PolicyType` | Which policy is currently serving requests |
| `Close() error` | Stop the background epoch goroutine |

## Settings

```go
type Settings struct {
    // EpochDuration controls how often the bandit re-evaluates policies.
    EpochDuration time.Duration

    // EvictPartialCapacityFilling allows switching before the cache is full.
    // When false, the bandit only runs once the active policy reaches capacity.
    EvictPartialCapacityFilling bool

    // MigrationStrategy controls data transfer on policy switch.
    // Default: MigrationCold.
    MigrationStrategy MigrationStrategy
}
```

## Migration Strategies

| Strategy | Behaviour | Trade-off |
| --- | --- | --- |
| `MigrationCold` (default) | New active policy starts empty | Simple; causes a temporary miss spike |
| `MigrationWarm` | All key/value pairs copied at switch time | No miss spike; O(n) work at switch |
| `MigrationGradual` | Keys lazily promoted on Get-miss; one key drained per Add | Spreads migration cost; window closes at next epoch |

## Architecture

```text
AdaptiveCache
  |-- active policy  (CacheWrapper -> real Cacher impl)
  |-- shadow policy  (CacheWrapper -> real Cacher impl, zero-value adds only)
  |-- Bandit         (Thompson Sampling via stitchfix/mab)
  |-- background goroutine (epoch ticker -> tryChangePolicy -> migrateData)
```

## Implementing the Bandit Interface

```go
type Bandit interface {
    // RecordStats delivers shadow-cache hit/miss stats for one epoch.
    RecordStats(stats ShadowStats)

    // SelectPolicy returns the policy that should become active next epoch.
    SelectPolicy() PolicyType
}
```

A full Thompson Sampling adapter using `stitchfix/mab` is provided in [examples/basic/main.go](examples/basic/main.go).

## TODO

- [ ] 2Q policy wrapper (`hashicorp/golang-lru/v2` 2Q variant)
- [ ] ARC policy wrapper (`hashicorp/golang-lru/v2` ARC variant)
- [ ] Random eviction policy
- [ ] TTL-based policy (`hashicorp/golang-lru/v2/expirable`)
- [ ] README: detailed benchmarks comparing policies per workload type

## References

- [Cache replacement policies — Wikipedia](https://en.wikipedia.org/wiki/Cache_replacement_policies)
- [Introducing Ristretto — hypermode.com](https://hypermode.com/blog/introducing-ristretto-high-perf-go-cache)
- [Ristretto (dgraph-io)](https://github.com/dgraph-io/ristretto) — inspiration for adaptive selection
- [hashicorp/golang-lru](https://github.com/hashicorp/golang-lru) — LRU/2Q/ARC implementations
- [stitchfix/mab](https://github.com/stitchfix/mab) — Multi-Armed Bandit (Thompson Sampling)
