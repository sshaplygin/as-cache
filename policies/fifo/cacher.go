package fifo

// The ascache.Cacher surface. The constructors, the eviction callback and the
// rebuild that Resize leans on live in fifo.go; this file is the methods a
// caller actually reaches for.

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

// Values returns the cached values, in the same order as Keys.
//
// A key the index holds but the library does not would break that
// correspondence. No test has ever produced one - upstream funnels every
// removal through its eviction callback, and the two paths that skip it
// (S3-FIFO's Purge, and Close via Purge) are reached only from this adapter's
// own Purge and Resize, which rebuild the index immediately afterwards - so
// this is defence against a state that should not exist rather than a fix for
// one that does.
//
// If it ever does occur, the stale key is dropped from the index so that Keys
// and Values agree again from the next call onward. Be clear about the limit:
// it does not repair the call it fires on, which still returns a slice one
// short of the Keys that preceded it.
//
// The alternative of padding the gap with a zero value is worse and is not on
// offer. That is the shape of the defect this repository documents against
// expirable.LRU, whose Values fills a full-length slice while skipping expired
// entries and so returns trailing zeros that no longer line up with Keys - and
// a caller cannot tell a padded zero from stored data.
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
			c.untrackLocked(key)

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
