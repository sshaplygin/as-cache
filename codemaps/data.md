# Data Models Codemap

**Last Updated:** 2026-02-23

## Enums

### PolicyType (models.go)

```go
type PolicyType uint

const (
    Undefined PolicyType = iota  // 0 -- zero value, invalid
    LRU                          // 1 -- Least Recently Used
    LFU                          // 2 -- Least Frequently Used
)
```

Generated `String()` method via `go:generate stringer -type=PolicyType` in
`generate.go`. Output in `policytype_string.go`.

### MigrationStrategy (models.go)

```go
type MigrationStrategy uint

const (
    MigrationCold    MigrationStrategy = iota  // 0 -- start fresh (default)
    MigrationWarm                               // 1 -- copy all keys at switch
    MigrationGradual                            // 2 -- lazy drain + promote
)
```

No generated stringer. Used in `Settings.MigrationStrategy`.

## Stats Structures

### PolicyStats (models.go)

```go
type PolicyStats struct {
    Hits   int64
    Misses int64
}
```

Per-policy hit/miss counters. Tracked by `CacheWrapper`. Reset each epoch by
`tryChangePolicy()` after recording to the bandit.

### ShadowStats (models.go)

```go
type ShadowStats struct {
    Policy PolicyType
    Hits   int64
    Misses int64
}
```

Passed to `Bandit.RecordStats()` at each epoch boundary. Contains the shadow
policy's type and its hit/miss counts for the elapsed epoch.

### GlobalStats (models.go)

```go
type GlobalStats struct {
    Hits   int64
    Misses int64
}
```

Returned by `AdaptiveCache.Stats()`. Contains cumulative hit/miss from the
currently active policy only.

## Configuration

### Settings (cache.go)

```go
type Settings struct {
    EpochDuration               time.Duration
    EvictPartialCapacityFilling bool
    MigrationStrategy           MigrationStrategy
}
```

| Field | Default | Purpose |
|-------|---------|---------|
| `EpochDuration` | (required) | How often the bandit re-evaluates policies |
| `EvictPartialCapacityFilling` | false | When false, policy switching is deferred until cache is full |
| `MigrationStrategy` | MigrationCold (0) | Controls data transfer on policy switch |

## Callback Types

### EvictCallback (cache.go)

```go
type EvictCallback[K comparable, V any] func(key K, value V)
```

Declared on AdaptiveCache but not yet wired (the `onEvict` field exists but is
not set by the constructor).

### simplelfu.EvictCallback (lfu/simplelfu/lfu.go)

```go
type EvictCallback[K comparable, V any] func(key K, value V)
```

Used by both `simplelfu.LFU` and `lfu.Cache`. Called when entries are evicted
due to capacity limits, explicit removal, purge, or resize.

## Internal Data Structures

### internal.Entry (lfu/internal/list.go)

```go
type Entry[K comparable, V any] struct {
    next, prev   *Entry[K, V]
    list         *LfuList[K, V]
    Key          K
    Value        V
    ExpiresAt    time.Time
    ExpireBucket uint8
    Freq         int
}
```

Doubly-linked list node used in frequency buckets. `Freq` tracks the access
count. `ExpiresAt` and `ExpireBucket` are present for future TTL support but
are not currently used by the LFU algorithm.

### internal.LfuList (lfu/internal/list.go)

```go
type LfuList[K comparable, V any] struct {
    root Entry[K, V]  // sentinel
    len  int
}
```

Ring-based doubly-linked list. Each frequency bucket in the LFU maps to one
`LfuList`. New entries are pushed to the front; eviction candidates are taken
from the back (FIFO tiebreaking within the same frequency).

### simplelfu.LFU Internal Maps

```go
items     map[K]*internal.Entry[K, V]       // O(1) key -> entry lookup
evictList map[int]*internal.LfuList[K, V]   // frequency -> linked list of entries
minFreq   int                                // current minimum frequency
```

## AdaptiveCache Internal State

### Policy Map

```go
policies map[PolicyType]Policy[K, V]
```

Maps each registered PolicyType to its Policy instance. Populated at construction
from the policies slice.

### Migration State (gradual only)

```go
migrating         bool              // true during active gradual migration
migrateFrom       PolicyType        // source policy for promotion/drain
migrationKeys     []K               // snapshot of source keys at switch time
migrationRealKeys map[K]struct{}    // keys safe to promote (not corrupted by shadow Add)
```

The migration window closes when:
1. All keys in `migrationRealKeys` have been drained or promoted
2. `Purge()` is called
3. The next epoch boundary triggers `clearMigrationState()`

## Errors

| Variable | Package | Value |
|----------|---------|-------|
| `ErrEmptyPolicies` | ascache | `"must provide non zero policies size"` |
| (anonymous) | simplelfu | `"must provide a positive size"` |

## Constants

| Constant | Package | Value | Purpose |
|----------|---------|-------|---------|
| `DefaultEvictedBufferSize` | lfu | 16 | Pre-allocated eviction buffer capacity |
