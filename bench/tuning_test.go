package bench_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
	"github.com/sshaplygin/as-cache/bench"
)

type tuningRecord struct {
	Trace         string           `json:"trace"`
	Strategy      string           `json:"strategy"`
	Gates         bool             `json:"gates"`
	Epochs        int              `json:"epochs"`
	EpochRequests int64            `json:"epoch_requests"`
	HitRate       spread           `json:"hit_rate_percent"`
	Settings      ascache.Settings `json:"settings"`
}

// TestAdaptiveTuning isolates configuration effects on the P3 example used in
// configuration.md. All four combinations use 5 repeats and 10/20/50 request
// epochs. It is not a search for a universally best production setting.
func TestAdaptiveTuning(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}
	dir, err := bench.TraceDir()
	if err != nil {
		t.Skipf("%s; configure %s", err, bench.TraceDirEnv)
	}
	var records []tuningRecord
	for _, spec := range knownTraces() {
		if spec.file != "arc_p3.gz" {
			continue
		}
		workload, loadErr := spec.load(filepath.Join(dir, spec.file))
		require.NoError(t, loadErr)
		for _, epochs := range traceEvidenceEpochs {
			for _, strategy := range []ascache.MigrationStrategy{ascache.MigrationCold, ascache.MigrationWarm} {
				for _, gates := range []bool{false, true} {
					record := tuningRecord{Trace: workload.Name, Epochs: epochs, EpochRequests: int64(len(workload.Keys) / epochs), Gates: gates, Strategy: "cold"}
					if strategy == ascache.MigrationWarm {
						record.Strategy = "warm"
					}
					for range traceEvidenceRuns {
						arms, err := bench.AdaptiveArms(spec.cache)
						require.NoError(t, err)
						settings := traceEvidenceSettings(record.EpochRequests)
						settings.MigrationStrategy = strategy
						if gates {
							settings.MinHitRateImprovement = 0.02
							settings.SwitchCooldownEpochs = 3
						}
						record.Settings = *settings
						cache, err := ascache.NewAdaptiveCache(arms, bandit.NewThompson(0.7, 13), settings)
						require.NoError(t, err)
						t.Cleanup(func() { require.NoError(t, cache.Close()) })
						record.HitRate.Runs = append(record.HitRate.Runs, bench.Replay("tuning", cache, workload).HitRate()*100)
						require.NoError(t, cache.Close())
					}
					t.Logf("%s %d epochs %s gates=%v: %s", record.Trace, epochs, record.Strategy, gates, record.HitRate)
					records = append(records, record)
				}
			}
		}
	}
	require.Len(t, records, 12)
	if path := os.Getenv("AS_CACHE_EVIDENCE_OUT"); path != "" {
		data, err := json.MarshalIndent(records, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(strings.TrimSuffix(path, ".json")+"-tuning.json", append(data, '\n'), 0o600))
	}
}
