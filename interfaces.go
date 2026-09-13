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
// interesting part and depends on how quickly the traffic changes. Ready-made
// ones live in the companion github.com/sshaplygin/as-cache/bandit module:
// a local Thompson sampler and a greedy control.
//
// # What a slow implementation costs
//
// Both methods are called with no cache lock held, so an implementation that
// takes its time delays the switch it is deciding and the epoch after it, but
// not the cache's own operations: concurrent Get and Add are unaffected. A
// bandit that talks to a network or a disk is therefore allowed, though the
// selection it produces is applied later than the epoch that asked for it, and
// is dropped entirely if a further epoch has collected in the meantime.
//
// One caller does still wait: under Settings.EpochRequests the Get that
// completes an epoch runs it, so that one call pays for the bandit as it
// already pays for the switch and any migration. Under Settings.EpochDuration
// the work is on the background goroutine and no caller waits at all.
//
// Calls are serialised: no implementation is entered from two goroutines at
// once, and the reports for one epoch arrive before that epoch's selection is
// asked for.
type Bandit interface {
	// RecordStats delivers one policy's performance report. On every
	// reporting epoch each policy reports — the active policy included — so
	// implementations receive a full set of arms and must not synthesize
	// stats for the active arm themselves. When
	// Settings.EvictPartialCapacityFilling is false, epochs where the active
	// policy is not yet full skip reporting entirely; counters then
	// accumulate and the next report spans the skipped epochs.
	//
	// It is not called on a bandit that also implements EpochBandit; that
	// interface's RecordEpoch replaces it.
	RecordStats(stats ShadowStats)

	// SelectPolicy asks the bandit to choose which policy should become the
	// active one for the next epoch.
	//
	// Returning a policy the cache was not built with - Undefined included,
	// which is the natural answer from a bandit that has not yet formed an
	// opinion - is not an error and means no change.
	SelectPolicy() PolicyType
}

// EpochBandit is an optional extension of Bandit for implementations that need
// to see a reporting epoch as a whole rather than as a sequence of per-policy
// calls.
//
// RecordStats hands over one arm at a time with no epoch identifier, no marker
// for where one epoch ends and the next begins, and no indication of which arm
// was serving traffic. That is enough for a bandit that only accumulates
// posteriors, and not enough for one that has to publish an epoch's evidence
// somewhere else - which needs to know what to key it by, when the epoch is
// complete, and that the active arm's numbers were measured at full capacity
// while every shadow's were measured on a miniature.
//
// A Bandit that implements this receives exactly one RecordEpoch call per
// reporting epoch and no RecordStats calls at all.
type EpochBandit interface {
	Bandit

	// RecordEpoch delivers every arm's measurements for one reporting epoch.
	// The report and its Stats slice are freshly allocated for each call and
	// are never reused by the cache, so an implementation may retain them.
	//
	// The same non-blocking rule applies as to the rest of Bandit: this runs
	// under the cache's write lock.
	RecordEpoch(report EpochReport)
}
