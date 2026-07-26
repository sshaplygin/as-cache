package bench_test

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bench"
)

// cacheSize is small relative to each workload's keyspace, so the policies are
// actually forced to choose what to keep. A cache large enough to hold the
// working set makes every policy look identical.
const cacheSize = 500

// workloads returns the suite every comparison runs over.
func workloads() []bench.Workload {
	return []bench.Workload{
		bench.Zipf(200000, 20000, 1.1, 1),
		bench.Uniform(200000, 5000, 2),
		// Working set just over capacity: the classic LRU pathology.
		bench.Loop(200000, cacheSize+50),
		bench.Scan(200, 100, 400, 600),
		bench.PhaseShift(20, 10000, 20000, cacheSize+50, 3),
	}
}

// runFixed measures every shipped policy on a workload.
func runFixed(t *testing.T, w bench.Workload) []bench.Result {
	t.Helper()

	results := make([]bench.Result, 0, len(bench.FixedPolicies()))
	for _, builder := range bench.FixedPolicies() {
		policy, err := builder.Build(cacheSize)
		require.NoError(t, err, "build %s", builder.Name)
		results = append(results, bench.Replay(builder.Name, policy, w))
	}

	return results
}

// TestFixedPolicyEvidence reports what each policy achieves on each workload.
// It asserts the properties these workloads exist to demonstrate; the printed
// tables are the artifact.
func TestFixedPolicyEvidence(t *testing.T) {
	if testing.Short() {
		// These replay millions of requests through timing-driven epochs.
		// They are evidence, not correctness checks, and under -race the
		// epoch pacing changes enough to make them meaningless.
		t.Skip("evidence run; use make evidence")
	}

	for _, w := range workloads() {
		t.Run(w.Name, func(t *testing.T) {
			results := runFixed(t, w)

			t.Logf("\n%s (%d requests, cache %d)\n%s\n%s",
				w.Name, w.Len(), cacheSize, w.Description, bench.Table(results))

			byPolicy := map[string]bench.Result{}
			for _, r := range results {
				byPolicy[r.Policy] = r
			}

			switch w.Name {
			case "loop":
				// Every key is evicted exactly before it is needed again.
				assert.Less(t, byPolicy["LRU"].HitRate(), 0.05,
					"a cyclic scan just over capacity should defeat LRU almost completely")
				assert.Greater(t, byPolicy["Random"].HitRate(), byPolicy["LRU"].HitRate(),
					"random eviction should beat LRU on its pathological case")

			case "scan":
				assert.Greater(t, byPolicy["W-TinyLFU"].HitRate(), byPolicy["LRU"].HitRate(),
					"frequency-based admission should hold the hot set through sweeps that flush LRU")

			case "uniform":
				// With no reuse structure, nothing can do much better than
				// anything else; bookkeeping earns nothing.
				spread := byPolicy["W-TinyLFU"].HitRate() - byPolicy["Random"].HitRate()
				assert.Less(t, spread, 0.10,
					"on a workload with no structure, a sophisticated policy should not beat random by much")
			}
		})
	}
}

// TestAdaptiveVersusFixed is the question the whole library exists to answer:
// does choosing a policy at runtime beat committing to one up front?
//
// The honest comparison is against the best fixed policy per workload, not
// against LRU. Adaptive selection is only worth its overhead if it tracks the
// best arm without knowing in advance which that is.
func TestAdaptiveVersusFixed(t *testing.T) {
	if testing.Short() {
		// These replay millions of requests through timing-driven epochs.
		// They are evidence, not correctness checks, and under -race the
		// epoch pacing changes enough to make them meaningless.
		t.Skip("evidence run; use make evidence")
	}

	var rows []summaryRow

	for _, w := range workloads() {
		t.Run(w.Name, func(t *testing.T) {
			fixed := runFixed(t, w)

			arms, err := bench.AdaptiveArms(cacheSize)
			require.NoError(t, err)

			cache, err := ascache.NewAdaptiveCache(arms,
				bench.NewThompsonBandit(0.7, 7),
				&ascache.Settings{
					// Short enough that many epochs elapse during a replay, so
					// the bandit gets the chance to react within a phase.
					EpochDuration:               2 * time.Millisecond,
					EvictPartialCapacityFilling: true,
					MigrationStrategy:           ascache.MigrationWarm,
				})
			require.NoError(t, err)
			t.Cleanup(func() { _ = cache.Close() })

			adaptive := bench.Replay("adaptive", cache, w)

			all := append([]bench.Result{}, fixed...)
			all = append(all, adaptive)
			t.Logf("\n%s vs fixed policies\n%s", w.Name, bench.Table(all))

			bestFixed, worstFixed := fixed[0], fixed[0]
			for _, r := range fixed {
				if r.HitRate() > bestFixed.HitRate() {
					bestFixed = r
				}
				if r.HitRate() < worstFixed.HitRate() {
					worstFixed = r
				}
			}

			rows = append(rows, summaryRow{w.Name, adaptive.HitRate(), bestFixed.Policy,
				bestFixed.HitRate(), worstFixed.HitRate()})

			// The claim worth defending is not that adaptive always wins, but
			// that it never lands near the bottom: a cache that can pick the
			// worst arm is worse than any fixed choice.
			assert.Greater(t, adaptive.HitRate(), worstFixed.HitRate(),
				"adaptive selection must beat the worst fixed policy on %s", w.Name)
		})
	}

	t.Log("\n" + summarise(rows))
}

// summaryRow is one workload's line in the headline comparison.
type summaryRow struct {
	workload  string
	adaptive  float64
	best      string
	bestRate  float64
	worstRate float64
}

func summarise(rows []summaryRow) string {
	out := "| Workload | Adaptive | Best fixed | Worst fixed | Adaptive vs best |\n| --- | --- | --- | --- | --- |\n"
	for _, r := range rows {
		out += fmt.Sprintf("| %s | %.2f%% | %s %.2f%% | %.2f%% | %+.2f pts |\n",
			r.workload, r.adaptive*100, r.best, r.bestRate*100, r.worstRate*100,
			(r.adaptive-r.bestRate)*100)
	}

	return out
}

// TestSamplingPreservesPolicyRanking settles the debt Milestone 2 left behind.
//
// Sampled shadows only help if a 5% miniature ranks policies the way full-size
// shadows would. That rests on the miniature-simulation result, which is
// established for stack algorithms under stationary workloads and shakier for
// policies whose bookkeeping is sized in absolute terms - 2Q's ghost queues,
// ARC's list balance, W-TinyLFU's admission sketch. If the ranking inverts,
// sampling silently makes the bandit choose wrong while everything still looks
// healthy, so this measures it rather than trusting the theory.
func TestSamplingPreservesPolicyRanking(t *testing.T) {
	if testing.Short() {
		// These replay millions of requests through timing-driven epochs.
		// They are evidence, not correctness checks, and under -race the
		// epoch pacing changes enough to make them meaningless.
		t.Skip("evidence run; use make evidence")
	}

	// Large enough that even a 5% miniature clears the MinShadowCapacity floor
	// and is still a real cache, small enough that the arms genuinely differ:
	// if every arm holds the whole working set they all score the same and
	// there is no ranking to preserve.
	const size = 8000

	// The rates a user would plausibly choose. 0.05 is the aggressive end this
	// library recommends; the rest exist so the cost of the choice is visible
	// rather than assumed.
	rates := []float64{0.05, 0.10, 0.30, 0.50}

	for _, w := range []bench.Workload{
		bench.Zipf(300000, 200000, 1.1, 11),
		bench.Scan(30, 4000, 16000, 40000),
	} {
		t.Run(w.Name, func(t *testing.T) {
			// The ground truth every sampled run is judged against.
			full := ratesUnderSampling(t, w, size, 0)
			bestFull := argmax(full)

			t.Logf("\n%s\n  full-size shadows  %s\n  -> picks %s",
				w.Name, formatRates(full), bestFull)

			for _, rate := range rates {
				sampled := ratesUnderSampling(t, w, size, rate)
				bestSampled := argmax(sampled)

				// Regret is the property that matters, not the argmax: arms
				// within noise of each other reorder run to run even with no
				// sampling at all, because epoch boundaries fall differently.
				// What counts is whether the arm sampling picks is actually
				// worse, judged by the full-size rates.
				regret := full[bestFull] - full[bestSampled]

				t.Logf("  rate %.2f          %s\n  -> picks %s, regret %.2f pts",
					rate, formatRates(sampled), bestSampled, regret*100)

				assert.Less(t, regret, 0.03,
					"at rate %.2f sampling picked %s (true rate %.2f%%) over %s (%.2f%%): %.2f points of regret on %s",
					rate, bestSampled, full[bestSampled]*100, bestFull, full[bestFull]*100,
					regret*100, w.Name)
			}
		})
	}
}

// TestSamplingPreservesClearOrderings checks the property that actually makes
// a sampled comparison trustworthy.
//
// It is tempting to assume sampling costs every arm the same few points, so
// that the shortfall cancels in the comparison. That is not what happens.
// Measured across rates, an arm's absolute rate can land either side of its
// full-size figure - at rate 0.10 on zipf the sampled rates come out several
// points HIGHER - because the estimate depends on which slice of the keyspace
// the seed selected, and a different slice has different reuse.
//
// So absolute sampled rates are not a prediction of the full-size rate, and
// nothing here should claim they are. What survives is the ORDERING: when two
// arms are genuinely separated, sampling ranks them the same way. That is
// sufficient, because the bandit only ever needs to know which arm is better.
func TestSamplingPreservesClearOrderings(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	const size = 8000

	// Pairs closer than this are within measurement noise and reorder run to
	// run even with no sampling at all, so they carry no ordering to preserve.
	const separated = 0.05

	for _, w := range []bench.Workload{
		bench.Zipf(300000, 200000, 1.1, 11),
		bench.Scan(30, 4000, 16000, 40000),
	} {
		t.Run(w.Name, func(t *testing.T) {
			full := ratesUnderSampling(t, w, size, 0)

			for _, rate := range []float64{0.05, 0.10, 0.30, 0.50} {
				sampled := ratesUnderSampling(t, w, size, rate)

				compared, inverted := 0, 0
				for a, rateA := range full {
					for b, rateB := range full {
						if a >= b || rateA-rateB < separated {
							continue
						}
						// a is clearly better than b at full size.
						compared++
						if sampled[a] < sampled[b] {
							inverted++
							t.Errorf("rate %.2f inverted a clear ordering on %s: "+
								"%s beats %s by %.2f points at full size, but sampling ranks %s higher",
								rate, w.Name, a, b, (rateA-rateB)*100, b)
						}
					}
				}

				t.Logf("rate %.2f: %d clearly separated pairs, %d inverted", rate, compared, inverted)
				assert.Positive(t, compared,
					"the workload must separate some arms or this proves nothing")
			}
		})
	}
}

// argmax returns the name with the highest rate.
func argmax(rates map[string]float64) string {
	best, bestRate := "", -1.0
	for name, rate := range rates {
		if rate > bestRate {
			best, bestRate = name, rate
		}
	}

	return best
}

func formatRates(rates map[string]float64) string {
	names := make([]string, 0, len(rates))
	for name := range rates {
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool { return rates[names[i]] > rates[names[j]] })

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%.2f%%", name, rates[name]*100))
	}

	return strings.Join(parts, " ")
}

// ratesUnderSampling measures every arm inside one AdaptiveCache at the given
// sample rate and returns each arm's measured hit rate.
func ratesUnderSampling(t *testing.T, w bench.Workload, size int, rate float64) map[string]float64 {
	t.Helper()

	arms, err := bench.AdaptiveArms(size)
	require.NoError(t, err)

	recorder := &rankingBandit{}

	cache, err := ascache.NewAdaptiveCache(arms, recorder, &ascache.Settings{
		EpochDuration:               5 * time.Millisecond,
		EvictPartialCapacityFilling: true,
		ShadowSampleRate:            rate,
		MinShadowCapacity:           256,
		// Hold the active policy still: this measures the arms, and a switch
		// would change which arm is being measured in which role.
		SwitchCooldownEpochs: 1 << 30,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	bench.Replay("ranking", cache, w)

	return recorder.rates()
}

// rankingBandit accumulates each arm's reported hits and misses and never
// switches, so a run measures every arm in a fixed role.
type rankingBandit struct {
	mu     sync.Mutex
	hits   map[ascache.PolicyType]int64
	misses map[ascache.PolicyType]int64
}

func (b *rankingBandit) RecordStats(stats ascache.ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.hits == nil {
		b.hits, b.misses = map[ascache.PolicyType]int64{}, map[ascache.PolicyType]int64{}
	}
	b.hits[stats.Policy] += stats.Hits
	b.misses[stats.Policy] += stats.Misses
}

func (b *rankingBandit) SelectPolicy() ascache.PolicyType {
	// Never switch: whichever arm is active stays active.
	return ascache.Undefined
}

func (b *rankingBandit) rates() map[string]float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make(map[string]float64, len(b.hits))
	for policy, hits := range b.hits {
		total := hits + b.misses[policy]
		if total == 0 {
			continue
		}
		out[policy.String()] = float64(hits) / float64(total)
	}

	return out
}
