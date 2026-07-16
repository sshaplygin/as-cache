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
	return &CacheWrapper[K, V]{
		Cacher: cache,
		policy: policy,
		size:   size,
	}
}

type CacheWrapper[K comparable, V any] struct {
	Cacher[K, V]
	size   int
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
	return c.size
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
