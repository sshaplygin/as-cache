package ascache

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evictingPolicy is a Policy that actually enforces its capacity, evicting in
// insertion order when full and on a shrinking Resize.
//
// mockPolicy deliberately does not: its Resize only records the new capacity.
// That blind spot hid three defects in the capacity handling around sampling,
// because every test that resized a policy saw its data survive regardless.
// Any test whose subject is capacity must use this type instead.
type evictingPolicy[K comparable, V any] struct {
	mu         sync.Mutex
	data       map[K]V
	order      []K
	cap        int
	policyType PolicyType
	stats      PolicyStats
}

func newEvictingPolicy[K comparable, V any](policyType PolicyType, capacity int) *evictingPolicy[K, V] {
	return &evictingPolicy[K, V]{
		data:       make(map[K]V, capacity),
		cap:        capacity,
		policyType: policyType,
	}
}

// evictLocked drops oldest-first until the cache is within capacity, returning
// how many entries it removed.
func (p *evictingPolicy[K, V]) evictLocked() int {
	evicted := 0
	for p.cap >= 0 && len(p.data) > p.cap {
		oldest := p.order[0]
		p.order = p.order[1:]
		if _, ok := p.data[oldest]; ok {
			delete(p.data, oldest)
			evicted++
		}
	}

	return evicted
}

func (p *evictingPolicy[K, V]) Add(key K, value V) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, existed := p.data[key]; !existed {
		p.order = append(p.order, key)
	}
	p.data[key] = value

	return p.evictLocked() > 0
}

func (p *evictingPolicy[K, V]) Get(key K) (V, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	v, ok := p.data[key]
	if ok {
		p.stats.Hits++
	} else {
		p.stats.Misses++
	}

	return v, ok
}

func (p *evictingPolicy[K, V]) Peek(key K) (V, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.data[key]

	return v, ok
}

func (p *evictingPolicy[K, V]) Contains(key K) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.data[key]

	return ok
}

func (p *evictingPolicy[K, V]) Remove(key K) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.data[key]
	delete(p.data, key)

	return ok
}

func (p *evictingPolicy[K, V]) Purge() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.data = make(map[K]V, p.cap)
	p.order = nil
}

func (p *evictingPolicy[K, V]) Keys() []K {
	p.mu.Lock()
	defer p.mu.Unlock()

	keys := make([]K, 0, len(p.data))
	for _, k := range p.order {
		if _, ok := p.data[k]; ok {
			keys = append(keys, k)
		}
	}

	return keys
}

func (p *evictingPolicy[K, V]) Values() []V {
	p.mu.Lock()
	defer p.mu.Unlock()

	vals := make([]V, 0, len(p.data))
	for _, v := range p.data {
		vals = append(vals, v)
	}

	return vals
}

func (p *evictingPolicy[K, V]) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.data)
}

func (p *evictingPolicy[K, V]) Cap() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.cap
}

func (p *evictingPolicy[K, V]) Resize(size int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cap = size

	return p.evictLocked()
}

func (p *evictingPolicy[K, V]) GetStats() PolicyStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.stats
}

func (p *evictingPolicy[K, V]) ResetStats() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stats = PolicyStats{}
}

func (p *evictingPolicy[K, V]) GetType() PolicyType { return p.policyType }

// makeEvictingCache builds a cache over two capacity-enforcing policies whose
// bandit always selects banditPick, so a test controls whether an epoch tick
// switches policies or leaves the active one in place.
func makeEvictingCache(t *testing.T, capacity int, banditPick PolicyType, settings *Settings) (
	*AdaptiveCache[string, int],
	*evictingPolicy[string, int],
	*evictingPolicy[string, int],
) {
	t.Helper()

	lru := newEvictingPolicy[string, int](LRU, capacity)
	lfu := newEvictingPolicy[string, int](LFU, capacity)

	settings.EpochDuration = 24 * time.Hour
	settings.EvictPartialCapacityFilling = true

	ac, err := NewAdaptiveCache([]Policy[string, int]{lru, lfu}, &mockBandit{next: banditPick}, settings)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	return ac, lru, lfu
}

// TestResize_KeepsGradualMigrationSourceIntact guards a resize that shrank the
// policy still draining into the active one down to miniature capacity,
// evicting the only copy of every key not yet promoted.
func TestResize_KeepsGradualMigrationSourceIntact(t *testing.T) {
	const capacity = 2000

	ac, _, _ := makeEvictingCache(t, capacity, LRU, &Settings{
		MigrationStrategy: MigrationGradual,
		ShadowSampleRate:  0.05,
		MinShadowCapacity: 8,
	})

	for i := 0; i < capacity; i++ {
		ac.Add("key-"+strconv.Itoa(i), i)
	}

	triggerSwitch(ac, LFU)
	require.True(t, ac.migrating, "expected a gradual window to open")

	// A resize to the capacity the cache already has must not lose anything.
	ac.Resize(capacity)

	found := 0
	for i := 0; i < capacity; i++ {
		if got, ok := ac.Get("key-" + strconv.Itoa(i)); ok {
			require.Equal(t, i, got, "key-%d returned a value that was never stored", i)
			found++
		}
	}

	assert.Equal(t, capacity, found,
		"a no-op resize during a gradual migration must not discard pending data")
}

// TestResize_ShadowCapacityTracksTheSampleRate guards a shrinking resize that
// left shadows running at a capacity larger than their share of the traffic.
// A shadow of capacity C fed an r-sampled stream simulates a cache of C/r, so
// breaking that identity makes every shadow look better than the active policy
// regardless of which policy is actually better.
func TestResize_ShadowCapacityTracksTheSampleRate(t *testing.T) {
	ac, _, lfu := makeEvictingCache(t, 20000, LRU, &Settings{ShadowSampleRate: 0.05})

	rate := ac.sampler.rate
	require.InDelta(t, 0.05, rate, 1e-9, "expected the requested rate to survive construction")

	for _, size := range []int{20000, 10000, 2000, 500, 100} {
		ac.Resize(size)

		got := float64(lfu.Cap()) / float64(size)
		assert.InDelta(t, rate, got, 0.01,
			"after Resize(%d) the shadow runs at capacity %d, which is %.4f of the cache but samples %.4f of the keys",
			size, lfu.Cap(), got, rate)
	}
}

// TestEpoch_ClosesAbandonedGradualWindow guards a window that stayed open
// forever when the workload stopped touching the keys still pending: the
// source kept real values at full capacity, competed as an arm no other shadow
// was comparable with, and forced every Get onto the write-locked path.
func TestEpoch_ClosesAbandonedGradualWindow(t *testing.T) {
	// The bandit keeps naming LFU, the policy that becomes active, so the epoch
	// tick does not switch and only the window-closing path can demote LRU.
	ac, lru, _ := makeEvictingCache(t, 1000, LFU, &Settings{
		MigrationStrategy: MigrationGradual,
		ShadowSampleRate:  0.05,
		MinShadowCapacity: 8,
	})

	for i := 0; i < 200; i++ {
		ac.Add("key-"+strconv.Itoa(i), i+1)
	}

	triggerSwitch(ac, LFU)
	require.True(t, ac.migrating, "expected a gradual window to open")
	require.Equal(t, 1000, lru.Cap(), "the source keeps full capacity while the window is open")

	// The workload never touches a pending key again; only an epoch tick can
	// end the window.
	ac.runEpoch()

	ac.mu.RLock()
	migrating := ac.migrating
	ac.mu.RUnlock()

	assert.False(t, migrating, "an epoch boundary must close an abandoned gradual window")
	assert.Equal(t, ac.shadowCap[LRU], lru.Cap(),
		"the source must be demoted to miniature capacity once the window closes")

	lru.mu.Lock()
	defer lru.mu.Unlock()
	for key, value := range lru.data {
		require.Zero(t, value, "the demoted source must not keep real values (key %q)", key)
	}
}

// TestDemotion_ResetsStats guards a demoted policy reporting measurements it
// took in its previous role, at a different capacity and over all traffic
// rather than the sample.
func TestDemotion_ResetsStats(t *testing.T) {
	ac, lru, _ := makeEvictingCache(t, 1000, LRU, &Settings{ShadowSampleRate: 0.05})

	for i := 0; i < 100; i++ {
		ac.Add("key-"+strconv.Itoa(i), i)
		ac.Get("key-" + strconv.Itoa(i))
	}
	require.NotEqual(t, PolicyStats{}, lru.GetStats(), "the active policy should have measured something")

	triggerSwitch(ac, LFU)

	assert.Equal(t, PolicyStats{}, lru.GetStats(),
		"a demoted policy must start its shadow tenure with no carried-over measurements")
}
