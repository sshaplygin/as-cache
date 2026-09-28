package bench_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bench"
)

type observeRun struct {
	HitRate       float64        `json:"hit_rate_percent"`
	EpochRequests int64          `json:"epoch_requests"`
	Advice        ascache.Advice `json:"advice"`
}

// ObserveOnly holds LRU active; Advice measures all nine policies on the sampled
// stream. This is an offline comparison, not a real-service performance trial.
func observeTrace(t *testing.T, capacity int, w bench.Workload) []observeRun {
	t.Helper()
	lru, err := fixedPolicy(t, "LRU").Build(capacity)
	require.NoError(t, err)
	baseline := bench.Replay("LRU", lru, w)
	var runs []observeRun
	for range traceEvidenceRuns {
		arms, buildErr := bench.AdaptiveArms(capacity)
		require.NoError(t, buildErr)
		settings := traceEvidenceSettings(int64(len(w.Keys) / 20))
		settings.ObserveOnly = true
		cache, cacheErr := ascache.NewAdaptiveCache(arms, nil, settings)
		require.NoError(t, cacheErr)
		t.Cleanup(func() { require.NoError(t, cache.Close()) })
		result := bench.Replay("observe-only", cache, w)
		advice := cache.Advice()
		require.Equal(t, baseline.Hits, result.Hits, "ObserveOnly must serve the unchanged LRU")
		require.Equal(t, ascache.LRU, advice.Active)
		require.Len(t, advice.Reports, 9)
		runs = append(runs, observeRun{result.HitRate() * 100, settings.EpochRequests, advice})
		require.NoError(t, cache.Close())
	}
	return runs
}
