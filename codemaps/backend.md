# Backend Implementation Codemap

**Last Updated:** 2026-02-23

## Root Package (`github.com/sshaplygin/as-cache`)

### interfaces.go -- Core Abstractions

```go
// Cacher[K, V] -- Standard cache interface (compatible with hashicorp/golang-lru/v2)
// Methods: Add, Get, Remove, Keys, Values, Len, Peek, Purge, Resize, Contains

// CacheStats -- Hit/miss statistics
// Methods: GetStats() PolicyStats, ResetStats()

// Policy[K, V] -- Extends Cacher with Cap(), CacheStats, GetType()

// Bandit -- MAB strategy abstraction
// Methods: RecordStats(ShadowStats), SelectPolicy() PolicyType
```

Compile-time check: `var _ Cacher[int, string] = (*AdaptiveCache[int, string])(nil)`

### cache.go -- AdaptiveCache Orchestrator

**Constructor:** `NewAdaptiveCache(policies, bandit, settings) (*AdaptiveCache, error)`
- Validates at least one policy exists (returns `ErrEmptyPolicies` otherwise)
- Creates internal context for background goroutine lifecycle
- Builds `map[PolicyType]Policy` from the policy slice
- First policy in the slice becomes the initial active policy
- Starts `runAdaptiveSelect()` goroutine

**Exported Types:**

| Type | Fields | Purpose |
|------|--------|---------|
| `Settings` | EpochDuration, EvictPartialCapacityFilling, MigrationStrategy | Cache configuration |
| `EvictCallback[K,V]` | func(K, V) | Eviction notification |

**AdaptiveCache Fields:**

| Category | Fields |
|----------|--------|
| Data Plane | activePolicy, oldPolicy, policies map, onEvict |
| Migration | migrating, migrateFrom, migrationKeys, migrationRealKeys |
| Control Plane | bandit |
| Settings | epochID, epochTicker, settings, ctx, cancel |

**Key Methods:**

| Method | Lock | Behavior |
|--------|------|----------|
| `Get(key)` | RLock, then Lock for promote | Shadow-get on all non-active; promote on miss if gradual |
| `Add(key, value)` | Lock | Shadow-add zero to non-active; drain one key if gradual |
| `Remove(key)` | Lock | Remove from all policies; clear from migration keys |
| `Purge()` | Lock | Purge all policies; clear migration state |
| `Resize(size)` | Lock | Resize all policies; return total evicted count |
| `Contains(key)` | RLock | Delegate to active only |
| `Keys()` | RLock | Delegate to active only |
| `Values()` | RLock | Delegate to active only |
| `Len()` | RLock | Delegate to active only |
| `Peek(key)` | RLock | Delegate to active only |
| `Stats()` | RLock | Return GlobalStats from active policy |
| `ActivePolicy()` | RLock | Return current PolicyType |
| `Close()` | none | Cancel context, stop ticker |

**Internal Methods:**

| Method | Purpose |
|--------|---------|
| `runAdaptiveSelect()` | Background goroutine: epoch timer loop |
| `tryChangePolicy()` | Collect shadow stats, ask bandit, switch if needed |
| `migrateData(from, to)` | Execute migration strategy on policy switch |
| `clearMigrationState()` | Reset all gradual migration fields |
| `drainOneKey()` | Pop and migrate one key from migration queue |
| `tryPromote(key)` | Get-miss promotion during gradual migration |

### wrapper.go -- CacheWrapper

**Constructor:** `NewCache(cache Cacher, policy PolicyType, size int) *CacheWrapper`

Wraps any `Cacher[K, V]` to implement `Policy[K, V]` by adding:
- Hit/miss counting in `Get()` (increments stats.Hits or stats.Misses)
- `Cap()` returns the configured size
- `Name()` returns lowercase policy type string
- `GetType()` returns the PolicyType
- `GetStats()` / `ResetStats()` for epoch-based statistics

All other Cacher methods are delegated via embedding.

---

## LFU Package (`github.com/sshaplygin/as-cache/lfu`)

Separate Go module. License: MPL-2.0 (Copyright IBM Corp. 2014, 2025).

### lfu/lfu.go -- Thread-Safe LFU Wrapper

**Constructor:** `New[K, V](size) (*Cache, error)` / `NewWithEvict[K, V](size, onEvicted) (*Cache, error)`

| Method | Lock Type | Notes |
|--------|-----------|-------|
| `Add` | Lock | Eviction callback invoked outside lock |
| `Get` | Lock | Full lock (updates frequency) |
| `Contains` | RLock | Read-only check |
| `Peek` | RLock | No frequency update |
| `Remove` | Lock | Eviction callback invoked outside lock |
| `Purge` | Lock | Callbacks invoked outside lock for each evicted entry |
| `Resize` | Lock | Callbacks invoked outside lock |
| `ContainsOrAdd` | Lock | Atomic check-and-add |
| `PeekOrAdd` | Lock | Atomic peek-and-add |
| `RemoveOldest` | Lock | Evicts least-frequent item |
| `GetOldest` | RLock | Returns least-frequent without removal |
| `Keys` | RLock | Sorted by frequency (low to high) |
| `Values` | RLock | Sorted by frequency (low to high) |
| `Len` | RLock | Item count |

Eviction buffering: uses `DefaultEvictedBufferSize = 16` pre-allocated slices to
collect evicted keys/values inside the lock, then invokes callbacks after unlocking.

### lfu/simplelfu/lfu.go -- Core O(1) LFU Algorithm

**Constructor:** `NewLFU[K, V](size, onEvict) (*LFU, error)`

Internal data structures:
- `items map[K]*internal.Entry[K, V]` -- O(1) key lookup
- `evictList map[int]*internal.LfuList[K, V]` -- frequency buckets
- `minFreq int` -- tracks the minimum frequency for O(1) eviction
- `size int` -- maximum capacity

Algorithm:
- `Add`: existing key updates value and increments frequency. New key evicts
  least-frequent if at capacity, then inserts at frequency 1.
- `Get`: returns value and increments frequency via `updateFreq`.
- `updateFreq`: removes entry from old frequency bucket, inserts into
  frequency+1 bucket. Updates minFreq if old bucket becomes empty.
- Eviction: always removes from `evictList[minFreq].Back()` (LFU + FIFO tiebreak).

### lfu/internal/list.go -- Doubly-Linked List

Generic doubly-linked list implemented as a ring with a sentinel root node.

**Entry[K, V]** fields: Key, Value, Freq, ExpiresAt, ExpireBucket, next, prev, list

**LfuList[K, V]** operations:
- `PushFront(k, v)` / `PushFrontFreq(k, v, freq)` -- insert at front
- `Back()` -- return last element (eviction candidate)
- `Remove(e)` -- unlink entry
- `MoveToFront(e)` -- reorder
- `Length()` -- O(1) count
- Expirable variants: `PushFrontExpirable`, `PushFrontFreqExpirable`

---

## Examples

### examples/basic/main.go

HTTP server (`:8080`) demonstrating adaptive cache with LRU + LFU policies.
Implements `StitchFixBanditAdapter` wrapping `stitchfix/mab` Thompson Sampling.
Endpoints: `/get?key=`, `/set?key=&name=&email=`.

### examples/migration/main.go

HTTP server (`:8081`) demonstrating all three migration strategies.
Adds a `controllableBandit` wrapper that allows forcing policy switches via HTTP.
Endpoints: `/get`, `/set`, `/keys`, `/stats`, `/switch?to=lfu|lru`, `/demo`.
The `/demo` endpoint runs a complete seed-switch-verify cycle and returns a JSON report.

---

## Test Coverage

| File | Package | Status | Coverage |
|------|---------|--------|----------|
| `cache_test.go` | ascache | Active | Migration strategies, concurrency |
| `lfu/lfu_test.go` | lfu | Active | ~93.2% |
| `lfu/simplelfu/lfu_test.go` | simplelfu | Active | ~100% |

Test helpers in `cache_test.go`:
- `mockBandit` -- deterministic policy selection
- `mockPolicy[K, V]` -- map-backed Policy implementation
- `makeCache()` -- creates AdaptiveCache with LRU+LFU mocks
- `triggerSwitch()` -- forces policy switch bypassing epoch timer
