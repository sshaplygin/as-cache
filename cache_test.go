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

// forceSwitchTo triggers a policy switch by manipulating the bandit and calling
// tryChangePolicy directly (it is unexported, so we call it via the background
// ticker using a very-short epoch duration cache built just for that purpose).
// For unit tests we instead call the internal method directly via a thin helper.
func triggerSwitch(ac *AdaptiveCache[string, int], to PolicyType) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	from := ac.activePolicy
	if from == to {
		return
	}
	ac.clearMigrationState()
	ac.migrateData(from, to)
	ac.activePolicy = to
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
		g := g
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
