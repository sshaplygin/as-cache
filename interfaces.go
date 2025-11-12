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
	Stats() GlobalStats
}

type Policy[K comparable, V any] interface {
	Cacher[K, V]
	GetType() PolicyType
}

type ShadowCache[K comparable] interface {
	GetType() PolicyType

	// Access симулирует доступ к ключу (Get или Put).
	// Возвращает 'true', если это был "хит" (ключ уже был в кеше).
	// Возвращает 'false', если это был "промах".
	Access(key K) (wasHit bool)

	// GetStatsAndReset возвращает собранную статистику за эпоху
	// и немедленно сбрасывает счетчики.
	GetStatsAndReset() ShadowStats
}

type Bandit interface {
	// RecordStats получает отчет о производительности от одного из
	// "сенсоров" (теневых кешей) за прошедшую эпоху.
	RecordStats(stats ShadowStats)

	// SelectPolicy просит бандита выбрать, какая политика
	// должна стать "основной" (active) на следующую эпоху.
	SelectPolicy() PolicyType
}
