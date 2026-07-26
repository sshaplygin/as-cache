package policies

import (
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// ttlEntry pairs a value with the moment it stops being servable. Keeping the
// deadline alongside the value is what makes expiry exact: the entry and its
// deadline are evicted together, so there is no bookkeeping to fall out of
// step with the cache.
type ttlEntry[V any] struct {
	value     V
	expiresAt time.Time
}

// TTLCache is an LRU cache whose entries also expire after a fixed duration.
//
// It is built on hashicorp's plain LRU with expiry handled here rather than on
// hashicorp's expirable LRU, for three reasons, each of which was a real
// defect when this was tried the other way round:
//
//   - expirable.LRU's Get and Peek return (zeroValue, true) for an entry that
//     has expired but not yet been reaped, because the expiry branch takes a
//     bare return over an already-true named result. A cache that hands a
//     caller a zero value and calls it a hit breaks the one invariant this
//     library is built around.
//   - expirable.LRU's Values allocates a full-length slice and skips expired
//     entries while filling it, so it returns trailing zero values that do not
//     line up with Keys.
//   - expirable.NewLRU starts a reaper goroutine per cache and offers no way
//     to stop it, so every policy ever constructed leaks that goroutine and
//     everything the cache retains, for the life of the process.
//
// Expiry here is lazy: an expired entry occupies its slot until it is read,
// overwritten, or evicted by LRU pressure. That trades a little memory for
// exactness and for not owning a goroutine, which is the right trade for a
// policy that may exist only to be measured as a shadow.
//
// It also treats capacity the way the rest of the Cacher implementations do.
// For expirable.LRU a size of zero means *unlimited* - documented as turning
// the LRU mechanism off - whereas every other policy treats zero as holding
// nothing. Since AdaptiveCache resizes shadow policies automatically, a resize
// that reached zero would quietly convert a bounded shadow into an unbounded
// one. Here zero means empty, like everywhere else.
type TTLCache[K comparable, V any] struct {
	mu    sync.RWMutex
	cache *lru.Cache[K, ttlEntry[V]]
	size  int
	ttl   time.Duration
	// now is time.Now except in tests, which need to move the clock.
	now func() time.Time
}

// NewTTLCache returns a cache holding up to size entries, treating entries
// older than ttl as absent. A ttl of zero or less disables expiry, leaving
// plain LRU behaviour. A size of zero or less holds nothing.
func NewTTLCache[K comparable, V any](size int, ttl time.Duration) *TTLCache[K, V] {
	if size < 0 {
		size = 0
	}

	// lru.New rejects a non-positive size, and a zero-capacity cache is
	// represented by size, not by the underlying cache's capacity.
	cache, err := lru.New[K, ttlEntry[V]](max(size, 1))
	if err != nil {
		// Unreachable: the size passed is at least 1.
		panic("policies: building ttl cache: " + err.Error())
	}

	return &TTLCache[K, V]{
		cache: cache,
		size:  size,
		ttl:   ttl,
		now:   time.Now,
	}
}

// expired reports whether an entry is past its deadline.
func (c *TTLCache[K, V]) expired(e ttlEntry[V]) bool {
	return !e.expiresAt.IsZero() && c.now().After(e.expiresAt)
}

// deadline returns the expiry instant for an entry stored now, or the zero
// time when expiry is disabled.
func (c *TTLCache[K, V]) deadline() time.Time {
	if c.ttl <= 0 {
		return time.Time{}
	}

	return c.now().Add(c.ttl)
}

// Add stores a value, reporting whether storing it evicted another entry.
func (c *TTLCache[K, V]) Add(key K, value V) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.size <= 0 {
		return false
	}

	return c.cache.Add(key, ttlEntry[V]{value: value, expiresAt: c.deadline()})
}

// Get returns the value for key if it is present and unexpired, recording the
// access. An expired entry is reported as a miss and dropped.
func (c *TTLCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.cache.Get(key)
	if !ok {
		var zero V

		return zero, false
	}

	if c.expired(entry) {
		c.cache.Remove(key)
		var zero V

		return zero, false
	}

	return entry.value, true
}

// Peek returns the value for key if it is present and unexpired, without
// recording an access or dropping the expired entry.
func (c *TTLCache[K, V]) Peek(key K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.cache.Peek(key)
	if !ok || c.expired(entry) {
		var zero V

		return zero, false
	}

	return entry.value, true
}

// Contains reports whether key is cached and unexpired, without recording an
// access.
func (c *TTLCache[K, V]) Contains(key K) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.cache.Peek(key)

	return ok && !c.expired(entry)
}

// Remove deletes key, reporting whether it was present. An entry that has
// expired but not yet been reclaimed is reported as absent.
func (c *TTLCache[K, V]) Remove(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.cache.Peek(key)
	c.cache.Remove(key)

	return ok && !c.expired(entry)
}

// Purge empties the cache.
func (c *TTLCache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache.Purge()
}

// Keys returns the unexpired cached keys, oldest first.
func (c *TTLCache[K, V]) Keys() []K {
	c.mu.RLock()
	defer c.mu.RUnlock()

	all := c.cache.Keys()
	keys := make([]K, 0, len(all))
	for _, key := range all {
		if entry, ok := c.cache.Peek(key); ok && !c.expired(entry) {
			keys = append(keys, key)
		}
	}

	return keys
}

// Values returns the unexpired cached values, in the same order as Keys.
func (c *TTLCache[K, V]) Values() []V {
	c.mu.RLock()
	defer c.mu.RUnlock()

	all := c.cache.Keys()
	values := make([]V, 0, len(all))
	for _, key := range all {
		if entry, ok := c.cache.Peek(key); ok && !c.expired(entry) {
			values = append(values, entry.value)
		}
	}

	return values
}

// Len returns the number of unexpired cached entries.
func (c *TTLCache[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.ttl <= 0 {
		return c.cache.Len()
	}

	live := 0
	for _, key := range c.cache.Keys() {
		if entry, ok := c.cache.Peek(key); ok && !c.expired(entry) {
			live++
		}
	}

	return live
}

// Cap returns the capacity.
func (c *TTLCache[K, V]) Cap() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.size
}

// Resize changes the capacity to size and returns the number of entries
// evicted to reach it. A size of zero or less empties the cache and holds
// nothing.
func (c *TTLCache[K, V]) Resize(size int) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if size < 0 {
		size = 0
	}
	c.size = size

	if size == 0 {
		evicted := c.cache.Len()
		c.cache.Purge()

		return evicted
	}

	return c.cache.Resize(size)
}
