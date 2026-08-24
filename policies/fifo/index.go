package fifo

// The key index. golang-fifo exposes no way to enumerate what it holds, and
// ascache.Cacher requires Keys and Values, so this adapter keeps its own copy
// of the key set alongside the library's.
//
// That is a real cost, not an implementation detail: the key set is stored
// twice, once by the library and once here. Values are not - the index holds
// keys only, and Values reads them back through Peek - so the duplication is
// bounded by the key set rather than by the data. It is still the largest
// single difference between this adapter and a policy that can report its own
// contents.
//
// Every function here is named *Locked and must be called with the Cache's
// mutex held, with one deliberate exception: untrackLocked is also reached
// from the library's eviction callback, which runs inside a library call this
// adapter made with its mutex already held. See Cache.onEvicted.

// resetIndexLocked empties the index, sizing it for a cache of the given
// capacity.
func (c *Cache[K, V]) resetIndexLocked(capacity int) {
	if capacity < 0 {
		capacity = 0
	}

	// The reservation is capped rather than taken at face value. Both
	// containers grow on demand, so reserving is an optimisation - but a
	// capacity near the top of the int range asks make for an allocation the
	// runtime refuses, and a panic from Resize would leave AdaptiveCache
	// half-way through resizing its arms, some at the new size and some not.
	// A cache that large cannot be filled anyway.
	if capacity > maxIndexReservation {
		capacity = maxIndexReservation
	}

	// Fresh containers rather than truncation: truncating keeps the backing
	// array and every key in it reachable, so a Purge meant to release memory
	// would pin the whole key set until an equal number of writes overwrote
	// the slots.
	c.keys = make([]K, 0, capacity)
	c.index = make(map[K]int, capacity)
}

// maxIndexReservation bounds how much the key index reserves up front. It is
// far above any cache anyone will build and far below the point where make
// refuses the request.
const maxIndexReservation = 1 << 22

// trackLocked records a key as present. Re-tracking a key already in the index
// is a no-op, which is what a write over a live key needs.
func (c *Cache[K, V]) trackLocked(key K) {
	if _, ok := c.index[key]; ok {
		return
	}

	c.index[key] = len(c.keys)
	c.keys = append(c.keys, key)
}

// untrackLocked records a key as gone, in constant time, by moving the last
// key into the vacated slot.
func (c *Cache[K, V]) untrackLocked(key K) {
	slot, ok := c.index[key]
	if !ok {
		return
	}

	last := len(c.keys) - 1
	if slot != last {
		moved := c.keys[last]
		c.keys[slot] = moved
		c.index[moved] = slot
	}

	var zero K
	c.keys[last] = zero
	c.keys = c.keys[:last]
	delete(c.index, key)
}

// snapshotLocked returns a copy of the tracked keys, so a caller iterating the
// result cannot be tripped by a later mutation and cannot mutate the index by
// writing to the slice it was handed.
func (c *Cache[K, V]) snapshotLocked() []K {
	keys := make([]K, len(c.keys))
	copy(keys, c.keys)

	return keys
}
