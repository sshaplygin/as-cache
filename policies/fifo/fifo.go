// Package fifo adapts the FIFO-queue cache algorithms from
// scalalang2/golang-fifo to ascache.Policy.
//
// Two policies live here, because they come from one dependency and share one
// adapter:
//
//   - S3-FIFO uses three static FIFO queues. A small queue holding a tenth of
//     the cache filters keys that are requested only once, a ghost queue
//     remembers what the small queue evicted so a returning key is admitted on
//     its second request rather than its third, and the main queue evicts by
//     FIFO-reinsertion over a counter capped at three. Cite: Yang, Zhang, Qiu,
//     Yue & Rashmi, "FIFO Queues are All You Need for Cache Eviction", SOSP '23.
//   - SIEVE uses one FIFO queue and a hand that sweeps it from the oldest end,
//     evicting the first entry it reaches that has not been visited since the
//     hand last passed and clearing the visited bit of every entry it steps
//     over. No ghost queue, no counters, no second queue. Cite: Zhang, Yang,
//     Yue, Vigfusson & Rashmi, "SIEVE is Simpler than LRU", NSDI '24.
//
// Neither reorders anything on a cache hit, which is what makes both cheap and
// scalable, and both are deterministic - a property this repository's
// reproducible replays depend on and which W-TinyLFU cannot offer.
//
// The package is a module of its own so that golang-fifo stays out of any
// build that does not use these arms, matching how the other adapter modules
// are arranged.
//
// # What this adapter has to supply
//
// golang-fifo's Cache interface is Set/Get/Remove/Contains/Peek/Len/Purge/
// Close/SetOnEvicted - that last one being what the whole index mechanism here
// rests on. ascache.Cacher additionally needs Keys, Values, Resize and Cap, and
// needs Add to report whether it evicted. None of those exist upstream, so
// this adapter keeps its own index of the live keys, maintained through the
// library's eviction callback, and rebuilds the cache to resize it. Read
// Resize and Keys before relying on either: both cost something the other
// policy adapters do not.
package fifo

import (
	"sync"

	"github.com/scalalang2/golang-fifo/s3fifo"
	"github.com/scalalang2/golang-fifo/sieve"
	"github.com/scalalang2/golang-fifo/types"

	ascache "github.com/sshaplygin/as-cache"
)

// builder constructs an underlying cache of the given size. The size passed is
// always positive; see Cache.build.
type builder[K comparable, V any] func(size int) types.Cache[K, V]

// Cache adapts a golang-fifo cache to ascache.Cacher. NewS3FIFO and NewSieve
// choose which algorithm it wraps; everything else here is common to both.
//
// It is safe for concurrent use. Every method takes this type's mutex before
// touching either the index or the underlying cache, so the lock order is
// always this type's lock and then the library's, never the reverse - with one
// deliberate exception described on onEvicted.
type Cache[K comparable, V any] struct {
	mu sync.Mutex
	// newInner is kept because upstream cannot resize: Resize rebuilds through
	// it at the new capacity.
	newInner builder[K, V]
	inner    types.Cache[K, V]
	size     int

	// keys holds every key the underlying cache currently holds, and index
	// maps a key to its slot in keys. The library exposes no way to enumerate
	// its contents, and Cacher requires Keys and Values, so the adapter keeps
	// this second copy of the key set. Removal swaps the last key into the
	// vacated slot, which keeps both operations constant time at the cost of
	// any meaningful ordering.
	keys  []K
	index map[K]int

	// evictions counts the entries the library has reported evicting. Add
	// compares it either side of a write, which is how this adapter reports an
	// exact evicted flag rather than inferring one from the length.
	evictions uint64
}

// NewS3FIFO returns an S3-FIFO cache holding up to size entries. A size of zero
// or less means the cache holds nothing.
func NewS3FIFO[K comparable, V any](size int) *Cache[K, V] {
	return newCache(size, func(size int) types.Cache[K, V] {
		return s3fifo.New[K, V](size, 0)
	})
}

// NewSieve returns a SIEVE cache holding up to size entries. A size of zero or
// less means the cache holds nothing.
func NewSieve[K comparable, V any](size int) *Cache[K, V] {
	return newCache(size, func(size int) types.Cache[K, V] {
		return sieve.New[K, V](size, 0)
	})
}

// newCache builds an adapter around one of the library's constructors.
func newCache[K comparable, V any](size int, newInner builder[K, V]) *Cache[K, V] {
	if size < 0 {
		size = 0
	}

	c := &Cache[K, V]{newInner: newInner, size: size}
	c.resetIndexLocked(size)
	c.inner = c.build(size)

	return c
}

// build constructs an underlying cache of the given size with the eviction
// callback attached, or nil when the size is not positive.
//
// The TTL is always zero, and that is load-bearing rather than a default. A
// non-zero TTL makes golang-fifo start a background goroutine that expires
// entries on its own clock, which would call the eviction callback from a
// goroutine this adapter never entered - see onEvicted for why that is not
// safe here. A policy that expires by time already exists in the parent
// package as NewTTL.
func (c *Cache[K, V]) build(size int) types.Cache[K, V] {
	if size <= 0 {
		// Neither algorithm can be built at a non-positive size, and they fail
		// differently: SIEVE panics outright, while S3-FIFO's Set loops "while
		// len >= size, evict" and at size zero never returns. The adapter holds
		// no cache at all in that state and refuses writes in Add.
		return nil
	}

	cache := c.newInner(size)
	cache.SetOnEvicted(c.onEvicted)

	return cache
}

// onEvicted keeps the key index in step with the underlying cache.
//
// It deliberately does not take this type's mutex, and must not. golang-fifo
// invokes the callback from inside removeEntry while holding its own mutex,
// and every path that reaches removeEntry here is one this adapter entered
// with its own mutex already held: Add, Remove, Resize and - for SIEVE, whose
// Purge walks its entries through removeEntry - Purge. Taking the mutex again
// would deadlock on the first eviction, since a sync.Mutex is not reentrant.
//
// Purge and Resize detach the callback before they run rather than relying on
// this, but the enumeration has to be complete regardless: it is the whole
// safety argument for a non-reentrant mutex, and a fourth path nobody listed
// is how that argument stops being true.
//
// That reasoning holds only because the underlying cache is built with a TTL
// of zero and therefore has no background expiry goroutine. See build.
func (c *Cache[K, V]) onEvicted(key K, _ V, reason types.EvictReason) {
	c.untrackLocked(key)

	if reason == types.EvictReasonEvicted {
		c.evictions++
	}
}

// Add stores a value, reporting whether storing it evicted another entry.
//
// The eviction count is exact rather than inferred: the library reports each
// eviction through the callback, so this counts them instead of guessing from
// the length as the 2Q, ARC and W-TinyLFU adapters have to.
//
// One upstream behaviour is worth knowing, and both algorithms have it: a
// write over an existing key counts as an access. S3-FIFO raises the entry's
// frequency counter and SIEVE sets its visited bit. Neither paper counts a
// write that way, and this repository's shadow policies are driven with Add,
// so an arm on shadow duty looks more used here than it should.
func (c *Cache[K, V]) Add(key K, value V) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inner == nil {
		// A cache of zero capacity holds nothing, and nothing was evicted to
		// make room, because nothing was stored.
		return false
	}

	before := c.evictions
	c.trackLocked(key)
	c.inner.Set(key, value)

	return c.evictions > before
}

// Get returns the value for key, if present, and records the access so the
// entry's frequency counter rises.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inner == nil {
		var zero V

		return zero, false
	}

	return c.inner.Get(key)
}

// Peek returns the value for key without recording an access.
func (c *Cache[K, V]) Peek(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inner == nil {
		var zero V

		return zero, false
	}

	return c.inner.Peek(key)
}

// Contains reports whether key is cached, without recording an access. A key
// remembered only by the ghost queue is not cached and is not reported.
func (c *Cache[K, V]) Contains(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inner == nil {
		return false
	}

	return c.inner.Contains(key)
}

// Remove deletes key, reporting whether it was present.
func (c *Cache[K, V]) Remove(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inner == nil {
		return false
	}

	// The library's callback removes the key from the index for us.
	return c.inner.Remove(key)
}

// Purge empties the cache.
func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inner != nil {
		// The callback is detached first because the two algorithms disagree
		// about whether Purge runs it: S3-FIFO deletes its entries directly,
		// while SIEVE walks every entry through removeEntry, which does. The
		// index is rebuilt on the next line either way, so those callbacks can
		// only do redundant work - a map delete and a slice swap per entry,
		// measured at over ten times the cost of the purge itself on a
		// 20,000-entry cache, paid on every policy switch because migrateData
		// purges the incoming policy.
		c.inner.SetOnEvicted(nil)
		c.inner.Purge()
		c.inner.SetOnEvicted(c.onEvicted)
	}
	c.resetIndexLocked(c.size)
}

// Keys returns the cached keys.
//
// The order is this adapter's insertion order with removals filled by moving
// the last key into the vacated slot, because golang-fifo exposes no way to
// walk its queues. It is arbitrary, it is not an eviction order, and it is not
// stable across mutations - a Keys call and a later Values call may disagree
// if the cache changed in between, though each call on its own is consistent.
//
// AdaptiveCache uses this order in two places where a policy's own ordering
// would be preferable: warm migration copies entries in it, and demotion
// rewrites values in it. The arbitrary order is not what costs anything there
// - see the note on ascache's demoteLocked, where the cost is that the rewrite
// counts as an access at all, which for SIEVE sets the one bit its eviction
// decision is made on. Neither is incorrect with an arbitrary order, but
// neither gets the benefit a policy that can report its own recency gives.
func (c *Cache[K, V]) Keys() []K {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.snapshotLocked()
}

// Values returns the cached values, in the same order as the Keys returned by
// the same snapshot.
func (c *Cache[K, V]) Values() []V {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inner == nil {
		return nil
	}

	keys := c.snapshotLocked()
	values := make([]V, 0, len(keys))
	for _, key := range keys {
		value, ok := c.inner.Peek(key)
		if !ok {
			continue
		}
		values = append(values, value)
	}

	return values
}

// Len returns the number of cached entries.
//
// It is answered from this adapter's index rather than by asking the library.
// golang-fifo's S3-FIFO reads its two queues in Len without taking its mutex,
// so calling that concurrently with a write is a data race the detector will
// report. Its SIEVE does lock; answering both the same way here keeps the
// hazard out of reach rather than depending on which algorithm is wrapped.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.keys)
}

// Cap returns the capacity.
func (c *Cache[K, V]) Cap() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.size
}

// Resize changes the capacity to size and returns the number of entries
// evicted to reach it.
//
// golang-fifo cannot be resized, so this rebuilds it at the new size and
// replays the entries that fit. **The rebuilt cache starts with an empty ghost
// queue and every frequency counter back at zero**, which is most of what
// S3-FIFO knows: an entry that had earned the main queue has to earn it again,
// and a key evicted just before the resize no longer gets its second chance.
//
// That matters more here than for the 2Q and ARC adapters, which pay the same
// cost, because AdaptiveCache resizes a policy every time it is promoted or
// demoted. A policy that changes role often is a policy that is permanently
// re-learning, and it will under-report its own hit rate while it does.
//
// A size of zero or less empties the cache and holds nothing.
func (c *Cache[K, V]) Resize(size int) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if size < 0 {
		size = 0
	}
	if size == c.size {
		return 0
	}

	before := len(c.keys)

	type entry struct {
		key   K
		value V
	}
	kept := make([]entry, 0, before)
	if c.inner != nil {
		for _, key := range c.snapshotLocked() {
			value, ok := c.inner.Peek(key)
			if !ok {
				continue
			}
			kept = append(kept, entry{key: key, value: value})
		}
		// Detached for the same reason as in Purge: SIEVE's Close purges, and
		// purging runs the callback per entry against an index this function
		// is about to replace wholesale.
		c.inner.SetOnEvicted(nil)
		c.inner.Close()
	}

	c.size = size
	c.resetIndexLocked(size)
	c.inner = c.build(size)

	if c.inner != nil {
		for _, e := range kept {
			// Tracked before the write, so the callback can find and drop any
			// key the rebuilt cache evicts to make room for a later one.
			c.trackLocked(e.key)
			c.inner.Set(e.key, e.value)
		}
	}

	return before - len(c.keys)
}

// NewS3FIFOPolicy returns an S3-FIFO policy of the given size, ready to be used
// as a bandit arm.
func NewS3FIFOPolicy[K comparable, V any](size int) (ascache.Policy[K, V], error) {
	return ascache.NewCache[K, V](NewS3FIFO[K, V](size), ascache.S3FIFO, size), nil
}

// NewSievePolicy returns a SIEVE policy of the given size, ready to be used as
// a bandit arm.
//
// It is worth carrying alongside S3-FIFO rather than instead of it. Both filter
// keys that are requested once, but on different evidence - a first-in
// first-out probation period against a sweeping hand - and SIEVE keeps no ghost
// queue at all, so it costs less and sees less.
func NewSievePolicy[K comparable, V any](size int) (ascache.Policy[K, V], error) {
	return ascache.NewCache[K, V](NewSieve[K, V](size), ascache.SIEVE, size), nil
}

var _ ascache.Cacher[string, int] = (*Cache[string, int])(nil)
