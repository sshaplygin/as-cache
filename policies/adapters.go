package policies

import (
	"fmt"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	ascache "github.com/sshaplygin/as-cache"
)

// NewLRU returns an LRU policy of the given size, backed by
// hashicorp/golang-lru/v2.
func NewLRU[K comparable, V any](size int) (ascache.Policy[K, V], error) {
	cache, err := lru.New[K, V](size)
	if err != nil {
		return nil, fmt.Errorf("build lru cache: %w", err)
	}

	return ascache.NewCache[K, V](cache, ascache.LRU, size), nil
}

// NewTwoQueue returns a 2Q policy of the given size, backed by
// hashicorp/golang-lru/v2.
//
// 2Q puts a small recent-access queue in front of a frequently-accessed queue,
// so a one-off scan passes through the recent queue without flushing the
// working set. That makes it a useful arm to hold alongside LRU, which a scan
// defeats completely.
// 2Q reports neither evictions nor removals and cannot be resized, so it is
// adapted rather than used directly.
func NewTwoQueue[K comparable, V any](size int) (ascache.Policy[K, V], error) {
	cache, err := Adapt[K, V](size, func(size int) (PartialCacher[K, V], error) {
		built, err := lru.New2Q[K, V](size)
		if err != nil {
			return nil, fmt.Errorf("build 2q cache: %w", err)
		}

		return built, nil
	})
	if err != nil {
		return nil, err
	}

	return ascache.NewCache[K, V](cache, ascache.TwoQueue, size), nil
}

// NewTTL returns a policy that evicts by expiry as well as by recency, backed
// by hashicorp/golang-lru/v2/expirable. Entries older than ttl are evicted
// regardless of use; a ttl of zero disables expiry, leaving plain LRU
// behaviour.
//
// Note that this policy's hit rate depends on wall-clock time, not only on the
// access pattern. As a shadow it is therefore measuring something the other
// arms are not, which is the point when the workload has genuinely stale data,
// and misleading when it does not.
func NewTTL[K comparable, V any](size int, ttl time.Duration) ascache.Policy[K, V] {
	return ascache.NewCache[K, V](NewTTLCache[K, V](size, ttl), ascache.TTL, size)
}

// NewRandomPolicy returns a random-eviction policy of the given size, ready to
// be used as a bandit arm.
func NewRandomPolicy[K comparable, V any](size int) ascache.Policy[K, V] {
	return ascache.NewCache[K, V](NewRandom[K, V](size), ascache.Random, size)
}
