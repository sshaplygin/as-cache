package ascache

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeSampledCache builds a two-policy cache with sampling enabled at the
// given rate. Capacities are large enough that the miniature floor does not
// silently disable sampling.
func makeSampledCache(t *testing.T, rate float64, strategy MigrationStrategy) (
	*AdaptiveCache[string, int],
	*mockPolicy[string, int],
	*mockPolicy[string, int],
) {
	t.Helper()

	lru := newMockPolicy[string, int](LRU, 100000)
	lfu := newMockPolicy[string, int](LFU, 100000)

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lru, lfu},
		&mockBandit{next: LRU},
		&Settings{
			EpochDuration:               24 * time.Hour,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           strategy,
			ShadowSampleRate:            rate,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	return ac, lru, lfu
}

// ---------------------------------------------------------------------------
// Sampling: shadows track a fraction of the keyspace, the active policy all of it
// ---------------------------------------------------------------------------

func TestSampling_ShadowTracksOnlyTheSample(t *testing.T) {
	const rate = 0.05
	const keys = 5000

	ac, lru, lfu := makeSampledCache(t, rate, MigrationCold)

	for i := 0; i < keys; i++ {
		ac.Add("key-"+strconv.Itoa(i), i)
	}

	assert.Equal(t, keys, lru.Len(), "the active policy must hold every key")

	shadowLen := lfu.Len()
	assert.InDelta(t, rate*keys, float64(shadowLen), rate*keys*0.25,
		"the shadow should hold roughly %v of the keys, held %d", rate, shadowLen)
	assert.Less(t, shadowLen, keys/10, "the shadow must be dramatically smaller than the active policy")
}

func TestSampling_ActiveServesEveryKey(t *testing.T) {
	const keys = 2000

	ac, _, _ := makeSampledCache(t, 0.05, MigrationCold)

	for i := 0; i < keys; i++ {
		ac.Add("key-"+strconv.Itoa(i), i*7)
	}

	for i := 0; i < keys; i++ {
		got, ok := ac.Get("key-" + strconv.Itoa(i))
		require.True(t, ok, "key-%d must be served regardless of sampling", i)
		require.Equal(t, i*7, got, "key-%d value mismatch", i)
	}
}

// TestSampling_ArmsAreJudgedOnEqualEvidence checks the property that makes
// sampled measurement sound: the active policy is reported to the bandit over
// the same sampled substream as the shadows, so no arm carries more evidence
// than another.
func TestSampling_ArmsAreJudgedOnEqualEvidence(t *testing.T) {
	const keys = 4000

	lru := newMockPolicy[string, int](LRU, 100000)
	lfu := newMockPolicy[string, int](LFU, 100000)
	bandit := &recordingBandit{next: LRU}

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lru, lfu},
		bandit,
		&Settings{
			EpochDuration:               24 * time.Hour,
			EvictPartialCapacityFilling: true,
			ShadowSampleRate:            0.05,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	for i := 0; i < keys; i++ {
		ac.Add("key-"+strconv.Itoa(i), i)
	}
	for i := 0; i < keys; i++ {
		ac.Get("key-" + strconv.Itoa(i))
	}

	_ = ac.tryChangePolicy()

	byPolicy := map[PolicyType]ShadowStats{}
	for _, r := range bandit.getRecords() {
		byPolicy[r.Policy] = r
	}

	activeTotal := byPolicy[LRU].Hits + byPolicy[LRU].Misses
	shadowTotal := byPolicy[LFU].Hits + byPolicy[LFU].Misses

	require.NotZero(t, activeTotal, "the active arm must report something")
	assert.Equal(t, activeTotal, shadowTotal,
		"both arms must be measured over the identical sampled substream")
	assert.Less(t, activeTotal, int64(keys/10),
		"the reported evidence must be the sample, not the full traffic")

	// Stats(), unlike the bandit report, covers everything the cache served.
	stats := ac.Stats()
	assert.Equal(t, int64(keys), stats.Hits+stats.Misses,
		"Stats must report real traffic, not the sample")
}

func TestSampling_DisabledByDefault(t *testing.T) {
	const keys = 500

	ac, lru, lfu := makeCache4(t, MigrationCold)

	for i := 0; i < keys; i++ {
		ac.Add("key-"+strconv.Itoa(i), i)
	}

	assert.Equal(t, keys, lru.Len(), "active policy holds every key")
	assert.Equal(t, keys, lfu.Len(),
		"with the default settings the shadow must mirror every key, as before")
}

// TestSampling_SmallCacheDisablesSampling checks the degenerate end: a cache
// too small to host a meaningful miniature falls back to mirroring rather than
// measuring noise.
func TestSampling_SmallCacheDisablesSampling(t *testing.T) {
	lru := newMockPolicy[string, int](LRU, 50)
	lfu := newMockPolicy[string, int](LFU, 50)

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lru, lfu},
		&mockBandit{next: LRU},
		&Settings{
			EpochDuration:               24 * time.Hour,
			EvictPartialCapacityFilling: true,
			ShadowSampleRate:            0.01,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	assert.False(t, ac.sampler.sampling,
		"a 50-entry cache cannot host a useful miniature, so sampling must disable itself")

	for i := 0; i < 40; i++ {
		ac.Add("key-"+strconv.Itoa(i), i)
	}
	assert.Equal(t, 40, lfu.Len(), "with sampling disabled the shadow mirrors every key")
}

// ---------------------------------------------------------------------------
// Demotion: values are dropped, bookkeeping is kept
// ---------------------------------------------------------------------------

func TestDemotion_DropsValuesButKeepsKeys(t *testing.T) {
	ac, lru, _ := makeCache4(t, MigrationCold)

	const keys = 200
	for i := 0; i < keys; i++ {
		ac.Add("key-"+strconv.Itoa(i), i+1)
	}
	require.Equal(t, keys, lru.Len())

	triggerSwitch(ac, LFU)

	assert.Equal(t, keys, lru.Len(),
		"a demoted policy must keep its keys: they are its eviction bookkeeping")

	lru.mu.Lock()
	defer lru.mu.Unlock()
	for key, value := range lru.data {
		require.Zero(t, value,
			"a demoted policy must not keep holding real values (key %q)", key)
	}
}

// TestDemotion_NeverLeaksAZeroToACaller is the invariant everything else
// protects: dropping a demoted policy's values must never make a caller see a
// zero as if it were cached data, under any migration strategy.
func TestDemotion_NeverLeaksAZeroToACaller(t *testing.T) {
	strategies := map[string]MigrationStrategy{
		"cold":    MigrationCold,
		"warm":    MigrationWarm,
		"gradual": MigrationGradual,
	}

	for name, strategy := range strategies {
		t.Run(name, func(t *testing.T) {
			ac, _, _ := makeCache4(t, strategy)

			const keys = 100
			for i := 1; i <= keys; i++ {
				ac.Add("key-"+strconv.Itoa(i), i)
			}

			triggerSwitch(ac, LFU)

			for i := 1; i <= keys; i++ {
				key := "key-" + strconv.Itoa(i)
				got, ok := ac.Get(key)
				if !ok {
					// A miss is always a legitimate cache answer.
					continue
				}
				require.Equal(t, i, got,
					"%s: %q returned a value that was never stored - a dropped value leaked", name, key)
			}
		})
	}
}

func TestPromotion_RestoresFullCapacity(t *testing.T) {
	ac, lru, lfu := makeSampledCache(t, 0.05, MigrationCold)

	nominal := ac.nominalCap[LFU]
	require.Positive(t, nominal)
	require.Less(t, lfu.Cap(), nominal, "a shadow must start at miniature capacity")

	triggerSwitch(ac, LFU)

	assert.Equal(t, nominal, lfu.Cap(), "a promoted policy must be restored to full capacity")
	assert.Less(t, lru.Cap(), nominal, "the demoted policy must shrink to miniature capacity")
}

func TestDemotion_DeferredDuringGradualWindow(t *testing.T) {
	ac, lru, _ := makeCache4(t, MigrationGradual)

	for i := 1; i <= 20; i++ {
		ac.Add("key-"+strconv.Itoa(i), i)
	}

	triggerSwitch(ac, LFU)
	require.True(t, ac.migrating, "expected a gradual window to open")

	// While the window is open the source must still hold real values, or
	// promotion would hand callers zeros.
	lru.mu.Lock()
	nonZero := 0
	for _, v := range lru.data {
		if v != 0 {
			nonZero++
		}
	}
	lru.mu.Unlock()
	assert.Positive(t, nonZero, "the gradual source must keep its values while the window is open")

	ac.Purge()

	ac.mu.RLock()
	migrating := ac.migrating
	ac.mu.RUnlock()
	assert.False(t, migrating, "Purge must close the window")
}

// makeCache4 is makeCache with capacities large enough that the miniature
// capacity floor does not disable sampling, and sampling left at the default.
func makeCache4(t *testing.T, strategy MigrationStrategy) (
	*AdaptiveCache[string, int],
	*mockPolicy[string, int],
	*mockPolicy[string, int],
) {
	t.Helper()

	lru := newMockPolicy[string, int](LRU, 100000)
	lfu := newMockPolicy[string, int](LFU, 100000)

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lru, lfu},
		&mockBandit{next: LRU},
		&Settings{
			EpochDuration:               24 * time.Hour,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           strategy,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	return ac, lru, lfu
}

// ---------------------------------------------------------------------------
// Resize keeps shadows miniature
// ---------------------------------------------------------------------------

func TestResize_KeepsShadowsMiniature(t *testing.T) {
	ac, lru, lfu := makeSampledCache(t, 0.05, MigrationCold)

	ac.Resize(200000)

	assert.Equal(t, 200000, lru.Cap(), "the active policy takes the requested capacity")
	assert.Equal(t, 10000, lfu.Cap(), "a shadow takes the miniature capacity for that size")
}
