package policies

import (
	"fmt"
	"sync"
)

// PartialCacher is the shape a cache library commonly ships: lookups and
// mutations, but no eviction or presence reporting and no way to change
// capacity after construction. Both hashicorp's 2Q and ARC caches have exactly
// this shape.
type PartialCacher[K comparable, V any] interface {
	Add(key K, value V)
	Get(key K) (value V, ok bool)
	Peek(key K) (value V, ok bool)
	Contains(key K) bool
	Remove(key K)
	Purge()
	Keys() []K
	Values() []V
	Len() int
}

// AdaptedCache turns a PartialCacher into a full ascache.Cacher.
//
// It supplies the three things the underlying cache does not: Add reports
// whether it evicted, Remove reports whether the key was present, and Resize
// changes capacity by rebuilding the cache at the new size and replaying the
// entries that fit.
//
// The underlying cache is held behind this type's mutex rather than embedded,
// because Resize replaces it wholesale; an operation that landed on the
// outgoing instance would simply be lost.
type AdaptedCache[K comparable, V any] struct {
	mu    sync.RWMutex
	cache PartialCacher[K, V]
	size  int
	build func(size int) (PartialCacher[K, V], error)
}

// Adapt wraps a cache built by build, which must return a cache of the
// capacity it is given. build is called again on every Resize.
func Adapt[K comparable, V any](
	size int,
	build func(size int) (PartialCacher[K, V], error),
) (*AdaptedCache[K, V], error) {
	if build == nil {
		return nil, fmt.Errorf("adapt cache: build function must not be nil")
	}

	cache, err := build(size)
	if err != nil {
		return nil, fmt.Errorf("adapt cache: %w", err)
	}

	return &AdaptedCache[K, V]{cache: cache, size: size, build: build}, nil
}

// Add stores a value, reporting whether storing it evicted another entry.
//
// The underlying cache does not report evictions, so this infers one: an
// insert of a key the cache did not already hold, made while the cache is
// full, must have evicted something to make room.
func (c *AdaptedCache[K, V]) Add(key K, value V) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.size <= 0 {
		// A cache of zero capacity holds nothing. The underlying cache cannot
		// be rebuilt at size zero - both 2Q and ARC reject a non-positive size
		// - so the entry is refused here instead. Nothing was evicted to make
		// room, because nothing was stored.
		return false
	}

	evicts := !c.cache.Contains(key) && c.cache.Len() >= c.size
	c.cache.Add(key, value)
	c.enforceCapacityLocked()

	return evicts
}

// enforceCapacityLocked trims the cache to the configured size.
//
// Normally the underlying cache enforces its own capacity and this does
// nothing. It matters after a Resize whose rebuild failed: the old instance is
// still in use at its original capacity while the configured size is smaller,
// and without this the cache would hold far more than the caller asked for
// while Cap reported the smaller number.
func (c *AdaptedCache[K, V]) enforceCapacityLocked() {
	for c.cache.Len() > c.size {
		keys := c.cache.Keys()
		if len(keys) == 0 {
			return
		}
		c.cache.Remove(keys[0])
	}
}

// Get returns the value for key, if present, and records the access.
func (c *AdaptedCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cache.Get(key)
}

// Peek returns the value for key without recording an access.
func (c *AdaptedCache[K, V]) Peek(key K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.cache.Peek(key)
}

// Contains reports whether key is cached, without recording an access.
func (c *AdaptedCache[K, V]) Contains(key K) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.cache.Contains(key)
}

// Remove deletes key, reporting whether it was present.
func (c *AdaptedCache[K, V]) Remove(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	present := c.cache.Contains(key)
	c.cache.Remove(key)

	return present
}

// Purge empties the cache.
func (c *AdaptedCache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache.Purge()
}

// Keys returns the cached keys, oldest first.
func (c *AdaptedCache[K, V]) Keys() []K {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.cache.Keys()
}

// Values returns the cached values, in the same order as Keys.
func (c *AdaptedCache[K, V]) Values() []V {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.cache.Values()
}

// Len returns the number of cached entries.
func (c *AdaptedCache[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.cache.Len()
}

// Cap returns the capacity.
func (c *AdaptedCache[K, V]) Cap() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.size
}

// Resize changes the capacity to size and returns the number of entries
// evicted to reach it.
//
// The underlying cache cannot be resized, so this rebuilds it at the new size
// and replays every entry, letting the rebuilt cache evict down to capacity.
// Whatever internal adaptation the algorithm had accumulated - 2Q's queue
// split, ARC's recency/frequency balance and ghost lists - is lost, and the
// rebuilt cache has to relearn it. Until it has, the policy's measured hit
// rate understates the algorithm, so resizing such a policy repeatedly would
// keep it permanently unadapted. Which entries survive a shrink is not
// meaningful either; see the note in the body.
//
// A size of zero or less empties the cache and holds nothing.
func (c *AdaptedCache[K, V]) Resize(size int) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if size < 0 {
		size = 0
	}
	if size == c.size {
		return 0
	}

	before := c.cache.Len()

	if size == 0 {
		c.cache.Purge()
		c.size = 0

		return before
	}

	// Every entry is replayed and the rebuilt cache evicts down to the new
	// capacity itself, rather than this code selecting survivors up front.
	//
	// There is no correct selection to make. Keys() carries no consistent
	// meaning across the caches this adapter serves: 2Q returns its frequent
	// queue followed by its recent one, ARC returns its recent list followed
	// by its frequent one - opposite groupings, and neither is one global
	// recency order. Any "keep the tail" or "keep the head" rule is therefore
	// right for one and precisely backwards for the other.
	//
	// So which entries survive a shrink is not meaningful, and callers should
	// not rely on it. That is part of the same cost as losing the algorithm's
	// learned state, and a good reason to prefer a natively resizable policy
	// as an arm where one exists.
	keys := c.cache.Keys()

	type entry struct {
		key   K
		value V
	}
	kept := make([]entry, 0, len(keys))
	for _, key := range keys {
		value, ok := c.cache.Peek(key)
		if !ok {
			continue
		}
		kept = append(kept, entry{key: key, value: value})
	}

	rebuilt, err := c.build(size)
	if err != nil {
		// No cache of the new size could be built - hashicorp's 2Q, for one,
		// rejects a size of 1 because its ghost queues round down to zero.
		// Keep the existing instance but enforce the requested capacity on it
		// anyway, so Cap and behaviour still agree. Reporting the new size
		// while silently retaining the old capacity would let the cache hold
		// far more than the caller asked for.
		c.size = size
		before := c.cache.Len()
		c.enforceCapacityLocked()

		return before - c.cache.Len()
	}

	for _, e := range kept {
		rebuilt.Add(e.key, e.value)
	}

	c.cache = rebuilt
	c.size = size

	return before - rebuilt.Len()
}
