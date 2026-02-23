package ascache

// PolicyType identifies a cache replacement policy.
type PolicyType uint

const (
	Undefined PolicyType = iota
	LRU
	LFU
)

// MigrationStrategy controls how key/value pairs are transferred when the
// active policy changes.
type MigrationStrategy uint

const (
	// MigrationCold starts the new active policy from an empty state. This is
	// the simplest strategy but causes a temporary cache-miss spike after every
	// policy switch.
	MigrationCold MigrationStrategy = iota

	// MigrationWarm copies all key/value pairs from the old active policy into
	// the new active policy at switch time. Shadow zero-value entries in the
	// target policy are purged first so that only real values are served.
	MigrationWarm

	// MigrationGradual lazily drains the old active policy into the new one.
	// Each Get() miss attempts to promote the key from the old policy; each
	// Add() call migrates one additional key. The migration window closes at
	// the next epoch boundary, on Purge(), or when all keys have been drained.
	MigrationGradual
)

// GlobalStats — структура для внешней статистики.
type GlobalStats struct {
	Hits   int64
	Misses int64
}

type PolicyStats struct {
	Hits   int64
	Misses int64
}

// ShadowStats — результат работы "сенсора" за эпоху.
type ShadowStats struct {
	Policy PolicyType
	Hits   int64
	Misses int64
}
