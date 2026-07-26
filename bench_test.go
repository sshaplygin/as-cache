package ascache

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// benchPolicy is a minimal thread-safe Policy used to measure AdaptiveCache's
// own orchestration overhead. It is deliberately simple - a mutex around a map,
// like any real cache - so the numbers reflect the cost the adaptive layer adds
// on top of a policy, not the policy's own algorithm.
type benchPolicy[K comparable, V any] struct {
	mu         sync.RWMutex
	data       map[K]V
	cap        int
	policyType PolicyType
	hits       int64
	misses     int64
}

func newBenchPolicy[K comparable, V any](policyType PolicyType, capacity int) *benchPolicy[K, V] {
	return &benchPolicy[K, V]{
		data:       make(map[K]V, capacity),
		cap:        capacity,
		policyType: policyType,
	}
}

func (p *benchPolicy[K, V]) Add(key K, value V) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, existed := p.data[key]
	p.data[key] = value
	return existed
}

func (p *benchPolicy[K, V]) Get(key K) (V, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.data[key]
	if ok {
		p.hits++
	} else {
		p.misses++
	}
	return v, ok
}

func (p *benchPolicy[K, V]) Peek(key K) (V, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.data[key]
	return v, ok
}

func (p *benchPolicy[K, V]) Contains(key K) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.data[key]
	return ok
}

func (p *benchPolicy[K, V]) Remove(key K) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.data[key]
	delete(p.data, key)
	return ok
}

func (p *benchPolicy[K, V]) Purge() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.data = make(map[K]V, p.cap)
}

func (p *benchPolicy[K, V]) Keys() []K {
	p.mu.RLock()
	defer p.mu.RUnlock()
	keys := make([]K, 0, len(p.data))
	for k := range p.data {
		keys = append(keys, k)
	}
	return keys
}

func (p *benchPolicy[K, V]) Values() []V {
	p.mu.RLock()
	defer p.mu.RUnlock()
	vals := make([]V, 0, len(p.data))
	for _, v := range p.data {
		vals = append(vals, v)
	}
	return vals
}

func (p *benchPolicy[K, V]) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.data)
}

func (p *benchPolicy[K, V]) Cap() int { return p.cap }

func (p *benchPolicy[K, V]) Resize(size int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cap = size
	return 0
}

func (p *benchPolicy[K, V]) GetStats() PolicyStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return PolicyStats{Hits: p.hits, Misses: p.misses}
}

func (p *benchPolicy[K, V]) ResetStats() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hits, p.misses = 0, 0
}

func (p *benchPolicy[K, V]) GetType() PolicyType { return p.policyType }

const benchKeys = 1000

// newBenchCache builds a cache with shadowCount shadow policies alongside the
// active one, pre-populated with benchKeys keys, and an epoch long enough that
// no switch happens mid-benchmark. A sampleRate of 0 leaves sampling off.
func newBenchCache(b *testing.B, shadowCount int, sampleRate float64) (*AdaptiveCache[string, int], []string) {
	b.Helper()

	types := []PolicyType{LRU, LFU, PolicyType(3), PolicyType(4)}
	policies := make([]Policy[string, int], 0, shadowCount+1)
	for i := 0; i <= shadowCount; i++ {
		policies = append(policies, newBenchPolicy[string, int](types[i], benchKeys*2))
	}

	ac, err := NewAdaptiveCache(policies, &mockBandit{next: LRU}, &Settings{
		EpochDuration:               time.Hour,
		EvictPartialCapacityFilling: true,
		ShadowSampleRate:            sampleRate,
		// The floor would otherwise disable sampling at this cache size.
		MinShadowCapacity: 8,
	})
	if err != nil {
		b.Fatalf("NewAdaptiveCache: %v", err)
	}
	b.Cleanup(func() { _ = ac.Close() })

	keys := make([]string, benchKeys)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
		ac.Add(keys[i], i)
	}

	return ac, keys
}

// benchRates is the set of shadow sample rates each benchmark reports, so the
// overhead reduction from sampling is visible as a single comparison.
var benchRates = []struct {
	name string
	rate float64
}{
	{"sample=off", 0},
	{"sample=0.05", 0.05},
}

// BenchmarkGet measures the single-goroutine read path.
func BenchmarkGet(b *testing.B) {
	for _, r := range benchRates {
		for _, shadows := range []int{1, 3} {
			b.Run(r.name+"/shadows="+strconv.Itoa(shadows), func(b *testing.B) {
				ac, keys := newBenchCache(b, shadows, r.rate)
				b.ResetTimer()
				for i := 0; b.Loop(); i++ {
					ac.Get(keys[i%benchKeys])
				}
			})
		}
	}
}

// BenchmarkGetParallel measures read scalability: every goroutine contends on
// the cache's RWMutex on top of each policy's own lock, so this is where the
// per-operation shadow fan-out costs the most.
func BenchmarkGetParallel(b *testing.B) {
	for _, r := range benchRates {
		for _, shadows := range []int{1, 3} {
			b.Run(r.name+"/shadows="+strconv.Itoa(shadows), func(b *testing.B) {
				ac, keys := newBenchCache(b, shadows, r.rate)
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					i := 0
					for pb.Next() {
						ac.Get(keys[i%benchKeys])
						i++
					}
				})
			})
		}
	}
}

// BenchmarkMixedParallel measures a read-heavy workload with writes mixed in.
// Writes take the cache's write lock, so this is where readers being blocked
// by an unrelated Add shows up.
func BenchmarkMixedParallel(b *testing.B) {
	for _, r := range benchRates {
		b.Run(r.name, func(b *testing.B) {
			ac, keys := newBenchCache(b, 1, r.rate)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					if i%10 == 0 {
						ac.Add(keys[i%benchKeys], i)
					} else {
						ac.Get(keys[i%benchKeys])
					}
					i++
				}
			})
		})
	}
}

// BenchmarkAdd measures the write path, which fans out to every shadow policy.
func BenchmarkAdd(b *testing.B) {
	for _, r := range benchRates {
		for _, shadows := range []int{1, 3} {
			b.Run(r.name+"/shadows="+strconv.Itoa(shadows), func(b *testing.B) {
				ac, keys := newBenchCache(b, shadows, r.rate)
				b.ResetTimer()
				for i := 0; b.Loop(); i++ {
					ac.Add(keys[i%benchKeys], i)
				}
			})
		}
	}
}
