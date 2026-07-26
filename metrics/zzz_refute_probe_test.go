package metrics_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/metrics"
	"github.com/sshaplygin/as-cache/policies"
)

// TestProbe_IdleCacheSnapshot prints what an observe-only cache that has served
// NOTHING publishes, after several epochs have elapsed.
func TestProbe_IdleCacheSnapshot(t *testing.T) {
	lru, err := policies.NewLRU[string, int](1000)
	require.NoError(t, err)
	twoQ, err := policies.NewTwoQueue[string, int](1000)
	require.NoError(t, err)

	cache, err := ascache.NewAdaptiveCache[string, int](
		[]ascache.Policy[string, int]{lru, twoQ}, nil,
		&ascache.Settings{EpochDuration: 5 * time.Millisecond, ObserveOnly: true},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	// Snapshot BEFORE any epoch has run at all.
	t.Logf("t=0   (no epoch yet): %s", metrics.Take(cache).String())

	// Let several epochs elapse with zero traffic.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cache.Advice().Epochs > 3 {
			break
		}
	}

	snap := metrics.Take(cache)
	t.Logf("idle  (epochs=%d): %s", snap.Epochs, snap.String())
	t.Logf("advice struct: %+v", cache.Advice())
	t.Logf("advice string:\n%s", cache.Advice().String())

	// Is the "nothing measured" state distinguishable from a genuine 0%?
	t.Logf("hits=%d misses=%d hits+misses=%d best=%q active=%q improvement=%v",
		snap.Hits, snap.Misses, snap.Hits+snap.Misses,
		snap.BestPolicy, snap.ActivePolicy, snap.Improvement)
}

// TestProbe_GenuineZeroHitRate shows what a cache that genuinely misses on
// everything publishes, for comparison with the idle case.
func TestProbe_GenuineZeroHitRate(t *testing.T) {
	lru, err := policies.NewLRU[string, int](10)
	require.NoError(t, err)
	twoQ, err := policies.NewTwoQueue[string, int](10)
	require.NoError(t, err)

	cache, err := ascache.NewAdaptiveCache[string, int](
		[]ascache.Policy[string, int]{lru, twoQ}, nil,
		&ascache.Settings{EpochDuration: 5 * time.Millisecond, ObserveOnly: true},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	// Every key distinct -> every Get is a miss.
	for i := 0; i < 5000; i++ {
		cache.Get(string(rune(i)) + "-never-stored")
	}

	snap := metrics.Take(cache)
	t.Logf("all-miss: %s", snap.String())
	t.Logf("hits=%d misses=%d hit_rate=%v", snap.Hits, snap.Misses, snap.HitRate)
}
