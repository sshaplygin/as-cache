package ascache

// PolicyType identifies a cache replacement policy.
type PolicyType uint

const (
	// Undefined is the zero value and names no policy.
	Undefined PolicyType = iota
	// LRU evicts the least recently used entry.
	LRU
	// LFU evicts the least frequently used entry.
	LFU
	// TwoQueue evicts using the 2Q algorithm, which keeps a small recent-access
	// queue in front of a frequently-accessed queue so a scan cannot flush the
	// working set.
	TwoQueue
	// ARC evicts using Adaptive Replacement Cache, which balances recency
	// against frequency on its own. The algorithm is patented by IBM, so its
	// adapter lives in a separate module that nothing else depends on.
	ARC
	// Random evicts an arbitrary entry. It is a useful control arm: a policy
	// that cannot beat random on a workload is not earning its bookkeeping.
	Random
	// TTL evicts by expiry as well as by recency.
	TTL
	// TinyLFU evicts using the W-TinyLFU family, which gates admission on a
	// frequency sketch so a new key must earn its place against the entry it
	// would displace. It is the strongest general-purpose baseline in wide
	// use, and the one an adaptive cache has to beat to justify itself.
	TinyLFU
)

// MigrationStrategy controls how key/value pairs are transferred when the
// active policy changes.
type MigrationStrategy uint

const (
	// MigrationCold starts the new active policy from an empty state. This is
	// the simplest strategy but causes a temporary cache-miss spike after every
	// policy switch.
	MigrationCold MigrationStrategy = iota + 1

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

// EpochReport is one reporting epoch's complete set of measurements, delivered
// to a bandit that implements EpochBandit.
//
// It exists because the per-arm ShadowStats stream loses three things a bandit
// coordinating with anything outside the process needs: which epoch the
// numbers belong to, where the epoch ends, and which arm was active.
type EpochReport struct {
	// EpochID is the cache's epoch counter at the time of the report. It
	// counts ticks, including those the EvictPartialCapacityFilling gate
	// skipped, so consecutive reports are not necessarily consecutive IDs.
	// It is process-local: two caches in two processes share no origin, so it
	// orders one cache's reports and nothing more.
	EpochID int64

	// Active is the policy that was serving traffic during the epoch. Its
	// counts were measured at full capacity over the sampled substream; every
	// other arm's were measured on a miniature of that capacity. The rates are
	// comparable by construction, but not identically measured, and pooling
	// one arm's active-role numbers with another's shadow-role numbers gives
	// the active one a systematic advantage.
	Active PolicyType

	// Stats holds one entry per arm, ordered by PolicyType so the report is
	// reproducible, and carries the same counts RecordStats would have
	// delivered individually.
	Stats []ShadowStats

	// Capacity is the nominal capacity of the active policy: the size the
	// cache actually serves at.
	//
	// It is reported because a hit rate only means something alongside the
	// capacity it was measured at. A bandit pooling evidence from several
	// caches has to refuse to pool measurements taken at different sizes -
	// otherwise it averages a 1000-entry cache's hit rate with a 100-entry
	// cache's and acts on a number that describes neither.
	Capacity int

	// SampleRate is the fraction of the keyspace the measurements cover, 1
	// when Settings.ShadowSampleRate is off. Like Capacity, it is part of what
	// makes two caches' numbers comparable: shadows run as miniatures scaled
	// to this rate, so two caches sampling differently are simulating
	// different things.
	SampleRate float64
}
