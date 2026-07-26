// Package policies provides ready-made Policy implementations for
// AdaptiveCache, adapting well-known cache libraries to the ascache.Cacher
// interface so they can be used as bandit arms without writing glue.
//
// The ARC policy is deliberately absent: the algorithm is patented by IBM and
// its adapter lives in the separate github.com/sshaplygin/as-cache/policies/arc
// module, so importing this package never pulls a patented implementation into
// a build.
package policies

import (
	"math/rand/v2"
	"sync"

	ascache "github.com/sshaplygin/as-cache"
)

// RandomCache evicts an arbitrary entry when it is full.
//
// Random eviction is worth having as a bandit arm precisely because it is the
// null hypothesis: it carries no bookkeeping and makes no assumption about the
// workload, so a policy that cannot beat it is not paying for itself. On a
// workload with no reuse structure - a uniform random or a pure scan - it also
// happens to be competitive with far more elaborate policies.
//
// It is safe for concurrent use.
type RandomCache[K comparable, V any] struct {
	mu   sync.Mutex
	data map[K]V
	// keys holds every key currently in data, and index maps a key to its slot
	// in keys. Together they make "pick a uniformly random key" and "remove a
	// key" both O(1): removal swaps the last key into the freed slot.
	keys  []K
	index map[K]int
	size  int
	rng   *rand.Rand
}

// NewRandom returns a random-eviction cache holding up to size entries.
// A size of zero or less means the cache holds nothing.
func NewRandom[K comparable, V any](size int) *RandomCache[K, V] {
	if size < 0 {
		size = 0
	}

	return &RandomCache[K, V]{
		data:  make(map[K]V, size),
		keys:  make([]K, 0, size),
		index: make(map[K]int, size),
		size:  size,
		//nolint:gosec // Eviction choice is not a security decision; a cheap
		// non-cryptographic source is the right one here.
		rng: rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())),
	}
}

// trackLocked records a newly inserted key.
func (c *RandomCache[K, V]) trackLocked(key K) {
	c.index[key] = len(c.keys)
	c.keys = append(c.keys, key)
}

// untrackLocked removes a key from the tracking structures in O(1) by swapping
// the last key into the vacated slot.
func (c *RandomCache[K, V]) untrackLocked(key K) {
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

	c.keys = c.keys[:last]
	delete(c.index, key)
}

// removeLocked deletes a key and its tracking entry.
func (c *RandomCache[K, V]) removeLocked(key K) bool {
	if _, ok := c.data[key]; !ok {
		return false
	}

	delete(c.data, key)
	c.untrackLocked(key)

	return true
}

// evictLocked drops random entries until the cache is within capacity,
// returning how many it removed.
func (c *RandomCache[K, V]) evictLocked() int {
	evicted := 0
	for len(c.keys) > c.size {
		victim := c.keys[c.rng.IntN(len(c.keys))]
		c.removeLocked(victim)
		evicted++
	}

	return evicted
}

// Add stores a value, reporting whether storing it evicted another entry.
//
// When the cache is full, room is made before the new entry is inserted, so
// the victim is drawn from the entries that were already resident. Inserting
// first and then evicting would put the caller's own write into the draw and
// discard it with probability 1/(size+1) - accepting a value and losing it
// before the next read, which no other policy here does.
func (c *RandomCache[K, V]) Add(key K, value V) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.data[key]; exists {
		c.data[key] = value

		return false
	}

	if c.size <= 0 {
		// A cache of zero capacity holds nothing, and nothing was evicted to
		// make room, because nothing was stored.
		return false
	}

	evicted := 0
	for len(c.keys) >= c.size {
		victim := c.keys[c.rng.IntN(len(c.keys))]
		c.removeLocked(victim)
		evicted++
	}

	c.trackLocked(key)
	c.data[key] = value

	return evicted > 0
}

// Get returns the value for key, if present.
func (c *RandomCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	value, ok := c.data[key]

	return value, ok
}

// Peek returns the value for key without affecting eviction order. For random
// eviction there is no order to affect, so it is identical to Get.
func (c *RandomCache[K, V]) Peek(key K) (V, bool) {
	return c.Get(key)
}

// Contains reports whether key is cached.
func (c *RandomCache[K, V]) Contains(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, ok := c.data[key]

	return ok
}

// Remove deletes key, reporting whether it was present.
func (c *RandomCache[K, V]) Remove(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.removeLocked(key)
}

// Purge empties the cache.
func (c *RandomCache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data = make(map[K]V, c.size)
	// A new slice rather than a truncation: truncating keeps the backing array
	// and every key in it reachable, so a Purge meant to release memory would
	// pin the whole keyspace until an equal number of Adds overwrote the slots.
	c.keys = make([]K, 0, c.size)
	c.index = make(map[K]int, c.size)
}

// Keys returns the cached keys. The order is arbitrary and not an eviction
// order: this cache has none.
func (c *RandomCache[K, V]) Keys() []K {
	c.mu.Lock()
	defer c.mu.Unlock()

	keys := make([]K, len(c.keys))
	copy(keys, c.keys)

	return keys
}

// Values returns the cached values, in the same arbitrary order as Keys.
func (c *RandomCache[K, V]) Values() []V {
	c.mu.Lock()
	defer c.mu.Unlock()

	values := make([]V, 0, len(c.keys))
	for _, key := range c.keys {
		values = append(values, c.data[key])
	}

	return values
}

// Len returns the number of cached entries.
func (c *RandomCache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.data)
}

// Resize changes the capacity, evicting at random down to the new size, and
// returns how many entries it evicted.
func (c *RandomCache[K, V]) Resize(size int) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if size < 0 {
		size = 0
	}
	c.size = size

	return c.evictLocked()
}

// Cap returns the capacity.
func (c *RandomCache[K, V]) Cap() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.size
}

var _ ascache.Cacher[string, int] = (*RandomCache[string, int])(nil)
