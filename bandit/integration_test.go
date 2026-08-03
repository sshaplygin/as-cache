package bandit

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
)

// evictOrder selects which end of the insertion order a testPolicy discards.
type evictOrder int

const (
	// evictOldest is LRU: discard the least recently used entry.
	evictOldest evictOrder = iota + 1
	// evictNewest is MRU: discard the most recently used entry. It is the
	// right policy for a cyclic scan just larger than the cache, which is
	// exactly where LRU serves nothing at all - so the two make an
	// unambiguous pair to test selection with.
	evictNewest
)

// testPolicy is a small recency-ordered cache. The bandit module depends on
// the core module alone, so it cannot reach for the ready-made policies; this
// is enough to drive a real AdaptiveCache.
type testPolicy struct {
	mu       sync.Mutex
	entries  map[string]int
	order    []string
	capacity int
	evict    evictOrder
	policy   ascache.PolicyType
	hits     int64
	misses   int64
}

func newTestPolicy(policy ascache.PolicyType, capacity int, order evictOrder) *testPolicy {
	return &testPolicy{
		entries:  make(map[string]int, capacity),
		capacity: capacity,
		evict:    order,
		policy:   policy,
	}
}

func (p *testPolicy) touchLocked(key string) {
	for i, existing := range p.order {
		if existing == key {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	p.order = append(p.order, key)
}

func (p *testPolicy) Add(key string, value int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	_, existed := p.entries[key]
	if !existed && p.capacity > 0 && len(p.entries) >= p.capacity {
		victim := p.order[0]
		if p.evict == evictNewest {
			victim = p.order[len(p.order)-1]
		}
		delete(p.entries, victim)
		p.removeFromOrderLocked(victim)
	}

	if p.capacity <= 0 {
		return false
	}

	p.entries[key] = value
	p.touchLocked(key)

	return !existed
}

func (p *testPolicy) removeFromOrderLocked(key string) {
	for i, existing := range p.order {
		if existing == key {
			p.order = append(p.order[:i], p.order[i+1:]...)
			return
		}
	}
}

func (p *testPolicy) Get(key string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	value, ok := p.entries[key]
	if ok {
		p.hits++
		p.touchLocked(key)
	} else {
		p.misses++
	}

	return value, ok
}

func (p *testPolicy) Peek(key string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	value, ok := p.entries[key]

	return value, ok
}

func (p *testPolicy) Contains(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	_, ok := p.entries[key]

	return ok
}

func (p *testPolicy) Remove(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	_, ok := p.entries[key]
	delete(p.entries, key)
	p.removeFromOrderLocked(key)

	return ok
}

func (p *testPolicy) Purge() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.entries = make(map[string]int, p.capacity)
	p.order = nil
}

func (p *testPolicy) Keys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	keys := make([]string, len(p.order))
	copy(keys, p.order)

	return keys
}

func (p *testPolicy) Values() []int {
	p.mu.Lock()
	defer p.mu.Unlock()

	values := make([]int, 0, len(p.order))
	for _, key := range p.order {
		values = append(values, p.entries[key])
	}

	return values
}

func (p *testPolicy) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.entries)
}

func (p *testPolicy) Cap() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.capacity
}

func (p *testPolicy) Resize(size int) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.capacity = size

	evicted := 0
	for len(p.entries) > size && len(p.order) > 0 {
		victim := p.order[0]
		delete(p.entries, victim)
		p.order = p.order[1:]
		evicted++
	}

	return evicted
}

func (p *testPolicy) GetStats() ascache.PolicyStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	return ascache.PolicyStats{Hits: p.hits, Misses: p.misses}
}

func (p *testPolicy) ResetStats() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.hits, p.misses = 0, 0
}

func (p *testPolicy) GetType() ascache.PolicyType { return p.policy }

// TestIntegration_FleetOfCachesSwitchesToTheBetterPolicy wires the whole thing
// together: real AdaptiveCaches with their own epoch goroutines, real bandits
// with their own coordination goroutines, one shared store.
//
// The workload is a cyclic scan slightly larger than the cache, where LRU
// serves nothing at all and MRU serves most of it. Each replica sees a
// fraction of the traffic.
func TestIntegration_FleetOfCachesSwitchesToTheBetterPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-dependent: runs goroutines at millisecond epochs")
	}

	const (
		replicas   = 4
		capacity   = 32
		workingSet = 40
	)

	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	caches := make([]*ascache.AdaptiveCache[string, int], 0, replicas)
	bandits := make([]*Distributed, 0, replicas)

	for i := range replicas {
		b, err := NewDistributed(Config{
			Store:             store,
			Namespace:         "integration",
			NodeID:            string(rune('a' + i)),
			CoordinationEpoch: 20 * time.Millisecond,
			Window:            4,
			Seed:              uint64(i + 1),
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = b.Close() })
		bandits = append(bandits, b)

		cache, err := ascache.NewAdaptiveCache(
			[]ascache.Policy[string, int]{
				newTestPolicy(ascache.LRU, capacity, evictOldest),
				newTestPolicy(ascache.TinyLFU, capacity, evictNewest),
			},
			b,
			&ascache.Settings{
				EpochDuration:               5 * time.Millisecond,
				EvictPartialCapacityFilling: true,
				MigrationStrategy:           ascache.MigrationWarm,
			},
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cache.Close() })
		caches = append(caches, cache)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, cache := range caches {
		wg.Go(func() {
			for round := 0; ; round++ {
				select {
				case <-stop:
					return
				default:
				}

				key := keyOf(round % workingSet)
				if _, ok := cache.Get(key); !ok {
					cache.Add(key, round)
				}
			}
		})
	}

	// Every replica should end up on the arm the fleet's pooled evidence
	// favours, through a decision none of them made alone.
	require.Eventually(t, func() bool {
		for _, cache := range caches {
			if cache.ActivePolicy() != ascache.TinyLFU {
				return false
			}
		}

		return true
	}, 15*time.Second, 25*time.Millisecond)

	close(stop)
	wg.Wait()

	for _, b := range bandits {
		snapshot := b.Snapshot()
		assert.False(t, snapshot.Fallback, "%s fell back despite a healthy store", snapshot.NodeID)
		assert.Positive(t, snapshot.Syncs)
	}

	// Leadership is claimed once per bucket across the fleet, so no single
	// replica can have led every one of them.
	led := 0
	for _, b := range bandits {
		if b.Snapshot().Leaderships > 0 {
			led++
		}
	}
	assert.Positive(t, led)
}

func keyOf(i int) string {
	return "k" + strconv.Itoa(i)
}

// TestIntegration_CacheSurvivesABanditThatNeverReachesItsStore is the failure
// case that matters most: the store is down from the start, so the bandit is
// in fallback for the whole run. The cache must keep serving throughout.
func TestIntegration_CacheSurvivesABanditThatNeverReachesItsStore(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-dependent: runs goroutines at millisecond epochs")
	}

	store := NewMemStore()
	store.Fail(assert.AnError)
	t.Cleanup(func() { _ = store.Close() })

	b, err := NewDistributed(Config{
		Store:             store,
		Namespace:         "outage",
		CoordinationEpoch: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	cache, err := ascache.NewAdaptiveCache(
		[]ascache.Policy[string, int]{
			newTestPolicy(ascache.LRU, 16, evictOldest),
			newTestPolicy(ascache.TinyLFU, 16, evictNewest),
		},
		b,
		&ascache.Settings{
			EpochDuration:               2 * time.Millisecond,
			EvictPartialCapacityFilling: true,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	deadline := time.Now().Add(2 * time.Second)
	for round := 0; time.Now().Before(deadline); round++ {
		key := keyOf(round % 20)
		if _, ok := cache.Get(key); !ok {
			cache.Add(key, round)
		}
	}

	stats := cache.Stats()
	assert.Positive(t, stats.Hits+stats.Misses, "the cache must keep serving through an outage")

	snapshot := b.Snapshot()
	assert.True(t, snapshot.Fallback)
	assert.Positive(t, snapshot.SyncFailures)
	assert.NotEqual(t, ascache.Undefined, cache.ActivePolicy())
}
