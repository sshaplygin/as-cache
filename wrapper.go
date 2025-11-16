package ascache

import (
	"strings"
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
		stats:  PolicyStats{},
	}
}

type CacheWrapper[K comparable, V any] struct {
	Cacher[K, V]
	size   int
	policy PolicyType
	stats  PolicyStats
}

func (c *CacheWrapper[K, V]) Get(key K) (value V, ok bool) {
	value, ok = c.Cacher.Get(key)
	if ok {
		c.stats.Hits++
	} else {
		c.stats.Misses++
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
	return c.stats
}

func (c *CacheWrapper[K, V]) ResetStats() {
	c.stats = PolicyStats{}
}
