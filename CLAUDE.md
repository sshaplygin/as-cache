# CLAUDE.md - as-cache Project Guidelines

## Project Overview

**as-cache** (Adaptive Selection Cache) is an experimental Go library that uses a Multi-Armed Bandit (MAB) statistical approach to automatically select the optimal cache replacement policy at runtime.

Instead of forcing users to choose a fixed eviction algorithm upfront, as-cache runs multiple policies in parallel (shadow caching), measures their hit/miss rates per epoch, and uses Thompson Sampling to select the best-performing policy dynamically.

**Module:** `github.com/sshaplygin/as-cache`
**Go version:** 1.21+
**Status:** Experimental

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      AdaptiveCache                          │
│   (Manages active policy + shadow policies, runs Bandit)    │
└─────────────────────────────────────────────────────────────┘
          │ active          │ shadows
          ▼                 ▼
   ┌─────────────┐   ┌─────────────┐   ┌─────────────┐
   │ CacheWrapper│   │ CacheWrapper│   │ CacheWrapper │
   │   (LRU)     │   │   (LFU)     │   │  (future...) │
   └─────────────┘   └─────────────┘   └─────────────┘
          │                 │
          ▼                 ▼
   ┌─────────────┐   ┌─────────────┐
   │  hashicorp  │   │  lfu/simple │
   │    LRU      │   │    LFU      │
   └─────────────┘   └─────────────┘
          │
          ▼
   ┌─────────────┐
   │   Bandit    │ (Thompson Sampling via stitchfix/mab)
   │  SelectPolicy│
   └─────────────┘
```

**Key mechanism:**
1. Every epoch (configurable duration), the background goroutine calls `bandit.SelectPolicy()`
2. Each policy's hit/miss stats feed Beta distribution parameters
3. Bandit samples from distributions, picks the winner, switches active policy
4. Shadow caches receive dummy adds (no real data) to track comparative metrics

---

## File Structure

```
as-cache/
├── CLAUDE.md                    # This file
├── README.md                    # User-facing documentation
├── go.mod / go.sum              # Root module dependencies
├── generate.go                  # go:generate stringer directive
│
├── interfaces.go                # Core interface definitions
├── models.go                    # PolicyType, PolicyStats, ShadowStats, GlobalStats
├── cache.go                     # AdaptiveCache implementation
├── wrapper.go                   # CacheWrapper (wraps any Cacher, adds stats)
├── policytype_string.go         # Generated: PolicyType.String() via stringer
│
├── lfu/                         # Separate module: LFU cache
│   ├── go.mod / go.sum
│   ├── lfu.go                   # Thread-safe LFU wrapper with eviction callbacks
│   ├── lfu_test.go              # (stub - needs tests)
│   ├── internal/
│   │   └── list.go              # Doubly-linked list for frequency buckets
│   └── simplelfu/
│       ├── lfu.go               # Core LFU algorithm (O(1) add/get/evict)
│       └── lfu_test.go          # (stub - needs tests)
│
└── examples/
    └── basic/
        ├── go.mod / go.sum
        └── main.go              # HTTP server demo (GET/SET endpoints)
```

---

## Core Interfaces

### `Cacher[K, V]` (interfaces.go)
Standard cache interface compatible with `hashicorp/golang-lru/v2`:
```go
Add(key K, value V) bool
Get(key K) (V, bool)
Remove(key K) bool
Keys() []K
Values() []V
Len() int
Peek(key K) (V, bool)
Purge()
Resize(size int) int
Contains(key K) bool
```

### `Policy[K, V]` (interfaces.go)
Extends `Cacher` with capacity tracking and stats:
```go
Cap() int
GetStats() PolicyStats
ResetStats()
GetType() PolicyType
```

### `Bandit` (interfaces.go)
MAB strategy abstraction:
```go
RecordStats(stats ShadowStats)
SelectPolicy() PolicyType
```

---

## Key Types

| Type | Location | Purpose |
|---|---|---|
| `AdaptiveCache[K,V]` | cache.go | Main adaptive cache orchestrator |
| `CacheWrapper[K,V]` | wrapper.go | Wraps any Cacher, adds hit/miss tracking |
| `PolicyType` | models.go | Enum: Undefined, LRU, LFU |
| `MigrationStrategy` | models.go | Enum: MigrationCold, MigrationWarm |
| `PolicyStats` | models.go | Hits + Misses counters |
| `ShadowStats` | models.go | Per-epoch policy performance |
| `GlobalStats` | models.go | Aggregate statistics |

---

## Dependencies

| Package | Version | Role |
|---|---|---|
| `hashicorp/golang-lru/v2` | v2.0.7 | LRU reference implementation |
| `stitchfix/mab` | v0.1.1 | Multi-Armed Bandit (Thompson Sampling) |
| `gonum.org/v1/gonum` | v0.8.2 | Numerical computing (used by mab) |
| `golang.org/x/exp` | indirect | Used by gonum |

---

## Code Patterns

### Generics - All cache types use Go generics
```go
type AdaptiveCache[K comparable, V any] struct { ... }
type Cacher[K comparable, V any] interface { ... }
```

### Thread Safety
- `sync.RWMutex` in `AdaptiveCache` guards policy switching
- `sync.RWMutex` in LFU `Cache` guards all operations
- Eviction callbacks invoked outside critical sections

### Context-Based Lifecycle
```go
func New[K comparable, V any](ctx context.Context, ...) *AdaptiveCache[K, V]
// Background goroutine stops on ctx.Done()
```

### Shadow Caching Pattern
- Active policy stores real key/value pairs
- Shadow policies receive `Add(key, zeroValue)` calls to track access patterns
- Stats are reset each epoch after bandit records them

---

## Development Commands

```bash
# Run root package tests
go test ./...

# Run LFU package tests
cd lfu && go test ./...

# Run example
cd examples/basic && go run main.go

# Regenerate stringer (after modifying PolicyType in models.go)
go generate ./...

# Tidy dependencies
go mod tidy
cd lfu && go mod tidy
cd examples/basic && go mod tidy
```

---

## Current Status & Incomplete Features

### Implemented
- [x] `AdaptiveCache.Add()` and `Get()` with shadow policy tracking
- [x] `AdaptiveCache.Remove()` - delegates to active policy, propagates to shadows
- [x] `AdaptiveCache.Purge()` - purges all policies
- [x] `AdaptiveCache.Resize()` - resizes all policies, returns total eviction count
- [x] `AdaptiveCache.Contains()` - delegates to active policy
- [x] `AdaptiveCache.Keys()` / `Values()` / `Len()` / `Peek()` - delegate to active policy
- [x] `AdaptiveCache.Stats()` - returns cumulative hit/miss from active policy
- [x] Background epoch goroutine with bandit-based policy selection
- [x] `CacheWrapper` with hit/miss statistics
- [x] LFU implementation (simplelfu + thread-safe wrapper) — all methods implemented
- [x] `lfu.Cache`: `Resize`, `ContainsOrAdd`, `PeekOrAdd`, `RemoveOldest`, `GetOldest`
- [x] `simplelfu.LFU`: `Resize`, `GetOldest`, `RemoveOldest`
- [x] Basic example with HTTP server

### Incomplete / TODO

- [x] Data migration between policies on switch — `MigrationStrategy` in `Settings` (`MigrationCold` default, `MigrationWarm` copies all keys from old active to new active)
- [x] Unit tests for LFU packages (simplelfu: 100% coverage, lfu wrapper: 93.2% coverage)
- [ ] Unit tests for root package (cache_test.go, wrapper_test.go -- still empty stubs)
- [ ] Additional policies: Random, 2Q, ARC (mentioned in README but not implemented)
- [ ] README Usage and Idea sections

---

## Implementation Plan

### Phase 1: Test Coverage
Priority: fill empty test stubs before adding new features.

**`lfu/simplelfu/lfu_test.go`** -- DONE (100% coverage)
- Test Add/Get/Contains/Peek/Remove/Purge/Keys/Values/Len
- Test eviction behavior (least-frequently-used item removed)
- Test frequency increment on repeated access
- Edge cases: empty cache, single item, duplicate keys
- Bug fixes applied: removed double Freq increment in Add, fixed Keys/Values slice init

**`lfu/lfu_test.go`** -- DONE (93.2% coverage; uncovered methods are panic stubs)
- Test thread-safe wrapper around simplelfu
- Test eviction callbacks (buffered channel, DefaultEvictedBufferSize=16)
- Test concurrent Add/Get under race detector
- Concurrent tests for mixed operations, purge-while-reading, keys/values

**Root package tests (`cache_test.go`, `wrapper_test.go`)**
- `CacheWrapper`: hit/miss stats tracking, GetStats/ResetStats
- `AdaptiveCache`: Add/Get with epoch-based switching
- Test bandit integration with mock bandit
- Test context cancellation stops background goroutine

### Phase 2: Complete AdaptiveCache Methods
Implement the missing methods that currently return zero values:
- `Remove(key K)`: delegate to active policy, propagate to shadows
- `Purge()`: purge all policies
- `Contains(key K)`: check active policy
- `Keys()` / `Values()` / `Len()` / `Peek()`: delegate to active policy
- `Resize(size int)`: resize all policies
- `Stats()`: aggregate hit/miss from all wrappers

### Phase 3: Policy Migration on Switch -- DONE

`MigrationStrategy` enum added to `models.go`; `Settings.MigrationStrategy` field controls behaviour:

- `MigrationCold` (default, 0): start fresh — simple, causes temporary miss spike
- `MigrationWarm`: on switch, purge zero-value shadow entries from new active policy, then copy all key/value pairs from old active via `Keys()`+`Peek()`

- `MigrationGradual`: Get-time promotion (miss in new active → peek old policy → add to new active) + Add-time drain (one key migrated per Add call). Migration window closes when all keys are drained, on `Purge()`, or at the next epoch boundary.

Bug fix applied during implementation: all three strategies now purge shadow zero-value entries from the new active policy at switch time, so callers never observe a shadow zero as a real cached value.

### Phase 4: Additional Policies
Add wrappers for:
- `hashicorp/golang-lru/v2/expirable` (TTL-based)
- `hashicorp/golang-lru/v2` 2Q variant
- Random eviction policy

Each new policy only needs to implement the `Cacher` interface and be wrapped by `CacheWrapper`.

---

## Testing Guidelines

- Use `go test -race ./...` to catch race conditions (mandatory given concurrent design)
- Mock the `Bandit` interface to test policy switching deterministically
- Use table-driven tests for cache operation coverage
- Test epoch transitions with short durations (e.g., 1ms) in tests
- Minimum 80% coverage target

---

## Rules

1. No emojis in code, comments, or documentation
2. All public types must have godoc comments
3. Run `go vet ./...` before committing
4. Keep each file under 400 lines; split by responsibility
5. No `panic` except in initialization failures or truly unimplemented stubs
6. Eviction callbacks must not be called while holding a mutex
7. All new policies must implement `Cacher[K, V]` and be wrapped via `CacheWrapper`
8. Update `PolicyType` enum and regenerate stringer when adding new policies
