# CLAUDE.md - as-cache Project Guidelines

## Project Overview

**as-cache** (Adaptive Selection Cache) is an experimental Go library that uses a Multi-Armed Bandit (MAB) statistical approach to automatically select the optimal cache replacement policy at runtime.

Instead of forcing users to choose a fixed eviction algorithm upfront, as-cache runs multiple policies in parallel (shadow caching), measures their hit/miss rates per epoch, and uses Thompson Sampling to select the best-performing policy dynamically.

**Module:** `github.com/sshaplygin/as-cache`
**Go version:** 1.25+
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
├── errors.go                    # Sentinel errors returned by NewAdaptiveCache
├── settings.go                  # Settings + NewAdaptiveCache validation
├── cache.go                     # AdaptiveCache struct + public cache API
├── epoch.go                     # Epoch loop, bandit reporting, policy selection
├── migration.go                 # Migration strategies (cold/warm/gradual)
├── shadow.go                    # Promotion/demotion, shadow duty, value dropping
├── sampling.go                  # keySampler + miniature capacity maths
├── stability.go                 # Switch gates (cool-down, min improvement)
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
| `stretchr/testify` | v1.11.1 | Test assertions (root module, test-only) |

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

# Lint all modules (golangci-lint v2, config in .golangci.yml)
make lint          # or: golangci-lint run ./...  (per module)
make lint-fix      # apply --fix and formatters
make install-tools # install the pinned golangci-lint version

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
- [x] `AdaptiveCache.Stats()` - cumulative: completed epochs (`globalStats`) plus the active policy's in-progress epoch
- [x] Background epoch goroutine with bandit-based policy selection
- [x] `CacheWrapper` with hit/miss statistics
- [x] LFU implementation (simplelfu + thread-safe wrapper) — all methods implemented
- [x] `lfu.Cache`: `Resize`, `ContainsOrAdd`, `PeekOrAdd`, `RemoveOldest`, `GetOldest`
- [x] `simplelfu.LFU`: `Resize`, `GetOldest`, `RemoveOldest`
- [x] Basic example with HTTP server
- [x] Roadmap Milestone 1 (correctness): locked epoch switch in `runEpoch`; atomic
  `CacheWrapper` counters; active policy's per-epoch stats reported to the bandit
  alongside shadows with all counters reset each epoch (active counts folded into
  `AdaptiveCache.globalStats` so `Stats()` stays cumulative and demotion never
  leaks active-tenure stats into a shadow epoch); constructor validation with
  sentinel errors in `errors.go` (`ErrNilSettings`, `ErrInvalidEpochDuration`,
  `ErrNilBandit`, `ErrNilPolicy`, `ErrDuplicatePolicy`); idempotent `Close()`
  (`sync.Once`) that waits for the epoch goroutine (`sync.WaitGroup`); explicit
  `go vet` CI step and `TestAdaptiveCache_StressAcrossEpochBoundaries` (1ms
  epochs, all three migration strategies, full public API under `-race`).
  Migration strategies moved verbatim from `cache.go` to `migration.go` to keep
  files under the 400-line rule.

- [x] Roadmap Milestone 2 (overhead reduction), three of four items:
  - **Sampled shadow caching** (`sampling.go`, `shadow.go`): `Settings.ShadowSampleRate`
    gates the shadow fan-out on `maphash.Comparable(seed, key) < threshold`. One
    sampler is shared by every policy so all arms measure the same substream --
    per-policy seeds would make their hit rates incomparable. Shadows resize to
    `ceil(rate*Cap)` so each stays a faithful miniature; `MinShadowCapacity`
    (default 256) floors that, raising the *effective rate* rather than only the
    capacity, and disabling sampling when a cache is too small to host a useful
    miniature.
  - **Stats deviate from the roadmap wording deliberately**: sampled counts are
    NOT scaled up by `1/rate`. That would restore magnitude while inventing
    confidence, handing a Beta posterior 20x the evidence collected. Instead the
    *active* arm is measured over the same sampled substream
    (`activeSampledHits`/`activeSampledMisses`, reported by `selectPolicyLocked`),
    so every arm carries equal, honest evidence. `Stats()` still reports real
    unsampled traffic; only the bandit sees the sample.
  - **Value dropping on demotion** (`demoteLocked`): a demoted policy keeps its
    keys (that is its eviction bookkeeping) but its entries are rewritten to the
    zero value, unsampled keys removed, then it shrinks to miniature capacity.
    Rewriting in `Keys()` order preserves LRU recency and leaves LFU relative
    order untouched. Deferred while a gradual window is open, since that window
    promotes out of the source's real values.
  - **Switch stability** (`stability.go`): `MinHitRateImprovement`,
    `SwitchCooldownEpochs`, `MinEpochRequests`, all off at their zero values.
  - **Ordering invariant** (documented on `switchLocked`): *every mutation of a
    policy must happen while that policy is not the active one.* Mutate the
    incoming policy before promoting it, the outgoing one after demoting it.
    This is what keeps a caller from ever reading a policy mid-rewrite.
  - **Test policies**: `mockPolicy` does not enforce capacity (its `Resize` only
    records the new size). That blind spot hid three capacity bugs found in
    review. `evictingPolicy` in `evicting_test.go` does enforce it -- use it for
    anything whose subject is capacity, resizing, or eviction.
  - Measured on an M1 Max with mutex-backed stub policies: `Get` 100 -> 36 ns/op
    with one shadow, 145 -> 38 ns/op with three; `Add` 109 -> 52 and 189 -> 57;
    mixed parallel 184 -> 85. The structural win is that cost becomes flat in the
    number of shadows, so adding policies is nearly free.
  - **Deferred: lock-free reads** (`atomic.Pointer` to the active policy). An
    adversarial safety analysis found the usual seqlock-retry framing unsound and
    surfaced five further hazards -- retry must wrap all six read delegations
    (`Values()` on a stale post-drop state returns N zeros at the right length),
    retries are not side-effect-free (double-counted stats, double-bumped
    recency), the `GetStats`/`ResetStats` split silently discards hits once reads
    are unlocked (fixing it changes the public `CacheStats` interface), and
    `MigrationGradual` cannot go lock-free since `promoteLocked` mutates from
    inside `Get`. Worth its own scoped change.

### Incomplete / TODO

- [x] Data migration between policies on switch — `MigrationStrategy` in `Settings` (`MigrationCold` default, `MigrationWarm` copies all keys from old active to new active)
- [x] Unit tests for LFU packages (simplelfu: 98.3% coverage, lfu wrapper: 100% coverage)
- [x] Unit tests for root package (cache_test.go: 93.6% coverage -- CacheWrapper, AdaptiveCache delegated methods, tryChangePolicy, epoch-based switching, constructor edge cases, concurrent access)
- [ ] Additional policies: Random, 2Q, ARC (mentioned in README but not implemented)
- [ ] README Usage and Idea sections

---

## Implementation Plan

### Phase 1: Test Coverage
Priority: fill empty test stubs before adding new features.

**`lfu/simplelfu/lfu_test.go`** -- DONE (98.3% coverage)
- Test Add/Get/Contains/Peek/Remove/Purge/Keys/Values/Len
- Test eviction behavior (least-frequently-used item removed)
- Test frequency increment on repeated access
- Edge cases: empty cache, single item, duplicate keys
- Bug fixes applied: removed double Freq increment in Add, fixed Keys/Values slice init
- Bucket-index invariant enforced: every bucket in `evictList` holds at least one
  entry, and `minFreq` always addresses a live bucket while the cache is non-empty.
  `Add`'s eviction path and `removeElement` previously left an emptied bucket in
  the map, so a later `minFreq` recompute selected it and panicked on a nil
  dereference (`GetOldest`/`RemoveOldest`/`Resize`). Entry removal now funnels
  through `detach`/`recomputeMinFreq`, `Add` only evicts when the cache is
  non-empty (so `Resize(0)` + `Add` cannot panic), and the lookup helpers degrade
  to a miss instead of panicking if the index is ever corrupted.

**`lfu/lfu_test.go`** -- DONE (100% coverage)

- `Resize`, `ContainsOrAdd`, `PeekOrAdd`, `RemoveOldest` and `GetOldest` are
  covered directly; they are fully implemented (there are no panic stubs) but
  were previously untested, which is why the simplelfu bucket-index panics went
  unnoticed -- those methods are their public entry points.
- Test thread-safe wrapper around simplelfu
- Test eviction callbacks (buffered channel, DefaultEvictedBufferSize=16)
- Test concurrent Add/Get under race detector
- Concurrent tests for mixed operations, purge-while-reading, keys/values

**Root package tests (`cache_test.go`)** -- DONE (93.6% coverage)
- `CacheWrapper`: hit/miss stats tracking, GetStats/ResetStats, Cap, Name, GetType, delegated methods
- `AdaptiveCache`: Stats, Resize, Contains, Keys, Values, Len, Peek, ActivePolicy
- `AdaptiveCache`: Add/Get with epoch-based switching, tryChangePolicy (switch, no-switch, skip-when-not-full)
- Test bandit integration with mock bandit (recordingBandit verifies shadow stats delivery)
- Test context cancellation stops background goroutine (Close)
- Constructor edge cases: empty policies, nil policies
- Remove propagates to shadows, Purge clears all policies
- Concurrent access to all delegated read methods under race detector
- Migration strategy tests: Cold, Warm, Gradual (promotion, drain, zero-value safety, concurrent)
- All assertions use `testify/require` (fatal) and `testify/assert` (non-fatal) — no bare `t.Fatal`/`t.Error`
- Note: `tryChangePolicy()` returns `PolicyType` (the bandit's selection), not `bool`; tests compare against `LRU`/`LFU` constants

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

- `MigrationGradual`: Get-time promotion (during the window, `Get` takes the write lock and promotes the eligible key from the old policy into the new active BEFORE the counted lookup, so promoted requests register as active-policy hits — not misses) + Add-time drain (one key migrated per Add call). Migration window closes when no eligible keys remain (drained, promoted, or removed), on `Purge()`, on the next policy switch, and unconditionally at the next epoch boundary (`runEpoch` calls `closeMigrationLocked` first, so a workload that stops touching pending keys cannot leave the source pinned at full capacity holding real values). Switching away from an empty policy never opens a window.

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
- Use `require.*` for assertions that must stop the test on failure (setup, preconditions)
- Use `assert.*` for assertions where the test can continue (value checks, multiple independent conditions)
- Do not use bare `t.Fatal`, `t.Fatalf`, `t.Error`, or `t.Errorf` — use testify instead
- All three test packages (`ascache`, `lfu`, `simplelfu`) use `github.com/stretchr/testify`

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
