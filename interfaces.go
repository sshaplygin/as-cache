package ascache

var _ Cache[int, string] = (*AdaptiveCache[int, string])(nil)

type Cache[K comparable, V any] interface {
	Get(key K) (V, bool)
	Set(key K, value V)
	Del(key K)
}

type CacheStats interface {
	Stats() GlobalStats
}

type EvictionPolicy[K comparable, V any] interface {
	Get(key K) (V, bool)
	Set(key K, value V)
}

type ShadowCache[K comparable] interface {
	Name() string

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
	SelectPolicy() (policyName string)
}
