// Package tinylfu adapts the W-TinyLFU cache from maypok86/otter to
// ascache.Policy.
//
// It is a module of its own so that otter and its dependencies stay out of any
// build that does not use this arm, matching how the other adapter modules are
// arranged.
//
// W-TinyLFU is the arm worth carrying if the point of an adaptive cache is to
// be better than the state of the art rather than better than LRU. It gates
// admission on a frequency sketch, so a newly requested key must out-score the
// entry it would displace, which makes it strong on skewed workloads and
// resistant to scans. Including it as a candidate is what makes a comparison
// honest: if the bandit keeps choosing it, that is a real result, and if the
// bandit beats it by switching, that is a stronger one.
package tinylfu

import (
	"fmt"
	"math"

	"github.com/maypok86/otter/v2"

	ascache "github.com/sshaplygin/as-cache"
)

// otter expresses capacity as a uint64 while Cacher uses int. These two
// helpers make every conversion between them explicit and total, so no value
// can wrap: a capacity larger than an int can hold is clamped rather than
// silently becoming negative.
func capacityToInt(capacity uint64) int {
	if capacity > math.MaxInt {
		return math.MaxInt
	}

	return int(capacity)
}

func capacityToUint64(size int) uint64 {
	if size < 0 {
		return 0
	}

	return uint64(size)
}

// Cache adapts otter's W-TinyLFU cache to ascache.Cacher.
//
// otter is natively resizable, so unlike the 2Q and ARC adapters this one
// never rebuilds and never discards the frequency sketch the algorithm has
// built up.
//
// One contract difference is worth knowing: otter reports an approximate size,
// so Len is approximate too. See Len.
type Cache[K comparable, V any] struct {
	cache *otter.Cache[K, V]
}

// New returns a W-TinyLFU cache holding up to size entries.
func New[K comparable, V any](size int) (*Cache[K, V], error) {
	cache, err := otter.New(&otter.Options[K, V]{MaximumSize: size})
	if err != nil {
		return nil, fmt.Errorf("build w-tinylfu cache: %w", err)
	}

	return &Cache[K, V]{cache: cache}, nil
}

// Add stores a value, reporting whether storing it evicted another entry.
//
// otter reports the previous value rather than an eviction, so an eviction is
// inferred the same way the other adapters do it: storing a key the cache did
// not already hold, while the cache is full, must have displaced something.
//
// The inference is best-effort for a second reason here. W-TinyLFU may reject
// the incoming key outright when the frequency sketch says the resident entry
// is the more valuable one, in which case nothing was evicted and nothing was
// admitted. Callers use this result as a hint, and AdaptiveCache ignores it
// for shadow policies entirely.
func (c *Cache[K, V]) Add(key K, value V) bool {
	_, existed := c.cache.GetEntryQuietly(key)
	full := c.cache.EstimatedSize() >= capacityToInt(c.cache.GetMaximum())
	c.cache.Set(key, value)

	return !existed && full
}

// Get returns the value for key, if present, and records the access so the
// frequency sketch sees it.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	return c.cache.GetIfPresent(key)
}

// Peek returns the value for key without recording an access, leaving the
// frequency sketch untouched.
func (c *Cache[K, V]) Peek(key K) (V, bool) {
	entry, ok := c.cache.GetEntryQuietly(key)
	if !ok {
		var zero V

		return zero, false
	}

	return entry.Value, true
}

// Contains reports whether key is cached, without recording an access.
func (c *Cache[K, V]) Contains(key K) bool {
	_, ok := c.cache.GetEntryQuietly(key)

	return ok
}

// Remove deletes key, reporting whether it was present.
func (c *Cache[K, V]) Remove(key K) bool {
	_, invalidated := c.cache.Invalidate(key)

	return invalidated
}

// Purge empties the cache.
func (c *Cache[K, V]) Purge() {
	c.cache.InvalidateAll()
}

// Keys returns the cached keys. otter exposes them as an iterator; they are
// materialised here because Cacher hands callers a slice, and AdaptiveCache
// walks it while migrating data between policies.
//
// The order carries no eviction meaning. Migration only needs the set of live
// keys, but a policy whose Keys order does matter should not be adapted this
// way without checking.
func (c *Cache[K, V]) Keys() []K {
	keys := make([]K, 0, c.cache.EstimatedSize())
	for key := range c.cache.Keys() {
		keys = append(keys, key)
	}

	return keys
}

// Values returns the cached values, in the same arbitrary order as Keys.
func (c *Cache[K, V]) Values() []V {
	values := make([]V, 0, c.cache.EstimatedSize())
	for value := range c.cache.Values() {
		values = append(values, value)
	}

	return values
}

// Len returns the number of cached entries.
//
// otter reports an approximate size: it may differ from the true count while
// insertions or deletions are in flight, or while expired entries await
// removal. That is fine for every use AdaptiveCache makes of it except one -
// the capacity gate that holds off policy switching until the cache is full
// compares Len against Cap for exact equality, and an approximate Len can miss
// that equality. Set Settings.EvictPartialCapacityFilling to true when this
// policy is an arm, or accept that switching may start later than it would
// otherwise.
func (c *Cache[K, V]) Len() int {
	return c.cache.EstimatedSize()
}

// Cap returns the capacity.
func (c *Cache[K, V]) Cap() int {
	return capacityToInt(c.cache.GetMaximum())
}

// Resize changes the capacity to size and returns the number of entries
// evicted to reach it.
//
// otter resizes in place, so the frequency sketch survives - an adapted 2Q or
// ARC policy would have to be rebuilt and would lose everything it had
// learned. Eviction happens asynchronously, so the returned count is what had
// been evicted by the time this call finished and may understate the total.
func (c *Cache[K, V]) Resize(size int) int {
	if size < 0 {
		size = 0
	}

	before := c.cache.EstimatedSize()
	c.cache.SetMaximum(capacityToUint64(size))
	// Force pending maintenance so the eviction the new maximum implies has
	// happened before the count is taken.
	c.cache.CleanUp()

	evicted := before - c.cache.EstimatedSize()
	if evicted < 0 {
		return 0
	}

	return evicted
}

// NewPolicy returns a W-TinyLFU policy of the given size, ready to be used as
// a bandit arm.
func NewPolicy[K comparable, V any](size int) (ascache.Policy[K, V], error) {
	cache, err := New[K, V](size)
	if err != nil {
		return nil, err
	}

	return ascache.NewCache[K, V](cache, ascache.TinyLFU, size), nil
}

var _ ascache.Cacher[string, int] = (*Cache[string, int])(nil)
