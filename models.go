package ascache

type PolicyType uint

const (
	Undefined PolicyType = iota
	LRU
	LFU
)

// GlobalStats — структура для внешней статистики.
type GlobalStats struct {
	Hits   int64
	Misses int64
}

// ShadowStats — результат работы "сенсора" за эпоху.
type ShadowStats struct {
	Policy PolicyType
	Hits   int64
	Misses int64
}
