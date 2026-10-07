package bench_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
	"github.com/sshaplygin/as-cache/bench"
)

// replayAdaptiveEvidence captures the same configuration and cache instance
// that produce each observation; metadata must not infer settings separately.
func replayAdaptiveEvidence(t *testing.T, capacity int, workload bench.Workload, epochs int, settings *ascache.Settings) adaptiveRecord {
	t.Helper()
	record := adaptiveRecord{EpochsPerTrace: epochs, EpochRequests: settings.EpochRequests, Settings: *settings}
	for run := range traceEvidenceRuns {
		arms, err := bench.AdaptiveArms(capacity)
		require.NoError(t, err)
		cache, err := ascache.NewAdaptiveCache(arms, bandit.NewThompson(0.7, 13), settings)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, cache.Close()) })
		record.HitRate.Runs = append(record.HitRate.Runs, bench.Replay("adaptive", cache, workload).HitRate()*100)
		rate := cache.Advice().SampleRate
		if run == 0 {
			record.EffectiveSampleRate = rate
		} else {
			require.Equal(t, record.EffectiveSampleRate, rate)
		}
		require.NoError(t, cache.Close())
	}
	return record
}

func TestEvidenceMetadataUsesMeasuredConfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		capacity  int
		requested float64
		floor     int
		effective float64
	}{
		{"raised floor", 100, 0.05, 64, 0.64},
		{"changed configuration", 100, 0.25, 20, 0.25},
		{"clamped to full cache", 8, 0.05, 64, 1},
		{"library default floor", 1000, 0.1, 0, 0.256},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := traceEvidenceSettings(7)
			settings.ShadowSampleRate = test.requested
			settings.MinShadowCapacity = test.floor
			settings.SwitchCooldownEpochs = 9
			settings.MinHitRateImprovement = 0.07
			record := replayAdaptiveEvidence(t, test.capacity, bench.Workload{Keys: []string{"a", "b", "a"}}, 10, settings)
			data, err := json.Marshal(record)
			require.NoError(t, err)
			var decoded adaptiveRecord
			require.NoError(t, json.Unmarshal(data, &decoded))
			require.Equal(t, *settings, decoded.Settings)
			require.Equal(t, int64(7), decoded.EpochRequests)
			require.Equal(t, test.effective, decoded.EffectiveSampleRate)
			require.Len(t, decoded.HitRate.Runs, traceEvidenceRuns)
		})
	}
}

func TestReferenceMissPrecisionContract(t *testing.T) {
	for _, raw := range []string{"0.0000", "0.1234", "0.9999", "1.0000"} {
		_, err := parseReferenceMiss(raw)
		require.NoError(t, err, raw)
	}
	for _, raw := range []string{"0", "1", "0.1", "0.123", "0.12345", "NaN", "+Inf", "1.0001", "-0.0001", "0.abcd", "00.0000", " 0.0000"} {
		_, err := parseReferenceMiss(raw)
		require.Error(t, err, raw)
	}
	// Boundary examples retain the existing narrow gate; coarse input cannot
	// make a visibly incorrect LRU comparison pass by widening its tolerance.
	require.LessOrEqual(t, 0.005, referenceTolerance)
	require.Greater(t, 0.0052, referenceTolerance)
}
