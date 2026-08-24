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
	"fmt"
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

// NewS3FIFO returns an S3-FIFO cache holding up to size entries.
//
// A size of zero or less is an error, which is what NewLRU, NewLFU and
// NewTwoQueue all do. A cache built at zero would accept nothing and report no
// hits for as long as it existed, and as a bandit arm that is a silent no-op
// rather than a policy - the constructor is the last place it can be caught.
//
// Resizing an existing cache to zero remains legal, and is a separate case:
// AdaptiveCache.Resize passes its own new capacity through to every policy, so
// a caller resizing the whole cache to zero resizes each arm to zero. (A
// shadow policy's miniature capacity never reaches zero on its own - both
// scaledCapacity and shadowCapacity floor it at one.) See Resize.
func NewS3FIFO[K comparable, V any](size int) (*Cache[K, V], error) {
	return newCache(size, "s3-fifo", func(size int) types.Cache[K, V] {
		return s3fifo.New[K, V](size, 0)
	})
}

// NewSieve returns a SIEVE cache holding up to size entries.
//
// A size of zero or less is an error, for the reasons given on NewS3FIFO.
func NewSieve[K comparable, V any](size int) (*Cache[K, V], error) {
	return newCache(size, "sieve", func(size int) types.Cache[K, V] {
		return sieve.New[K, V](size, 0)
	})
}

// newCache builds an adapter around one of the library's constructors.
func newCache[K comparable, V any](
	size int,
	name string,
	newInner builder[K, V],
) (*Cache[K, V], error) {
	if size <= 0 {
		return nil, fmt.Errorf("build %s cache: must provide a positive size", name)
	}

	c := &Cache[K, V]{newInner: newInner, size: size}
	c.resetIndexLocked(size)
	c.inner = c.build(size)

	return c, nil
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

// NewS3FIFOPolicy returns an S3-FIFO policy of the given size, ready to be used
// as a bandit arm.
func NewS3FIFOPolicy[K comparable, V any](size int) (ascache.Policy[K, V], error) {
	cache, err := NewS3FIFO[K, V](size)
	if err != nil {
		return nil, err
	}

	return ascache.NewCache[K, V](cache, ascache.S3FIFO, size), nil
}

// NewSievePolicy returns a SIEVE policy of the given size, ready to be used as
// a bandit arm.
//
// It is worth carrying alongside S3-FIFO rather than instead of it. Both filter
// keys that are requested once, but on different evidence - a first-in
// first-out probation period against a sweeping hand - and SIEVE keeps no ghost
// queue at all, so it costs less and sees less.
func NewSievePolicy[K comparable, V any](size int) (ascache.Policy[K, V], error) {
	cache, err := NewSieve[K, V](size)
	if err != nil {
		return nil, err
	}

	return ascache.NewCache[K, V](cache, ascache.SIEVE, size), nil
}

var _ ascache.Cacher[string, int] = (*Cache[string, int])(nil)
