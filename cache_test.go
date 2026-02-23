package ascache

import (
	"sync"
	"testing"
	"time"
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
	if err != nil {
		t.Fatalf("NewAdaptiveCache: %v", err)
	}
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
	ac.oldPolicy = from
}

// --- MigrationCold ---

func TestMigrationCold_StartsFresh(t *testing.T) {
	ac, lru, _, _ := makeCache(t, MigrationCold)

	ac.Add("a", 1)
	ac.Add("b", 2)

	if lru.Len() != 2 {
		t.Fatalf("expected 2 keys in LRU, got %d", lru.Len())
	}

	triggerSwitch(ac, LFU)

	// LFU should be empty after a cold switch.
	val, ok := ac.Get("a")
	if ok || val != 0 {
		t.Errorf("expected cold miss for 'a', got (%v, %v)", val, ok)
	}
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
		if !ok {
			t.Errorf("warm: key %q not found after switch", key)
			continue
		}
		if got != want {
			t.Errorf("warm: key %q = %d, want %d", key, got, want)
		}
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
	if !ok {
		t.Fatal("warm: key 'x' not found after switch")
	}
	if val != 99 {
		t.Errorf("warm: expected 99, got %d (zero value leaked)", val)
	}
}

// --- MigrationGradual ---

func TestMigrationGradual_GetPromotes(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 42)

	triggerSwitch(ac, LFU)

	// LFU was purged at switch time and has no real data yet.
	// Get("a") should promote from old LRU policy.
	val, ok := ac.Get("a")
	if !ok {
		t.Fatal("gradual: Get did not promote 'a' from old policy")
	}
	if val != 42 {
		t.Errorf("gradual: promoted value = %d, want 42", val)
	}

	// Second Get should find it directly in the new active policy.
	val2, ok2 := ac.Get("a")
	if !ok2 || val2 != 42 {
		t.Errorf("gradual: second Get = (%v, %v), want (42, true)", val2, ok2)
	}
}

func TestMigrationGradual_AddDrainsOneKeyPerCall(t *testing.T) {
	ac, lru, lfu, _ := makeCache(t, MigrationGradual)

	keys := []string{"k1", "k2", "k3", "k4", "k5"}
	for i, k := range keys {
		ac.Add(k, i+1)
	}

	if lru.Len() != 5 {
		t.Fatalf("expected 5 keys in LRU before switch, got %d", lru.Len())
	}

	triggerSwitch(ac, LFU)

	// After switch, LFU was purged. Each Add should drain one key.
	lfu.mu.Lock()
	beforeLen := len(lfu.data)
	lfu.mu.Unlock()
	if beforeLen != 0 {
		t.Fatalf("expected LFU to be empty right after gradual switch, got %d", beforeLen)
	}

	// Add a new key — should drain one old key into LFU as a side effect.
	ac.Add("new", 100)

	lfu.mu.Lock()
	afterLen := len(lfu.data)
	lfu.mu.Unlock()

	// LFU should contain "new" plus exactly one drained key from LRU.
	if afterLen != 2 {
		t.Errorf("expected 2 keys in LFU after one Add (new + 1 drained), got %d", afterLen)
	}
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
	if !ok {
		t.Fatal("gradual: 'a' should be in LFU after explicit Add")
	}
	if val != 77 {
		t.Errorf("gradual: expected 77 (latest Add), got %d", val)
	}
}

func TestMigrationGradual_EpochClearsMigration(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 1)
	triggerSwitch(ac, LFU)

	if !ac.migrating {
		t.Fatal("expected migration to be active after gradual switch")
	}

	// Simulate epoch boundary (clearMigrationState is called at epoch start).
	ac.mu.Lock()
	ac.clearMigrationState()
	ac.mu.Unlock()

	if ac.migrating {
		t.Error("expected migration to be cleared after epoch")
	}
	if ac.migrationRealKeys != nil {
		t.Error("expected migrationRealKeys to be nil after clear")
	}
}

func TestMigrationGradual_PurgeClearsMigration(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 1)
	triggerSwitch(ac, LFU)

	if !ac.migrating {
		t.Fatal("expected migration to be active after gradual switch")
	}

	ac.Purge()

	ac.mu.RLock()
	migrating := ac.migrating
	ac.mu.RUnlock()

	if migrating {
		t.Error("Purge should have cleared gradual migration state")
	}
}

func TestMigrationGradual_RemovePreventsPromotion(t *testing.T) {
	ac, _, _, _ := makeCache(t, MigrationGradual)

	ac.Add("a", 1)
	triggerSwitch(ac, LFU)

	// Remove "a" explicitly; it should be removed from migrationRealKeys too.
	ac.Remove("a")

	val, ok := ac.Get("a")
	if ok {
		t.Errorf("gradual: expected miss after Remove, got (%d, true)", val)
	}
}

func TestMigrationGradual_DrainCompletesNaturally(t *testing.T) {
	ac, _, lfu, _ := makeCache(t, MigrationGradual)

	n := 5
	for i := 0; i < n; i++ {
		ac.Add(string(rune('a'+i)), i+1)
	}

	triggerSwitch(ac, LFU)

	if !ac.migrating {
		t.Fatal("expected migration to start")
	}

	// Add n new keys — each drains one old key. After n Adds, all old keys
	// should be migrated and migrating should be false.
	for i := 0; i < n; i++ {
		ac.Add(string(rune('A'+i)), i+100)
	}

	ac.mu.RLock()
	migrating := ac.migrating
	ac.mu.RUnlock()

	if migrating {
		t.Error("expected migration to complete after draining all keys")
	}

	// All original keys should be available (promoted or drained).
	for i := 0; i < n; i++ {
		key := string(rune('a' + i))
		if !lfu.Contains(key) {
			// Key was drained via Add — verify via ac.Get.
			val, ok := ac.Get(key)
			if !ok {
				t.Errorf("key %q lost after gradual drain", key)
				continue
			}
			if val != i+1 {
				t.Errorf("key %q = %d, want %d", key, val, i+1)
			}
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
