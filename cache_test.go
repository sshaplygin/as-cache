package ascache

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles ---

type mockBandit struct {
	next PolicyType
}

func (m *mockBandit) RecordStats(_ ShadowStats) {}
func (m *mockBandit) SelectPolicy() PolicyType  { return m.next }

// mockPolicy is a simple map-backed Policy implementation for testing.
type mockPolicy[K comparable, V any] struct {
	mu         sync.Mutex
	data       map[K]V
	cap        int
	policyType PolicyType
	stats      PolicyStats
}

func newMockPolicy[K comparable, V any](policyType PolicyType, capacity int) *mockPolicy[K, V] {
	return &mockPolicy[K, V]{
		data:       make(map[K]V, capacity),
		cap:        capacity,
		policyType: policyType,
	}
}

func (p *mockPolicy[K, V]) Add(key K, value V) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, evicted := p.data[key]
	p.data[key] = value
	return evicted
}

func (p *mockPolicy[K, V]) Get(key K) (V, bool) {
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

func (p *mockPolicy[K, V]) Peek(key K) (V, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.data[key]
	return v, ok
}

func (p *mockPolicy[K, V]) Contains(key K) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.data[key]
	return ok
}

func (p *mockPolicy[K, V]) Remove(key K) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.data[key]
	delete(p.data, key)
	return ok
}

func (p *mockPolicy[K, V]) Purge() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.data = make(map[K]V, p.cap)
}

func (p *mockPolicy[K, V]) Keys() []K {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make([]K, 0, len(p.data))
	for k := range p.data {
		keys = append(keys, k)
	}
	return keys
}

func (p *mockPolicy[K, V]) Values() []V {
	p.mu.Lock()
	defer p.mu.Unlock()
	vals := make([]V, 0, len(p.data))
	for _, v := range p.data {
		vals = append(vals, v)
	}
	return vals
}

func (p *mockPolicy[K, V]) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.data)
}

func (p *mockPolicy[K, V]) Cap() int { return p.cap }

func (p *mockPolicy[K, V]) Resize(size int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cap = size
	return 0
}

func (p *mockPolicy[K, V]) GetStats() PolicyStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *mockPolicy[K, V]) ResetStats() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stats = PolicyStats{}
}

func (p *mockPolicy[K, V]) GetType() PolicyType { return p.policyType }

// --- helpers ---

// makeCache constructs an AdaptiveCache with two mock policies (LRU active,
// LFU shadow) and a mock bandit. The bandit initially returns LRU so no
// switch happens until the caller changes bandit.next.
func makeCache(t *testing.T, strategy MigrationStrategy) (
	*AdaptiveCache[string, int],
	*mockPolicy[string, int],
	*mockPolicy[string, int],
	*mockBandit,
) {
	t.Helper()
	lru := newMockPolicy[string, int](LRU, 10)
	lfu := newMockPolicy[string, int](LFU, 10)
	bandit := &mockBandit{next: LRU}

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lru, lfu},
		bandit,
		&Settings{
			EpochDuration:               24 * time.Hour, // prevent automatic switching
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           strategy,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })
	return ac, lru, lfu, bandit
}

// triggerSwitch applies a policy switch synchronously, taking the same path
// runEpoch does so that promotion, migration and demotion are all exercised.
func triggerSwitch(ac *AdaptiveCache[string, int], to PolicyType) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	from := ac.activePolicy
	if from == to {
		return
	}
	ac.switchLocked(from, to)
}

// --- MigrationCold ---

func TestMigrationCold_StartsFresh(t *testing.T) {
	ac, lru, _, _ := makeCache(t, MigrationCold)

	ac.Add("a", 1)
	ac.Add("b", 2)

	require.Equal(t, 2, lru.Len(), "expected 2 keys in LRU")

	triggerSwitch(ac, LFU)

	// LFU should be empty after a cold switch.
	val, ok := ac.Get("a")
	assert.False(t, ok, "expected cold miss for 'a'")
	assert.Equal(t, 0, val)
}

// --- MigrationWarm ---

func TestMigrationWarm_CopiesAllKeys(t *testing.T) {
	ac, _, lfu, _ := makeCache(t, MigrationWarm)

	ac.Add("a", 1)
	ac.Add("b", 2)
	ac.Add("c", 3)

	triggerSwitch(ac, LFU)

	for key, want := range map[string]int{"a": 1, "b": 2, "c": 3} {
		got, ok := ac.Get(key)
		assert.True(t, ok, "warm: key %q not found after switch", key)
		assert.Equal(t, want, got, "warm: key %q value mismatch", key)
	}
	_ = lfu
}

func TestMigrationWarm_PurgesZeroValues(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationWarm)

	// Add "x" to active LRU (real value), which also shadow-adds zero to LFU.
	ac.Add("x", 99)

	// The shadow LFU now has x→0. After warm switch, it should be overwritten
	// with the real value 99.
	triggerSwitch(ac, LFU)

	val, ok := ac.Get("x")
	require.True(t, ok, "warm: key 'x' not found after switch")
	assert.Equal(t, 99, val, "warm: zero value leaked")
}

// --- MigrationGradual ---

func TestMigrationGradual_GetPromotes(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 42)

	triggerSwitch(ac, LFU)

	// LFU was purged at switch time and has no real data yet.
	// Get("a") should promote from old LRU policy.
	val, ok := ac.Get("a")
	require.True(t, ok, "gradual: Get did not promote 'a' from old policy")
	assert.Equal(t, 42, val, "gradual: promoted value mismatch")

	// Second Get should find it directly in the new active policy.
	val2, ok2 := ac.Get("a")
	assert.True(t, ok2, "gradual: second Get returned ok=false")
	assert.Equal(t, 42, val2, "gradual: second Get value mismatch")
}

func TestMigrationGradual_AddDrainsOneKeyPerCall(t *testing.T) {
	ac, lru, lfu, _ := makeCache(t, MigrationGradual)

	keys := []string{"k1", "k2", "k3", "k4", "k5"}
	for i, k := range keys {
		ac.Add(k, i+1)
	}

	require.Equal(t, 5, lru.Len(), "expected 5 keys in LRU before switch")

	triggerSwitch(ac, LFU)

	// After switch, LFU was purged. Each Add should drain one key.
	lfu.mu.Lock()
	beforeLen := len(lfu.data)
	lfu.mu.Unlock()
	require.Equal(t, 0, beforeLen, "expected LFU to be empty right after gradual switch")

	// Add a new key — should drain one old key into LFU as a side effect.
	ac.Add("new", 100)

	lfu.mu.Lock()
	afterLen := len(lfu.data)
	lfu.mu.Unlock()

	// LFU should contain "new" plus exactly one drained key from LRU.
	assert.Equal(t, 2, afterLen, "expected 2 keys in LFU after one Add (new + 1 drained)")
}

func TestMigrationGradual_ZeroValueNotPromoted(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	// "a" is added to LRU (real value) and shadow-added as zero to LFU.
	ac.Add("a", 55)

	triggerSwitch(ac, LFU)

	// Now Add "a" again with a new value while LFU is active.
	// This shadow-adds zero to LRU (the migration source) and marks "a" as
	// corrupted in migrationRealKeys. The Get should NOT promote a stale zero.
	ac.Add("a", 77)

	val, ok := ac.Get("a")
	require.True(t, ok, "gradual: 'a' should be in LFU after explicit Add")
	assert.Equal(t, 77, val, "gradual: expected latest Add value")
}

// TestMigrationGradual_PromotedGetCountsAsHit verifies that a Get served via
// promotion from the migration source is recorded as a hit on the active
// policy — not as a miss — so Stats() and the bandit report reflect a request
// the cache actually served.
func TestMigrationGradual_PromotedGetCountsAsHit(t *testing.T) {
	ac, _, lfu, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 42)
	triggerSwitch(ac, LFU)

	val, ok := ac.Get("a")
	require.True(t, ok, "promoted Get must serve the value")
	require.Equal(t, 42, val)

	stats := lfu.GetStats()
	assert.Equal(t, int64(1), stats.Hits, "promoted Get must count as a hit on the active policy")
	assert.Equal(t, int64(0), stats.Misses, "promoted Get must not leave a spurious miss")

	assert.Equal(t, GlobalStats{Hits: 1, Misses: 0}, ac.Stats(),
		"Stats must report the served request as a hit")
}

func TestMigrationGradual_EpochClearsMigration(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 1)
	triggerSwitch(ac, LFU)

	require.True(t, ac.migrating, "expected migration to be active after gradual switch")

	// Simulate epoch boundary (clearMigrationState is called at epoch start).
	ac.mu.Lock()
	ac.clearMigrationState()
	ac.mu.Unlock()

	assert.False(t, ac.migrating, "expected migration to be cleared after epoch")
	assert.Nil(t, ac.migrationRealKeys, "expected migrationRealKeys to be nil after clear")
}

func TestMigrationGradual_PurgeClearsMigration(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 1)
	triggerSwitch(ac, LFU)

	require.True(t, ac.migrating, "expected migration to be active after gradual switch")

	ac.Purge()

	ac.mu.RLock()
	migrating := ac.migrating
	ac.mu.RUnlock()

	assert.False(t, migrating, "Purge should have cleared gradual migration state")
}

func TestMigrationGradual_RemovePreventsPromotion(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 1)
	triggerSwitch(ac, LFU)

	// Remove "a" explicitly; it should be removed from migrationRealKeys too.
	ac.Remove("a")

	val, ok := ac.Get("a")
	assert.False(t, ok, "gradual: expected miss after Remove, got (%d, true)", val)
}

// TestMigrationGradual_RemoveLastKeyClosesWindow guards against a phantom
// migration window: Remove of the last pending key must close the window,
// otherwise every subsequent Get keeps taking the write lock until some other
// event (Add, Purge, policy switch) happens to end the migration.
func TestMigrationGradual_RemoveLastKeyClosesWindow(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 1)
	triggerSwitch(ac, LFU)
	require.True(t, ac.migrating, "expected migration window to open")

	ac.Remove("a")

	ac.mu.RLock()
	migrating := ac.migrating
	ac.mu.RUnlock()
	assert.False(t, migrating, "window must close when Remove empties the pending key set")
}

// TestMigrationGradual_EmptySourceOpensNoWindow verifies that switching away
// from an empty policy does not open a migration window at all.
func TestMigrationGradual_EmptySourceOpensNoWindow(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	triggerSwitch(ac, LFU)

	ac.mu.RLock()
	migrating := ac.migrating
	ac.mu.RUnlock()
	assert.False(t, migrating, "empty source must not open a migration window")
}

func TestMigrationGradual_DrainCompletesNaturally(t *testing.T) {
	ac, _, lfu, _ := makeCache(t, MigrationGradual)

	n := 5
	for i := 0; i < n; i++ {
		ac.Add(string(rune('a'+i)), i+1)
	}

	triggerSwitch(ac, LFU)

	require.True(t, ac.migrating, "expected migration to start")

	// Add n new keys — each drains one old key. After n Adds, all old keys
	// should be migrated and migrating should be false.
	for i := 0; i < n; i++ {
		ac.Add(string(rune('A'+i)), i+100)
	}

	ac.mu.RLock()
	migrating := ac.migrating
	ac.mu.RUnlock()

	assert.False(t, migrating, "expected migration to complete after draining all keys")

	// All original keys should be available (promoted or drained).
	for i := 0; i < n; i++ {
		key := string(rune('a' + i))
		if !lfu.Contains(key) {
			// Key was drained via Add — verify via ac.Get.
			val, ok := ac.Get(key)
			assert.True(t, ok, "key %q lost after gradual drain", key)
			assert.Equal(t, i+1, val, "key %q value mismatch", key)
		}
	}
}

// TestMigrationGradual_Concurrent exercises Get and Add concurrently during a
// gradual migration window. Run with -race to detect data races.
func TestMigrationGradual_Concurrent(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	for i := 0; i < 20; i++ {
		ac.Add(string(rune('a'+i%26)), i)
	}

	triggerSwitch(ac, LFU)

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				ac.Get(string(rune('a' + (g+i)%26)))
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				ac.Add(string(rune('A'+(g+i)%26)), g*i)
			}
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// AdaptiveCache: Stats, Resize, Contains, Keys, Values, Len, Peek,
// ActivePolicy
// ---------------------------------------------------------------------------

func TestAdaptiveCache_Stats(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	ac.Add("a", 1)
	ac.Add("b", 2)

	// Two hits
	ac.Get("a")
	ac.Get("b")
	// One miss
	ac.Get("missing")

	gs := ac.Stats()
	assert.Equal(t, int64(2), gs.Hits, "Stats.Hits mismatch")
	assert.Equal(t, int64(1), gs.Misses, "Stats.Misses mismatch")
}

func TestAdaptiveCache_Resize(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	ac.Add("a", 1)
	ac.Add("b", 2)

	evicted := ac.Resize(5)
	// mockPolicy.Resize returns 0, so total is 0. But the method was called.
	assert.Equal(t, 0, evicted, "Resize evicted count mismatch")
}

func TestAdaptiveCache_Contains(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	ac.Add("present", 42)

	assert.True(t, ac.Contains("present"), "Contains should return true for added key")
	assert.False(t, ac.Contains("absent"), "Contains should return false for missing key")
}

func TestAdaptiveCache_Keys(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	ac.Add("a", 1)
	ac.Add("b", 2)

	keys := ac.Keys()
	require.Len(t, keys, 2, "Keys length mismatch")
	assert.ElementsMatch(t, []string{"a", "b"}, keys)
}

func TestAdaptiveCache_Values(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	ac.Add("a", 10)
	ac.Add("b", 20)

	vals := ac.Values()
	require.Len(t, vals, 2, "Values length mismatch")
	assert.ElementsMatch(t, []int{10, 20}, vals)
}

func TestAdaptiveCache_Len(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	assert.Equal(t, 0, ac.Len(), "Len should be 0 for empty cache")

	ac.Add("a", 1)
	ac.Add("b", 2)

	assert.Equal(t, 2, ac.Len(), "Len mismatch")
}

func TestAdaptiveCache_Peek(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	ac.Add("a", 99)

	val, ok := ac.Peek("a")
	assert.True(t, ok)
	assert.Equal(t, 99, val)

	val, ok = ac.Peek("missing")
	assert.False(t, ok, "Peek for missing key should return false, got val=%d", val)
}

func TestAdaptiveCache_ActivePolicy(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	assert.Equal(t, LRU, ac.ActivePolicy())

	triggerSwitch(ac, LFU)

	assert.Equal(t, LFU, ac.ActivePolicy())
}

// ---------------------------------------------------------------------------
// AdaptiveCache: constructor edge cases
// ---------------------------------------------------------------------------

func TestNewAdaptiveCache_EmptyPolicies(t *testing.T) {
	_, err := NewAdaptiveCache(
		[]Policy[string, int]{},
		&mockBandit{next: LRU},
		&Settings{EpochDuration: time.Hour},
	)
	assert.ErrorIs(t, err, ErrEmptyPolicies)
}

func TestNewAdaptiveCache_NilPolicies(t *testing.T) {
	_, err := NewAdaptiveCache[string, int](
		nil,
		&mockBandit{next: LRU},
		&Settings{EpochDuration: time.Hour},
	)
	assert.ErrorIs(t, err, ErrEmptyPolicies)
}

func TestNewAdaptiveCache_NilBandit(t *testing.T) {
	_, err := NewAdaptiveCache(
		[]Policy[string, int]{newMockPolicy[string, int](LRU, 10)},
		nil,
		&Settings{EpochDuration: time.Hour},
	)
	assert.ErrorIs(t, err, ErrNilBandit)
}

func TestNewAdaptiveCache_NilSettings(t *testing.T) {
	_, err := NewAdaptiveCache(
		[]Policy[string, int]{newMockPolicy[string, int](LRU, 10)},
		&mockBandit{next: LRU},
		nil,
	)
	assert.ErrorIs(t, err, ErrNilSettings)
}

func TestNewAdaptiveCache_NonPositiveEpochDuration(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		_, err := NewAdaptiveCache(
			[]Policy[string, int]{newMockPolicy[string, int](LRU, 10)},
			&mockBandit{next: LRU},
			&Settings{EpochDuration: d},
		)
		assert.ErrorIs(t, err, ErrInvalidEpochDuration, "duration %s must be rejected", d)
	}
}

func TestNewAdaptiveCache_NilPolicyEntry(t *testing.T) {
	_, err := NewAdaptiveCache(
		[]Policy[string, int]{newMockPolicy[string, int](LRU, 10), nil},
		&mockBandit{next: LRU},
		&Settings{EpochDuration: time.Hour},
	)
	assert.ErrorIs(t, err, ErrNilPolicy)
}

func TestNewAdaptiveCache_DuplicatePolicyType(t *testing.T) {
	_, err := NewAdaptiveCache(
		[]Policy[string, int]{
			newMockPolicy[string, int](LRU, 10),
			newMockPolicy[string, int](LRU, 10),
		},
		&mockBandit{next: LRU},
		&Settings{EpochDuration: time.Hour},
	)
	assert.ErrorIs(t, err, ErrDuplicatePolicy)
}

// ---------------------------------------------------------------------------
// AdaptiveCache: tryChangePolicy via epoch ticker
// ---------------------------------------------------------------------------

// recordingBandit records all ShadowStats passed to RecordStats and returns
// a configurable next policy.
type recordingBandit struct {
	mu      sync.Mutex
	next    PolicyType
	records []ShadowStats
}

func (b *recordingBandit) RecordStats(stats ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records = append(b.records, stats)
}

func (b *recordingBandit) SelectPolicy() PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.next
}

func (b *recordingBandit) getRecords() []ShadowStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := make([]ShadowStats, len(b.records))
	copy(cp, b.records)
	return cp
}

func TestAdaptiveCache_TryChangePolicy_SwitchesPolicy(t *testing.T) {
	lruP := newMockPolicy[string, int](LRU, 10)
	lfuP := newMockPolicy[string, int](LFU, 10)
	bandit := &recordingBandit{next: LRU}

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lruP, lfuP},
		bandit,
		&Settings{
			EpochDuration:               24 * time.Hour,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           MigrationCold,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	// Add data and generate shadow stats.
	ac.Add("x", 1)
	ac.Get("x")
	ac.Get("missing")

	// Now set bandit to choose LFU and trigger the switch.
	bandit.mu.Lock()
	bandit.next = LFU
	bandit.mu.Unlock()

	selected := ac.tryChangePolicy()
	assert.Equal(t, LFU, selected, "expected bandit to select LFU")

	// Verify bandit received shadow stats for the non-active policy.
	records := bandit.getRecords()
	assert.NotEmpty(t, records, "expected bandit to receive shadow stats")
}

func TestAdaptiveCache_TryChangePolicy_NoSwitchWhenSamePolicy(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)
	// bandit.next is already LRU (same as active)

	selected := ac.tryChangePolicy()
	assert.Equal(t, LRU, selected, "expected bandit to select LRU (no switch)")
}

func TestAdaptiveCache_TryChangePolicy_SkipsWhenNotFull(t *testing.T) {
	lruP := newMockPolicy[string, int](LRU, 10)
	lfuP := newMockPolicy[string, int](LFU, 10)
	bandit := &mockBandit{next: LFU}

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lruP, lfuP},
		bandit,
		&Settings{
			EpochDuration:               24 * time.Hour,
			EvictPartialCapacityFilling: false, // require full capacity
			MigrationStrategy:           MigrationCold,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	// Cache is not full (0/10), so tryChangePolicy should not switch.
	ac.Add("a", 1)
	selected := ac.tryChangePolicy()
	assert.Equal(t, LRU, selected, "expected no switch when cache is not full and EvictPartialCapacityFilling=false")
}

// ---------------------------------------------------------------------------
// AdaptiveCache: switch stability (cool-down, minimum improvement, min samples)
// ---------------------------------------------------------------------------

// makeStabilityCache builds a two-policy cache whose bandit always picks LFU,
// with a 24h epoch so only explicit runEpoch calls advance it.
func makeStabilityCache(t *testing.T, s *Settings) (
	*AdaptiveCache[string, int],
	*mockPolicy[string, int],
	*mockPolicy[string, int],
) {
	t.Helper()

	lru := newMockPolicy[string, int](LRU, 10)
	lfu := newMockPolicy[string, int](LFU, 10)

	s.EpochDuration = 24 * time.Hour
	s.EvictPartialCapacityFilling = true

	ac, err := NewAdaptiveCache([]Policy[string, int]{lru, lfu}, &mockBandit{next: LFU}, s)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	return ac, lru, lfu
}

// primeStats gives the active policy `activeHits` hits out of `total` requests
// and the shadow the complement, by driving Get against keys that do or do not
// exist. Both policies see every Get, so we control the split by pre-seeding
// only the policy we want to hit.
func primeStats(p *mockPolicy[string, int], hits, misses int64) {
	p.mu.Lock()
	p.stats = PolicyStats{Hits: hits, Misses: misses}
	p.mu.Unlock()
}

// primeActiveStats sets the evidence the active arm reports to the bandit.
// The active policy is judged on the sampled substream rather than on its own
// full counters, so its epoch evidence lives on the cache, not on the policy.
func primeActiveStats(ac *AdaptiveCache[string, int], hits, misses int64) {
	ac.activeSampledHits.Store(hits)
	ac.activeSampledMisses.Store(misses)
}

func TestSwitchStability_ZeroSettingsSwitchesAsBefore(t *testing.T) {
	ac, lru, lfu := makeStabilityCache(t, &Settings{})

	primeActiveStats(ac, 5, 5)
	primeStats(lfu, 5, 5)
	_ = lru

	ac.runEpoch()

	assert.Equal(t, LFU, ac.ActivePolicy(),
		"a zero-valued Settings must apply every bandit selection")
}

func TestSwitchStability_MinHitRateImprovementBlocksMarginalSwitch(t *testing.T) {
	ac, lru, lfu := makeStabilityCache(t, &Settings{MinHitRateImprovement: 0.10})

	// Candidate is better, but only by 0.02 - below the 0.10 threshold.
	primeActiveStats(ac, 50, 50) // active hit rate 0.50
	primeStats(lfu, 52, 48)      // candidate hit rate 0.52
	_ = lru

	ac.runEpoch()

	assert.Equal(t, LRU, ac.ActivePolicy(),
		"a marginal improvement must not trigger a switch")
}

func TestSwitchStability_MinHitRateImprovementAllowsClearWin(t *testing.T) {
	ac, lru, lfu := makeStabilityCache(t, &Settings{MinHitRateImprovement: 0.10})

	primeActiveStats(ac, 40, 60) // active hit rate 0.40
	primeStats(lfu, 70, 30)      // candidate hit rate 0.70, +0.30
	_ = lru

	ac.runEpoch()

	assert.Equal(t, LFU, ac.ActivePolicy(),
		"an improvement above the threshold must trigger a switch")
}

func TestSwitchStability_CooldownBlocksConsecutiveSwitches(t *testing.T) {
	ac, lru, lfu := makeStabilityCache(t, &Settings{SwitchCooldownEpochs: 3})

	// Epoch 0: lastSwitchEpoch is 0 and epochID is 0, so the cool-down has
	// not elapsed yet and the switch is held back.
	primeStats(lru, 1, 1)
	primeStats(lfu, 1, 1)
	ac.runEpoch()
	require.Equal(t, LRU, ac.ActivePolicy(), "cool-down must hold the first switch")

	// Epochs 1 and 2 remain inside the window.
	ac.runEpoch()
	ac.runEpoch()
	require.Equal(t, LRU, ac.ActivePolicy(), "cool-down must still hold at epoch 2")

	// Epoch 3: three epochs have elapsed, the switch is allowed.
	ac.runEpoch()
	assert.Equal(t, LFU, ac.ActivePolicy(), "switch must be allowed once the cool-down elapses")
}

func TestSwitchStability_CooldownRearmsAfterSwitch(t *testing.T) {
	ac, _, _ := makeStabilityCache(t, &Settings{SwitchCooldownEpochs: 2})

	// Drive epochs until the first switch lands.
	for i := 0; i < 3; i++ {
		ac.runEpoch()
	}
	require.Equal(t, LFU, ac.ActivePolicy(), "expected the first switch to land")

	ac.mu.RLock()
	lastSwitch, epochID := ac.lastSwitchEpoch, ac.epochID
	ac.mu.RUnlock()

	assert.Equal(t, epochID-1, lastSwitch, "lastSwitchEpoch must record the switching epoch")
}

func TestSwitchStability_MinEpochRequestsBlocksThinEvidence(t *testing.T) {
	ac, _, lfu := makeStabilityCache(t, &Settings{MinEpochRequests: 100})

	primeActiveStats(ac, 1, 1) // 2 requests
	primeStats(lfu, 5, 0)      // 5 requests, perfect hit rate but far too few
	ac.runEpoch()
	require.Equal(t, LRU, ac.ActivePolicy(), "a handful of samples must not trigger a switch")

	primeActiveStats(ac, 100, 100)
	primeStats(lfu, 200, 0)
	ac.runEpoch()
	assert.Equal(t, LFU, ac.ActivePolicy(), "enough samples must allow the switch")
}

func TestSwitchStability_GatedEpochClearsEvidence(t *testing.T) {
	lru := newMockPolicy[string, int](LRU, 10)
	lfu := newMockPolicy[string, int](LFU, 10)

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lru, lfu},
		&mockBandit{next: LFU},
		&Settings{
			EpochDuration: 24 * time.Hour,
			// Require a full cache before switching; the mocks stay empty.
			EvictPartialCapacityFilling: false,
			MinHitRateImprovement:       0.01,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	primeActiveStats(ac, 10, 90)
	primeStats(lfu, 90, 10)
	_ = lru

	ac.runEpoch()

	assert.Equal(t, LRU, ac.ActivePolicy(), "a gated epoch must not switch")

	ac.mu.RLock()
	stats := len(ac.epochStats)
	ac.mu.RUnlock()
	assert.Zero(t, stats, "a gated epoch must not leave stale evidence for the gates")
}

// ---------------------------------------------------------------------------
// AdaptiveCache: epoch-based background switching
// ---------------------------------------------------------------------------

func TestAdaptiveCache_EpochBasedSwitch(t *testing.T) {
	lruP := newMockPolicy[string, int](LRU, 10)
	lfuP := newMockPolicy[string, int](LFU, 10)
	bandit := &mockBandit{next: LFU}

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lruP, lfuP},
		bandit,
		&Settings{
			EpochDuration:               5 * time.Millisecond,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           MigrationCold,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	// Add some data so the shadow has stats to report.
	ac.Add("a", 1)

	// Wait for at least one epoch tick.
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, LFU, ac.ActivePolicy(), "expected epoch-based switch to LFU")
}

// ---------------------------------------------------------------------------
// AdaptiveCache: bandit receives active policy stats; Stats() stays cumulative
// ---------------------------------------------------------------------------

// TestAdaptiveCache_BanditReceivesActivePolicyStats verifies that the active
// policy's epoch stats are reported to the bandit alongside the shadows and
// that every policy's counters are reset after reporting.
func TestAdaptiveCache_BanditReceivesActivePolicyStats(t *testing.T) {
	lruP := newMockPolicy[string, int](LRU, 10)
	lfuP := newMockPolicy[string, int](LFU, 10)
	bandit := &recordingBandit{next: LRU}

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lruP, lfuP},
		bandit,
		&Settings{
			EpochDuration:               24 * time.Hour,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           MigrationCold,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	ac.Add("x", 1)
	ac.Get("x")       // hit in active LRU and in shadow LFU (shadow-added key)
	ac.Get("missing") // miss everywhere

	_ = ac.tryChangePolicy()

	byPolicy := map[PolicyType]ShadowStats{}
	for _, r := range bandit.getRecords() {
		byPolicy[r.Policy] = r
	}

	require.Contains(t, byPolicy, LRU, "active policy stats must reach the bandit")
	require.Contains(t, byPolicy, LFU, "shadow policy stats must reach the bandit")
	assert.Equal(t, int64(1), byPolicy[LRU].Hits, "active policy hits mismatch")
	assert.Equal(t, int64(1), byPolicy[LRU].Misses, "active policy misses mismatch")
	assert.Equal(t, int64(1), byPolicy[LFU].Hits, "shadow policy hits mismatch")
	assert.Equal(t, int64(1), byPolicy[LFU].Misses, "shadow policy misses mismatch")

	assert.Equal(t, PolicyStats{}, lruP.GetStats(), "active counters must reset after reporting")
	assert.Equal(t, PolicyStats{}, lfuP.GetStats(), "shadow counters must reset after reporting")
}

// TestAdaptiveCache_StatsCumulativeAcrossEpochs verifies that Stats() keeps
// cumulative totals even though per-policy counters are reset every epoch.
func TestAdaptiveCache_StatsCumulativeAcrossEpochs(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	ac.Add("a", 1)
	ac.Get("a")       // hit
	ac.Get("missing") // miss

	before := ac.Stats()
	require.Equal(t, GlobalStats{Hits: 1, Misses: 1}, before)

	// Simulate an epoch boundary: stats are reported to the bandit and the
	// per-policy counters reset.
	_ = ac.tryChangePolicy()

	assert.Equal(t, before, ac.Stats(), "Stats must be cumulative across epoch resets")

	ac.Get("a")
	assert.Equal(t, GlobalStats{Hits: 2, Misses: 1}, ac.Stats(), "Stats must keep accumulating after the reset")
}

// TestAdaptiveCache_DemotionResetsCounters verifies that a demoted policy
// starts its first shadow epoch with clean counters: its active-tenure stats
// must not leak into the next epoch's shadow report.
func TestAdaptiveCache_DemotionResetsCounters(t *testing.T) {
	lruP := newMockPolicy[string, int](LRU, 10)
	lfuP := newMockPolicy[string, int](LFU, 10)
	bandit := &recordingBandit{next: LFU}

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lruP, lfuP},
		bandit,
		&Settings{
			EpochDuration:               24 * time.Hour,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           MigrationCold,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	ac.Add("x", 1)
	ac.Get("x") // hit while LRU is active

	// Epoch boundary: the bandit picks LFU, demoting LRU to a shadow.
	ac.runEpoch()
	require.Equal(t, LFU, ac.ActivePolicy())

	ac.Get("y") // miss in active LFU and in shadow LRU

	// The demoted LRU must carry only post-demotion traffic.
	assert.Equal(t, PolicyStats{Misses: 1}, lruP.GetStats(),
		"active-tenure stats leaked into the first shadow epoch")
}

// ---------------------------------------------------------------------------
// AdaptiveCache: context cancellation stops background goroutine
// ---------------------------------------------------------------------------

func TestAdaptiveCache_Close(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	err := ac.Close()
	require.NoError(t, err)

	// After Close, the context should be cancelled.
	select {
	case <-ac.ctx.Done():
		// expected
	default:
		assert.Fail(t, "expected context to be done after Close")
	}
}

func TestAdaptiveCache_Close_Idempotent(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	require.NoError(t, ac.Close())
	require.NoError(t, ac.Close())
}

func TestAdaptiveCache_Close_Concurrent(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, ac.Close())
		}()
	}
	wg.Wait()
}

// TestAdaptiveCache_Close_StopsEpochGoroutine verifies Close waits for the
// background goroutine: once Close returns, the epoch counter cannot advance.
func TestAdaptiveCache_Close_StopsEpochGoroutine(t *testing.T) {
	lruP := newMockPolicy[string, int](LRU, 10)
	lfuP := newMockPolicy[string, int](LFU, 10)

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lruP, lfuP},
		&mockBandit{next: LRU},
		&Settings{
			EpochDuration:               time.Millisecond,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           MigrationCold,
		},
	)
	require.NoError(t, err)

	require.NoError(t, ac.Close())

	ac.mu.RLock()
	before := ac.epochID
	ac.mu.RUnlock()

	time.Sleep(20 * time.Millisecond)

	ac.mu.RLock()
	after := ac.epochID
	ac.mu.RUnlock()

	assert.Equal(t, before, after, "epoch goroutine kept running after Close returned")
}

// blockingBandit parks inside SelectPolicy until released, so a test can hold
// the background epoch goroutine provably in-flight.
type blockingBandit struct {
	entered chan struct{} // buffered(1); signalled on first SelectPolicy entry
	release chan struct{} // closed to let SelectPolicy return
}

func (b *blockingBandit) RecordStats(_ ShadowStats) {}

func (b *blockingBandit) SelectPolicy() PolicyType {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return LRU
}

// TestAdaptiveCache_Close_WaitsForInFlightEpoch verifies that Close blocks on
// the WaitGroup: while the epoch goroutine is parked inside the bandit, Close
// must not return, and it must return promptly once the goroutine can exit.
// Without wg.Wait in Close this fails at the "Close returned while in-flight"
// select.
func TestAdaptiveCache_Close_WaitsForInFlightEpoch(t *testing.T) {
	bandit := &blockingBandit{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{
			newMockPolicy[string, int](LRU, 10),
			newMockPolicy[string, int](LFU, 10),
		},
		bandit,
		&Settings{
			EpochDuration: time.Millisecond,
			// Load-bearing: with partial filling disallowed the not-yet-full
			// cache would make selectPolicyLocked return before ever calling
			// bandit.SelectPolicy, and the goroutine would never park.
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           MigrationCold,
		},
	)
	require.NoError(t, err)

	// If an assertion below fails, unblock the bandit so the epoch goroutine
	// (parked while holding the cache mutex) does not leak, then Close.
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(bandit.release) })
		_ = ac.Close()
	})

	select {
	case <-bandit.entered:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "epoch goroutine never reached the bandit")
	}

	closed := make(chan struct{})
	go func() {
		_ = ac.Close()
		close(closed)
	}()

	select {
	case <-closed:
		require.FailNow(t, "Close returned while the epoch goroutine was still in-flight")
	case <-time.After(50 * time.Millisecond):
		// expected: Close is blocked in wg.Wait
	}

	releaseOnce.Do(func() { close(bandit.release) })

	select {
	case <-closed:
		// expected: goroutine exited, Close returned
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Close did not return after the epoch goroutine exited")
	}
}

// ---------------------------------------------------------------------------
// AdaptiveCache: Remove propagates to shadow policies
// ---------------------------------------------------------------------------

func TestAdaptiveCache_Remove_PropagatesToShadows(t *testing.T) {
	ac, lru, lfu, _ := makeCache(t, MigrationCold)

	ac.Add("a", 1)

	// Verify the shadow (LFU) received the key.
	require.True(t, lfu.Contains("a"), "shadow LFU should contain 'a' after Add")

	ac.Remove("a")

	assert.False(t, lru.Contains("a"), "LRU should not contain 'a' after Remove")
	assert.False(t, lfu.Contains("a"), "shadow LFU should not contain 'a' after Remove")
}

// ---------------------------------------------------------------------------
// AdaptiveCache: Purge clears all policies
// ---------------------------------------------------------------------------

func TestAdaptiveCache_Purge_ClearsAll(t *testing.T) {
	ac, lru, lfu, _ := makeCache(t, MigrationCold)

	ac.Add("a", 1)
	ac.Add("b", 2)

	ac.Purge()

	assert.Equal(t, 0, lru.Len(), "LRU should be empty after Purge")
	assert.Equal(t, 0, lfu.Len(), "LFU should be empty after Purge")
	assert.Equal(t, 0, ac.Len(), "ac.Len() should be 0 after Purge")
}

// ---------------------------------------------------------------------------
// AdaptiveCache: concurrent operations on delegated methods
// ---------------------------------------------------------------------------

func TestAdaptiveCache_ConcurrentDelegatedMethods(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationCold)

	for i := 0; i < 10; i++ {
		ac.Add(string(rune('a'+i)), i)
	}

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = ac.Contains(string(rune('a' + i%10)))
				_ = ac.Keys()
				_ = ac.Values()
				_ = ac.Len()
				_, _ = ac.Peek(string(rune('a' + i%10)))
				_ = ac.Stats()
				_ = ac.ActivePolicy()
			}
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// PolicyType.String() coverage
// ---------------------------------------------------------------------------

func TestPolicyType_String(t *testing.T) {
	tests := []struct {
		pt   PolicyType
		want string
	}{
		{Undefined, "Undefined"},
		{LRU, "LRU"},
		{LFU, "LFU"},
		{PolicyType(99), "PolicyType(99)"},
	}

	for _, tt := range tests {
		got := tt.pt.String()
		assert.Equal(t, tt.want, got, "PolicyType(%d).String() mismatch", tt.pt)
	}
}

// ---------------------------------------------------------------------------
// Concurrency regression tests
// ---------------------------------------------------------------------------

// flipBandit alternates its selection on every call so the active policy
// changes on every epoch tick, exercising the migrate + activePolicy swap path.
type flipBandit struct {
	mu sync.Mutex
	n  int
}

func (b *flipBandit) RecordStats(_ ShadowStats) {}
func (b *flipBandit) SelectPolicy() PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	if b.n%2 == 0 {
		return LRU
	}
	return LFU
}

// TestAdaptiveCache_ConcurrentSwitchAndAccess_NoRace guards against the data
// race where runEpoch mutated activePolicy / migration state outside the lock.
// Before the fix this fails under `go test -race`.
func TestAdaptiveCache_ConcurrentSwitchAndAccess_NoRace(t *testing.T) {
	lruP := newMockPolicy[string, int](LRU, 100)
	lfuP := newMockPolicy[string, int](LFU, 100)

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lruP, lfuP},
		&flipBandit{},
		&Settings{
			EpochDuration:               time.Millisecond,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           MigrationWarm,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					ac.Add("k", 1)
					ac.Get("k")
					ac.Contains("k")
					_ = ac.Keys()
					_ = ac.Len()
					_ = ac.ActivePolicy()
				}
			}
		}()
	}

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestCacheWrapper_ConcurrentGet_NoRace guards against the data race where
// CacheWrapper.Get mutated its stats counters non-atomically while callers
// invoked it concurrently. Before the fix this fails under `go test -race`.
func TestCacheWrapper_ConcurrentGet_NoRace(t *testing.T) {
	underlying := newMockPolicy[string, int](LRU, 100)
	w := NewCache[string, int](underlying, LRU, 100)
	w.Add("k", 1)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20000; j++ {
				w.Get("k")
				w.Get("missing")
			}
		}()
	}
	wg.Wait()

	stats := w.GetStats()
	// 8 goroutines * 20000 iters each of one hit + one miss.
	assert.Equal(t, int64(8*20000), stats.Hits, "hit count must not be lost to races")
	assert.Equal(t, int64(8*20000), stats.Misses, "miss count must not be lost to races")
}

// TestAdaptiveCache_StressAcrossEpochBoundaries hammers the full public API
// from many goroutines while 1ms epochs force a policy switch on every tick,
// for each migration strategy. It asserts nothing beyond survival: its job is
// to let the race detector observe every cross-epoch interleaving.
func TestAdaptiveCache_StressAcrossEpochBoundaries(t *testing.T) {
	strategies := map[string]MigrationStrategy{
		"cold":    MigrationCold,
		"warm":    MigrationWarm,
		"gradual": MigrationGradual,
	}

	for name, strategy := range strategies {
		t.Run(name, func(t *testing.T) {
			lruP := newMockPolicy[string, int](LRU, 100)
			lfuP := newMockPolicy[string, int](LFU, 100)

			ac, err := NewAdaptiveCache(
				[]Policy[string, int]{lruP, lfuP},
				&flipBandit{},
				&Settings{
					EpochDuration:               time.Millisecond,
					EvictPartialCapacityFilling: true,
					MigrationStrategy:           strategy,
				},
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = ac.Close() })

			const goroutines = 4
			var wg sync.WaitGroup
			stop := make(chan struct{})

			// Writers: Add with periodic Remove.
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func(seed int) {
					defer wg.Done()
					for i := 0; ; i++ {
						select {
						case <-stop:
							return
						default:
							key := string(rune('a' + (seed+i)%26))
							ac.Add(key, seed*1000+i)
							if i%7 == 0 {
								ac.Remove(key)
							}
						}
					}
				}(g)
			}

			// Readers: every read-path method.
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func(seed int) {
					defer wg.Done()
					for i := 0; ; i++ {
						select {
						case <-stop:
							return
						default:
							key := string(rune('a' + (seed+i)%26))
							ac.Get(key)
							ac.Peek(key)
							ac.Contains(key)
							_ = ac.Keys()
							_ = ac.Len()
							_ = ac.Stats()
							_ = ac.ActivePolicy()
						}
					}
				}(g)
			}

			// Maintenance: occasional Purge and Resize.
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
						time.Sleep(10 * time.Millisecond)
						if i%2 == 0 {
							ac.Purge()
						} else {
							ac.Resize(100)
						}
					}
				}
			}()

			time.Sleep(200 * time.Millisecond)
			close(stop)
			wg.Wait()
		})
	}
}
