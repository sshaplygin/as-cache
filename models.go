package ascache

// GlobalStats — структура для внешней статистики.
type GlobalStats struct {
	Hits   int64
	Misses int64
}

// ShadowStats — результат работы "сенсора" за эпоху.
type ShadowStats struct {
	PolicyName string
	Hits       int64
	Misses     int64
}
