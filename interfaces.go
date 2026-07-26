package ascache

var _ Cacher[int, string] = (*AdaptiveCache[int, string])(nil)

// Cacher is the cache interface an eviction policy must satisfy to be used as
// an arm. It is deliberately identical to the method set of
// hashicorp/golang-lru/v2, so an existing cache is usually already a Cacher,
// and so an AdaptiveCache is a drop-in replacement for one.
type Cacher[K comparable, V any] interface {
	Add(key K, value V) (evicted bool)
	Contains(key K) bool
	Get(key K) (value V, ok bool)
	Keys() []K
	Len() int
	Peek(key K) (value V, ok bool)
	Purge()
	Remove(key K) (present bool)
	Resize(size int) (evicted int)
	Values() []V

	// ContainsOrAdd(key K, value V) (ok bool, evicted bool)
	// GetOldest() (key K, value V, ok bool)
	// PeekOrAdd(key K, value V) (previous V, ok bool, evicted bool)
	// RemoveOldest() (key K, value V, ok bool)
}

// CacheStats is the hit/miss accounting a policy exposes so its performance
// can be compared with the other arms.
type CacheStats interface {
	GetStats() PolicyStats
	ResetStats()
}

// Policy is a cache that can serve as one arm of an AdaptiveCache: a Cacher
// that also reports its capacity, its measurements, and which policy it is.
type Policy[K comparable, V any] interface {
	Cacher[K, V]

	// Cap reports the capacity. hashicorp/golang-lru/v2 has no such method,
	// but the adaptive layer needs it: shadow policies run at a reduced
	// capacity and are restored to their full one when promoted.
	Cap() int

	CacheStats
	GetType() PolicyType
}

// Bandit chooses which policy should be active, given what each has measured.
//
// This package ships no implementation, because the choice of strategy is the
// interesting part and depends on how quickly the traffic changes. A
// Thompson-sampling implementation with evidence discounting is in the bench
// module and is short enough to copy.
type Bandit interface {
	// RecordStats delivers one policy's performance report. On every
	// reporting epoch each policy reports — the active policy included — so
	// implementations receive a full set of arms and must not synthesize
	// stats for the active arm themselves. When
	// Settings.EvictPartialCapacityFilling is false, epochs where the active
	// policy is not yet full skip reporting entirely; counters then
	// accumulate and the next report spans the skipped epochs.
	RecordStats(stats ShadowStats)

	// SelectPolicy asks the bandit to choose which policy should become the
	// active one for the next epoch.
	SelectPolicy() PolicyType
}
