package ascache

var _ Cacher[int, string] = (*AdaptiveCache[int, string])(nil)

// cache interface comparable from hashicorp/golang-lru/v2 cache's
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

type CacheStats interface {
	GetStats() PolicyStats
	ResetStats()
}

type Policy[K comparable, V any] interface {
	Cacher[K, V]
	// hashicorp/golang-lru/v2 doesn't have this method
	Cap() int

	CacheStats
	GetType() PolicyType
}

type Bandit interface {
	// RecordStats delivers a performance report from one of the shadow caches
	// for the previous epoch.
	RecordStats(stats ShadowStats)

	// SelectPolicy asks the bandit to choose which policy should become the
	// active one for the next epoch.
	SelectPolicy() PolicyType
}
