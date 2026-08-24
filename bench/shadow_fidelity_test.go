package bench_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bench"
)

// nondeterministicArms are the two arms whose own replay does not repeat, so a
// gap between their standalone and shadow figures says nothing about the
// shadow mechanism. W-TinyLFU evicts asynchronously through otter, and Random
// seeds itself from the global source - which is the point of a random control
// arm, not a defect in it. See docs/benchmarking.md.
var nondeterministicArms = map[ascache.PolicyType]bool{
	ascache.TinyLFU: true,
	ascache.Random:  true,
}

// canonical folds a policy name and a PolicyType onto the same string, so the
// harness's display names ("W-TinyLFU", "2Q") line up with the enum's.
func canonical(name string) string {
	folded := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(name))
	switch folded {
	case "wtinylfu":
		return "tinylfu"
	case "2q":
		return "twoqueue"
	}

	return folded
}

// TestShadowsMeasureWhatThePolicyWouldActuallyServe is the regression test for
// the defect that made every shadow measurement meaningless whenever the
// active policy was performing well.
//
// A shadow exists to answer one question: what would this policy serve on this
// traffic? So its measured hit rate must equal what the same policy serves
// replayed on its own. It did not. Shadows were fed only through the caller's
// Add, and a read-through caller calls Add only when the ACTIVE policy missed -
// so a shadow could never acquire a key the incumbent was already serving, its
// contents went static behind a strong incumbent, and a static cache covering
// most of a small keyspace scores extremely well.
//
// Measured on this workload before the fix, with W-TinyLFU active: policies
// that truly serve 0.00% were reported at over 90%, and Advice() recommended
// switching from the best arm to the worst. The distortion's sign and size
// depended on which arm was incumbent, so it did not cancel in the comparison.
//
// The active arm is deliberately the strongest one available, because that is
// the condition under which the defect appears at all.
func TestShadowsMeasureWhatThePolicyWouldActuallyServe(t *testing.T) {
	const size = 500

	for _, w := range []bench.Workload{
		// A cyclic scan just over capacity: the arms are separated by ninety
		// points here, so a shadow that is wrong is unmistakably wrong.
		bench.Loop(240000, 550),
		bench.Zipf(200000, 20000, 1.1, 1),
	} {
		t.Run(w.Name, func(t *testing.T) {
			standalone := map[string]float64{}
			for _, builder := range bench.FixedPolicies() {
				policy, err := builder.Build(size)
				require.NoError(t, err)
				standalone[canonical(builder.Name)] = bench.Replay(builder.Name, policy, w).HitRate()
			}

			arms, err := bench.AdaptiveArms(size)
			require.NoError(t, err)

			// The first arm becomes active. Put the strongest one there: the
			// higher the incumbent's hit rate, the fewer Adds a read-through
			// caller makes, and the defect scaled with exactly that.
			for i, arm := range arms {
				if arm.GetType() == ascache.TinyLFU {
					arms[0], arms[i] = arms[i], arms[0]

					break
				}
			}

			// ObserveOnly with a single epoch spanning the whole replay: this
			// measures the arms, and a switch would change which arm is being
			// measured in which role. No sampling, so any gap is the fan-out
			// rather than the sample.
			cache, err := ascache.NewAdaptiveCache(arms, nil, &ascache.Settings{
				EpochRequests: int64(w.Len()),
				ObserveOnly:   true,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = cache.Close() })

			for i, key := range w.Keys {
				if _, ok := cache.Get(key); !ok {
					cache.Add(key, i+1)
				}
			}

			require.Equal(t, ascache.TinyLFU, cache.ActivePolicy(),
				"the strongest arm must have stayed active for this to measure anything")

			for _, report := range cache.Advice().Reports {
				total := report.Hits + report.Misses
				require.Positive(t, total, "%s reported no measurements at all", report.Policy)

				measured := float64(report.Hits) / float64(total)
				want := standalone[canonical(report.Policy.String())]

				t.Logf("%-8s standalone %6.2f%%  shadow %6.2f%%  (%+.2f pts)",
					report.Policy, want*100, measured*100, (measured-want)*100)

				if nondeterministicArms[report.Policy] {
					continue
				}

				assert.InDelta(t, want, measured, 0.005,
					"%s serves %.2f%% replayed on its own but its shadow measured %.2f%%: "+
						"a shadow that does not reproduce its policy's own hit rate is not measuring that policy",
					report.Policy, want*100, measured*100)
			}
		})
	}
}
