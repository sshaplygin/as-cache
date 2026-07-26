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
	// During the window each Get() promotes the requested key from the old
	// policy into the new active — when it is still eligible (not overwritten
	// by a shadow Add, already promoted, or evicted from the source) — before
	// the lookup is counted, so served requests register as hits; each Add()
	// call migrates at most one additional key. While the window is open,
	// Get() takes the write lock, serializing reads.
	//
	// The window closes when no eligible keys remain, on Purge(), on the next
	// policy switch, and in any case at the next epoch boundary - a workload
	// that simply stops touching the pending keys must not leave it open
	// forever, holding the source at full capacity with its values retained.
	MigrationGradual
)

// GlobalStats holds aggregate hit/miss statistics exposed to callers.
type GlobalStats struct {
	Hits   int64
	Misses int64
}

type PolicyStats struct {
	Hits   int64
	Misses int64
}

// ShadowStats holds one policy's hit/miss counts since its last report —
// normally one epoch, or several when reporting was skipped because the cache
// was not yet full (EvictPartialCapacityFilling=false). Every policy reports
// through this channel on each reporting epoch, the active policy included,
// so the bandit's posterior for the active arm does not go stale while
// reports flow.
type ShadowStats struct {
	Policy PolicyType
	Hits   int64
	Misses int64
}
