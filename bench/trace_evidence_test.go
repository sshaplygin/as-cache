package bench_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
	"github.com/sshaplygin/as-cache/bench"
)

// traceEvidenceRuns is how many times each subject whose result is not
// reproducible is replayed. The adaptive cache samples its shadows through a
// hash seeded afresh for every cache, and two of the arms are not
// deterministic, so a single replay is one draw, not a measurement.
const traceEvidenceRuns = 5

// traceEvidenceEpochs are the epoch lengths the adaptive cache is replayed at,
// given as the number of epochs over the whole trace so that a 100k-request
// trace and a 2M-request one are both re-evaluated the same number of times.
// All three are reported: the result depends on the choice, and publishing
// only the best of them would hide that.
var traceEvidenceEpochs = []int{10, 20, 50}

// traceEvidenceSettings is the configuration the adaptive cache is replayed
// with: request-counted epochs, so the number of epochs does not depend on how
// fast the machine is. The sampling floor is an experimental choice, not the default.
func traceEvidenceSettings(epochRequests int64) *ascache.Settings {
	return &ascache.Settings{
		EpochRequests:               epochRequests,
		EvictPartialCapacityFilling: true,
		MigrationStrategy:           ascache.MigrationWarm,
		ShadowSampleRate:            0.05,
		MinShadowCapacity:           64,
	}
}

// nondeterministicArm names the arms whose replay differs between runs: Random
// seeds itself from the global source, and W-TinyLFU evicts asynchronously.
func nondeterministicArm(name string) bool {
	return name == "Random" || name == "W-TinyLFU"
}

// spread is one subject's hit rates, in percent, over repeated replays.
type spread struct {
	Runs []float64 `json:"runs"`
}

func (s spread) sorted() []float64 {
	out := append([]float64(nil), s.Runs...)
	sort.Float64s(out)

	return out
}

func (s spread) median() float64 {
	v := s.sorted()
	if len(v) == 0 {
		return math.NaN()
	}
	// Standard even-count median: the mean of the two central observations.
	if len(v)%2 == 1 {
		return v[len(v)/2]
	}

	return (v[len(v)/2-1] + v[len(v)/2]) / 2
}

func (s spread) String() string {
	v := s.sorted()
	if len(v) == 0 {
		return "n/a"
	}
	if v[0] == v[len(v)-1] {
		return fmt.Sprintf("%.2f%%", v[0])
	}

	return fmt.Sprintf("%.2f%% [%.2f-%.2f]", s.median(), v[0], v[len(v)-1])
}

type adaptiveRecord struct {
	EpochsPerTrace int    `json:"epochs_per_trace"`
	EpochRequests  int64  `json:"epoch_requests"`
	HitRate        spread `json:"hit_rate_percent"`
}

type traceRecord struct {
	Trace               string            `json:"trace"`
	Source              string            `json:"source"`
	Requests            int               `json:"requests"`
	Distinct            int               `json:"distinct_keys"`
	Capacity            int               `json:"capacity"`
	Fixed               map[string]spread `json:"fixed_hit_rate_percent"`
	Adaptive            []adaptiveRecord  `json:"adaptive"`
	EffectiveSampleRate float64           `json:"effective_sample_rate"`
	Observe             []observeRun      `json:"observe_only"`
	Diagnostic          sieveDiagnostic   `json:"lfu_sieve_diagnostic"`
}

// bestAndWorst returns the fixed policies with the highest and lowest median
// hit rate.
func (r traceRecord) bestAndWorst() (best, worst string) {
	names := make([]string, 0, len(r.Fixed))
	for name := range r.Fixed {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", ""
	}

	best, worst = names[0], names[0]
	for _, name := range names {
		if r.Fixed[name].median() > r.Fixed[best].median() {
			best = name
		}
		if r.Fixed[name].median() < r.Fixed[worst].median() {
			worst = name
		}
	}

	return best, worst
}

// TestTraceEvidence is the real-workload counterpart to TestAdaptiveVersusFixed.
// The synthetic result rests on workloads chosen by the author of the library,
// which is exactly the kind of evidence that should not be trusted on its own.
//
// Every fixed policy is replayed on its own, the two non-deterministic ones
// traceEvidenceRuns times. The adaptive cache, holding all nine arms, is
// replayed traceEvidenceRuns times at each of traceEvidenceEpochs. Set
// AS_CACHE_EVIDENCE_OUT to a file path to keep every run, with the commit it
// was measured at, as JSON.
func TestTraceEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	var records []traceRecord

	for _, found := range loadKnownTraces(t) {
		spec, w := found.spec, found.workload

		t.Run(w.Name, func(t *testing.T) {
			record := traceRecord{
				Trace:    w.Name,
				Source:   spec.source,
				Requests: len(w.Keys),
				Distinct: bench.DistinctKeys(w),
				Capacity: spec.cache,
				Fixed:    map[string]spread{},
			}
			t.Logf("\n%s\n%s\n%s\ncache %d entries, %.1f%% of the %d distinct keys",
				w.Name, spec.source, w.Description, spec.cache,
				float64(spec.cache)/float64(record.Distinct)*100, record.Distinct)

			for _, builder := range bench.FixedPolicies() {
				runs := 1
				if nondeterministicArm(builder.Name) {
					runs = traceEvidenceRuns
				}

				var s spread
				for range runs {
					policy, err := builder.Build(spec.cache)
					require.NoError(t, err)
					s.Runs = append(s.Runs, bench.Replay(builder.Name, policy, w).HitRate()*100)
				}
				record.Fixed[builder.Name] = s
			}

			record.EffectiveSampleRate = math.Min(1, math.Max(0.05, 64.0/float64(spec.cache)))
			record.Diagnostic = diagnoseSieve(t, spec.cache, w)
			record.Observe = observeTrace(t, spec.cache, w)
			for _, epochs := range traceEvidenceEpochs {
				epochRequests := int64(len(w.Keys) / epochs)

				var s spread
				for range traceEvidenceRuns {
					arms, err := bench.AdaptiveArms(spec.cache)
					require.NoError(t, err)

					cache, err := ascache.NewAdaptiveCache(arms, bandit.NewThompson(0.7, 13),
						traceEvidenceSettings(epochRequests))
					require.NoError(t, err)

					t.Cleanup(func() { require.NoError(t, cache.Close()) })
					s.Runs = append(s.Runs, bench.Replay("adaptive", cache, w).HitRate()*100)
					require.NoError(t, cache.Close())
				}
				record.Adaptive = append(record.Adaptive, adaptiveRecord{epochs, epochRequests, s})
			}

			t.Logf("\n%s", traceRecordTable(record))

			// Relative performance is an observation, never an acceptance gate.
			// Retain below-baseline results too; filtering them biases the evidence.

			records = append(records, record)
		})
	}

	t.Logf("\n%s", traceSummaryTable(records))
	writeTraceEvidence(t, records)
}

// traceRecordTable renders one trace's results, best median first.
func traceRecordTable(r traceRecord) string {
	type row struct {
		name   string
		median float64
		cell   string
	}

	rows := make([]row, 0, len(r.Fixed)+len(r.Adaptive))
	for name, s := range r.Fixed {
		rows = append(rows, row{name, s.median(), s.String()})
	}
	for _, a := range r.Adaptive {
		rows = append(rows, row{
			fmt.Sprintf("adaptive, %d epochs (every %d requests)", a.EpochsPerTrace, a.EpochRequests),
			a.HitRate.median(), a.HitRate.String(),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].median > rows[j].median })

	var b strings.Builder
	b.WriteString("| Subject | Hit rate, median [min-max] |\n| --- | --- |\n")
	for _, row := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", row.name, row.cell)
	}

	return b.String()
}

// traceSummaryTable renders one row per trace: the best and worst fixed policy
// by median, and the adaptive cache at each epoch length with its distance
// from the best.
func traceSummaryTable(records []traceRecord) string {
	var b strings.Builder

	b.WriteString("| Trace | Requests | Best fixed | Worst fixed |")
	for _, epochs := range traceEvidenceEpochs {
		fmt.Fprintf(&b, " Adaptive, %d epochs |", epochs)
	}
	b.WriteString("\n| --- | --- | --- | --- |" + strings.Repeat(" --- |", len(traceEvidenceEpochs)) + "\n")

	for _, r := range records {
		best, worst := r.bestAndWorst()
		fmt.Fprintf(&b, "| %s | %d | %s %s | %s %s |", r.Trace, r.Requests,
			best, r.Fixed[best], worst, r.Fixed[worst])
		for _, a := range r.Adaptive {
			fmt.Fprintf(&b, " %s (%+.2f) |", a.HitRate, a.HitRate.median()-r.Fixed[best].median())
		}
		b.WriteString("\n")
	}

	return b.String()
}

// writeTraceEvidence keeps every run as JSON when AS_CACHE_EVIDENCE_OUT names a
// file, together with what is needed to reproduce it: the commit and whether
// the tree was clean, the Go version and platform, and the settings.
func writeTraceEvidence(t *testing.T, records []traceRecord) {
	t.Helper()

	path := os.Getenv("AS_CACHE_EVIDENCE_OUT")
	if path == "" {
		return
	}

	commit, err := exec.Command("git", "rev-parse", "HEAD").Output()
	require.NoError(t, err, "the results file must name the commit it was measured at")
	status, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output()
	require.NoError(t, err)

	out := map[string]any{
		"commit":        strings.TrimSpace(string(commit)),
		"tree_modified": strings.TrimSpace(string(status)) != "",
		"measured_at":   time.Now().UTC().Format(time.RFC3339),
		"go":            runtime.Version(),
		"platform":      runtime.GOOS + "/" + runtime.GOARCH,
		"cpus":          runtime.NumCPU(),
		"runs":          traceEvidenceRuns,
		"settings": map[string]any{
			"epoch_mode": "requests", "epochs_per_trace": traceEvidenceEpochs,
			"EvictPartialCapacityFilling": true, "MigrationStrategy": "warm",
			"ShadowSampleRate": 0.05, "MinShadowCapacity": 64,
		},
		"bandit": "bandit.NewThompson(0.7, 13)",
		"traces": records,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
	t.Logf("wrote %s", path)
}
