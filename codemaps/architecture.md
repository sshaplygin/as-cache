# Architecture Codemap

**Last Updated:** 2026-02-23
**Module:** `github.com/sshaplygin/as-cache`
**Go Version:** 1.25.2
**Status:** Experimental

## Overview

as-cache (Adaptive Selection Cache) is a Go library that uses a Multi-Armed Bandit
(MAB) algorithm to automatically select the best cache eviction policy at runtime.
Instead of choosing a fixed policy upfront, the library runs multiple policies in
parallel via shadow caching, measures hit/miss rates per epoch, and uses Thompson
Sampling to switch to the best-performing policy dynamically.

## Component Diagram

```
                    User Code
                       |
                       v
             +-------------------+
             |  AdaptiveCache    |  cache.go
             |  [K, V]          |
             |  - activePolicy  |
             |  - policies map  |
             |  - bandit        |
             |  - epochTicker   |
             +--------+---------+
                      |
          +-----------+-----------+
          |                       |
          v                       v
  +---------------+      +---------------+
  | CacheWrapper  |      | CacheWrapper  |   wrapper.go
  | (LRU active)  |      | (LFU shadow)  |
  | - stats       |      | - stats       |
  +-------+-------+      +-------+-------+
          |                       |
          v                       v
  +---------------+      +---------------+
  | hashicorp     |      | lfu.Cache     |   lfu/lfu.go
  | golang-lru/v2 |      | (thread-safe) |
  +---------------+      +-------+-------+
                                  |
                          +-------+-------+
                          | simplelfu.LFU |   lfu/simplelfu/lfu.go
                          | (O(1) core)   |
                          +-------+-------+
                                  |
                          +-------+-------+
                          | internal.     |   lfu/internal/list.go
                          | LfuList       |
                          | (doubly-      |
                          |  linked list) |
                          +---------------+

  +-------------------+
  | Bandit interface  |  interfaces.go
  | (Thompson         |
  |  Sampling via     |  Implemented in examples/ using
  |  stitchfix/mab)   |  stitchfix/mab library
  +-------------------+
```

## Epoch-Based Adaptive Loop

```
1. Epoch timer fires (configurable duration)
2. AdaptiveCache.tryChangePolicy() acquires write lock
3. Clears any incomplete gradual migration from previous epoch
4. Collects hit/miss stats from all shadow policies
5. Calls bandit.RecordStats() for each shadow policy
6. Calls bandit.SelectPolicy() to pick the winner
7. If policy changed: migrateData(old, new) per MigrationStrategy
8. Resets stats, increments epochID
```

## File Map

| File | Package | Lines | Purpose |
|------|---------|-------|---------|
| `interfaces.go` | ascache | 47 | Core interfaces: Cacher, Policy, CacheStats, Bandit |
| `models.go` | ascache | 51 | PolicyType, MigrationStrategy enums; stats structs |
| `cache.go` | ascache | 413 | AdaptiveCache: orchestrator, migration, all cache ops |
| `wrapper.go` | ascache | 56 | CacheWrapper: wraps Cacher, adds hit/miss tracking |
| `generate.go` | ascache | 3 | go:generate directive for PolicyType stringer |
| `policytype_string.go` | ascache | 27 | Generated String() for PolicyType |
| `cache_test.go` | ascache | 466 | Unit tests: migration strategies, concurrency |
| `lfu/lfu.go` | lfu | 238 | Thread-safe LFU wrapper with eviction callbacks |
| `lfu/simplelfu/lfu.go` | simplelfu | 245 | Core O(1) LFU algorithm |
| `lfu/internal/list.go` | internal | 156 | Doubly-linked list for frequency buckets |
| `examples/basic/main.go` | main | 241 | HTTP server demo with bandit adapter |
| `examples/migration/main.go` | main | 507 | Migration strategy demo with controllable bandit |

## Module Structure

The project uses separate Go modules:

```
github.com/sshaplygin/as-cache          (root, go 1.25.2, no external deps)
github.com/sshaplygin/as-cache/lfu      (separate module, go 1.25.2, testify for tests)
```

The root module has zero external dependencies. The `lfu` package is a separate
module that only depends on `stretchr/testify` for testing. External dependencies
like `hashicorp/golang-lru/v2` and `stitchfix/mab` are used only in examples.

## Thread Safety Model

- `AdaptiveCache.mu` (sync.RWMutex): guards active policy, migration state, and
  all policy map operations. Read lock for Get/Contains/Keys/Values/Len/Peek/Stats.
  Write lock for Add/Remove/Purge/Resize and policy switching.
- `lfu.Cache.lock` (sync.RWMutex): per-policy lock in the thread-safe LFU wrapper.
  RLock for read-only operations (Contains, Peek, Keys, Values, Len, GetOldest).
  Full lock for mutations (Add, Get, Remove, Purge, Resize).
- Eviction callbacks are always invoked outside of critical sections.

## Data Flow

```
Add(key, value):
  1. Write lock
  2. Shadow-add zero value to all non-active policies
  3. If gradual migrating: mark key as corrupted, drain one old key
  4. Add real value to active policy
  5. Unlock

Get(key):
  1. Read lock
  2. Shadow-get on all non-active policies (updates their stats)
  3. Get from active policy
  4. Unlock
  5. If miss AND gradual migrating: tryPromote(key) with write lock
```

## Migration Strategies

| Strategy | Switch Cost | Miss Spike | Mechanism |
|----------|------------|------------|-----------|
| MigrationCold | O(1) purge | Full miss spike | New policy starts empty |
| MigrationWarm | O(n) copy | None | All keys copied at switch time |
| MigrationGradual | O(1) per op | Gradual decay | Get-promote + Add-drain |

## Extension Points

New policies only need to implement `Cacher[K, V]` (10 methods), then be wrapped
with `CacheWrapper` which adds `Cap()`, `GetStats()`, `ResetStats()`, and
`GetType()` to satisfy the `Policy[K, V]` interface.

The `Bandit` interface (2 methods: `RecordStats`, `SelectPolicy`) is implemented
by the user. The examples show how to adapt the `stitchfix/mab` Thompson Sampling
library.
