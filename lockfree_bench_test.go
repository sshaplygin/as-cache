package ascache

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// freePolicy is a read-contention-free policy: its map is populated before the
// benchmark and never written during it, and its counters are atomic. Reads
// therefore take no lock at all.
//
// It exists to answer one question: of the cost of a parallel Get through an
// AdaptiveCache, how much is the cache's own RWMutex and how much is the
// underlying policy's lock? Measuring against a policy that contributes zero
// read contention isolates the cache's share, which is the only part a
// lock-free read path could remove.
type freePolicy[K comparable, V any] struct {
	data   map[K]V
	cap    int
	policy PolicyType
	hits   atomic.Int64
	misses atomic.Int64
}

func newFreePolicy[K comparable, V any](policy PolicyType, capacity int) *freePolicy[K, V] {
	return &freePolicy[K, V]{data: make(map[K]V, capacity), cap: capacity, policy: policy}
}

// prefill populates the map before any concurrent reader exists.
func (p *freePolicy[K, V]) prefill(key K, value V) { p.data[key] = value }

func (p *freePolicy[K, V]) Get(key K) (V, bool) {
	v, ok := p.data[key]
	if ok {
		p.hits.Add(1)
	} else {
		p.misses.Add(1)
	}

	return v, ok
}

// Add is a no-op: the benchmark never writes, and a real write would race with
// the lock-free reads this type exists to model.
func (p *freePolicy[K, V]) Add(_ K, _ V) bool { return false }

func (p *freePolicy[K, V]) Peek(key K) (V, bool) { v, ok := p.data[key]; return v, ok }
func (p *freePolicy[K, V]) Contains(key K) bool  { _, ok := p.data[key]; return ok }
func (p *freePolicy[K, V]) Remove(_ K) bool      { return false }
func (p *freePolicy[K, V]) Purge()               {}
func (p *freePolicy[K, V]) Len() int             { return len(p.data) }
func (p *freePolicy[K, V]) Cap() int             { return p.cap }
func (p *freePolicy[K, V]) Resize(int) int       { return 0 }
func (p *freePolicy[K, V]) GetType() PolicyType  { return p.policy }

func (p *freePolicy[K, V]) Keys() []K {
	keys := make([]K, 0, len(p.data))
	for k := range p.data {
		keys = append(keys, k)
	}

	return keys
}

func (p *freePolicy[K, V]) Values() []V {
	vals := make([]V, 0, len(p.data))
	for _, v := range p.data {
		vals = append(vals, v)
	}

	return vals
}

func (p *freePolicy[K, V]) GetStats() PolicyStats {
	return PolicyStats{Hits: p.hits.Load(), Misses: p.misses.Load()}
}

func (p *freePolicy[K, V]) ResetStats() { p.hits.Store(0); p.misses.Store(0) }

const freeKeys = 1000

func freeKeySet() []string {
	keys := make([]string, freeKeys)
	for i := range keys {
		keys[i] = "k" + strconv.Itoa(i)
	}

	return keys
}

// BenchmarkLockFloor_PolicyDirect is the floor: reading the policy with no
// adaptive layer in the way at all. Everything above this is what the cache
// costs.
func BenchmarkLockFloor_PolicyDirect(b *testing.B) {
	keys := freeKeySet()
	p := newFreePolicy[string, int](LRU, freeKeys*2)
	for i, k := range keys {
		p.prefill(k, i)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			p.Get(keys[i%freeKeys])
			i++
		}
	})
}

// BenchmarkLockFloor_ThroughCache is the same read through AdaptiveCache. The
// gap against the direct benchmark is the cache's RWMutex plus its bookkeeping
// - the entire budget a lock-free read path could recover.
func BenchmarkLockFloor_ThroughCache(b *testing.B) {
	for _, sampled := range []bool{false, true} {
		name := "sample=off"
		rate := 0.0
		if sampled {
			name, rate = "sample=0.05", 0.05
		}

		b.Run(name, func(b *testing.B) {
			keys := freeKeySet()

			active := newFreePolicy[string, int](LRU, freeKeys*2)
			shadow := newFreePolicy[string, int](LFU, freeKeys*2)
			for i, k := range keys {
				active.prefill(k, i)
				shadow.prefill(k, i)
			}

			cache, err := NewAdaptiveCache[string, int](
				[]Policy[string, int]{active, shadow},
				&mockBandit{next: LRU},
				&Settings{
					EpochDuration:               time.Hour,
					EvictPartialCapacityFilling: true,
					ShadowSampleRate:            rate,
					MinShadowCapacity:           8,
				})
			if err != nil {
				b.Fatalf("NewAdaptiveCache: %v", err)
			}
			b.Cleanup(func() { _ = cache.Close() })

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					cache.Get(keys[i%freeKeys])
					i++
				}
			})
		})
	}
}

// BenchmarkLockFloor_RWMutexOnly isolates the read lock itself: the same
// parallel loop doing nothing but taking and releasing the cache's RWMutex.
// If this is close to the gap measured above, the lock is the cost; if it is
// far below, the cost is elsewhere and going lock-free would not recover it.
func BenchmarkLockFloor_RWMutexOnly(b *testing.B) {
	keys := freeKeySet()
	p := newFreePolicy[string, int](LRU, freeKeys*2)
	for i, k := range keys {
		p.prefill(k, i)
	}

	cache, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{p},
		&mockBandit{next: LRU},
		&Settings{EpochDuration: time.Hour, EvictPartialCapacityFilling: true},
	)
	if err != nil {
		b.Fatalf("NewAdaptiveCache: %v", err)
	}
	b.Cleanup(func() { _ = cache.Close() })

	// Taking and immediately releasing is the entire point: this measures the
	// lock, not work done under it.
	takeAndRelease := func() {
		cache.mu.RLock()
		defer cache.mu.RUnlock()
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			takeAndRelease()
		}
	})
}

// BenchmarkLockFloor_RealisticPolicy repeats the comparison with a policy that
// takes its own exclusive lock on Get, which is what a real LRU does: it must
// move the entry to the front, so reads mutate. This is the realistic estimate
// of what a lock-free read path would recover, because such a policy
// serialises regardless of what the cache above it does.
func BenchmarkLockFloor_RealisticPolicy(b *testing.B) {
	keys := freeKeySet()

	b.Run("direct", func(b *testing.B) {
		p := newBenchPolicy[string, int](LRU, freeKeys*2)
		for i, k := range keys {
			p.Add(k, i)
		}
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				p.Get(keys[i%freeKeys])
				i++
			}
		})
	})

	b.Run("through cache, sampled", func(b *testing.B) {
		active := newBenchPolicy[string, int](LRU, freeKeys*2)
		shadow := newBenchPolicy[string, int](LFU, freeKeys*2)

		cache, err := NewAdaptiveCache[string, int](
			[]Policy[string, int]{active, shadow},
			&mockBandit{next: LRU},
			&Settings{
				EpochDuration:               time.Hour,
				EvictPartialCapacityFilling: true,
				ShadowSampleRate:            0.05,
				MinShadowCapacity:           8,
			})
		if err != nil {
			b.Fatalf("NewAdaptiveCache: %v", err)
		}
		b.Cleanup(func() { _ = cache.Close() })

		for i, k := range keys {
			cache.Add(k, i)
		}

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				cache.Get(keys[i%freeKeys])
				i++
			}
		})
	})
}
