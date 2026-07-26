package ascache

import (
	"strings"
	"sync/atomic"
)

func NewCache[K comparable, V any](
	cache Cacher[K, V],
	policy PolicyType,
	size int,
) *CacheWrapper[K, V] {
	w := &CacheWrapper[K, V]{
		Cacher: cache,
		policy: policy,
	}
	w.size.Store(int64(size))

	return w
}

type CacheWrapper[K comparable, V any] struct {
	Cacher[K, V]
	// size tracks the wrapped cache's capacity. Resize updates it, and Cap
	// may be read concurrently with a resize, so it is atomic.
	size   atomic.Int64
	policy PolicyType
	// hits and misses are updated from Get, which callers may invoke
	// concurrently (AdaptiveCache.Get holds only a read lock), so they must be
	// mutated atomically.
	hits   atomic.Int64
	misses atomic.Int64
}

func (c *CacheWrapper[K, V]) Get(key K) (value V, ok bool) {
	value, ok = c.Cacher.Get(key)
	if ok {
		c.hits.Add(1)
	} else {
		c.misses.Add(1)
	}
	return
}

func (c *CacheWrapper[K, V]) Cap() int {
	return int(c.size.Load())
}

// Resize changes the wrapped cache's capacity and keeps Cap in step with it.
// The embedded Cacher's Resize would otherwise be promoted directly, leaving
// Cap reporting the capacity the wrapper was built with forever.
func (c *CacheWrapper[K, V]) Resize(size int) int {
	evicted := c.Cacher.Resize(size)
	c.size.Store(int64(size))

	return evicted
}

func (c *CacheWrapper[K, V]) Name() string {
	return strings.ToLower(c.policy.String())
}

func (c *CacheWrapper[K, V]) GetType() PolicyType {
	return c.policy
}

func (c *CacheWrapper[K, V]) GetStats() PolicyStats {
	return PolicyStats{
		Hits:   c.hits.Load(),
		Misses: c.misses.Load(),
	}
}

func (c *CacheWrapper[K, V]) ResetStats() {
	c.hits.Store(0)
	c.misses.Store(0)
}
