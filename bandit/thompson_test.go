package bandit

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ascache "github.com/sshaplygin/as-cache"
)

func feed(b ascache.Bandit, epochs int, rates map[ascache.PolicyType]float64, requests int64) {
	for range epochs {
		for policy, rate := range rates {
			hits := int64(float64(requests) * rate)
			b.RecordStats(ascache.ShadowStats{
				Policy: policy,
				Hits:   hits,
				Misses: requests - hits,
			})
		}
	}
}

func winner(b ascache.Bandit, draws int) ascache.PolicyType {
	counts := make(map[ascache.PolicyType]int)
	for range draws {
		counts[b.SelectPolicy()]++
	}

	best, bestCount := ascache.Undefined, -1
	for policy, count := range counts {
		if count > bestCount || (count == bestCount && policy < best) {
			best, bestCount = policy, count
		}
	}

	return best
}

func TestThompson_UndefinedBeforeAnyEvidence(t *testing.T) {
	// The cache reads Undefined as "no change", which is what makes this a
	// safe answer rather than a crash.
	assert.Equal(t, ascache.Undefined, NewThompson(0.7, 1).SelectPolicy())
}

func TestThompson_FavoursTheBetterArm(t *testing.T) {
	b := NewThompson(1, 42)
	feed(b, 10, map[ascache.PolicyType]float64{ascache.LRU: 0.3, ascache.TinyLFU: 0.8}, 1000)

	assert.Equal(t, ascache.TinyLFU, winner(b, 200))
}

func TestThompson_ChangesItsMindWhenTheWorkloadDoes(t *testing.T) {
	// Discounting is the whole point: without it the first ten thousand
	// requests keep deciding long after the traffic has moved on.
	b := NewThompson(0.5, 7)
	feed(b, 20, map[ascache.PolicyType]float64{ascache.LRU: 0.9, ascache.TinyLFU: 0.2}, 1000)
	assert.Equal(t, ascache.LRU, winner(b, 200))

	feed(b, 20, map[ascache.PolicyType]float64{ascache.LRU: 0.2, ascache.TinyLFU: 0.9}, 1000)
	assert.Equal(t, ascache.TinyLFU, winner(b, 200))
}

func TestThompson_NeverForgettingIsStuck(t *testing.T) {
	// The control for the test above: with a discount of 1 the old evidence
	// still outweighs the new, which is the failure discounting prevents.
	b := NewThompson(1, 7)
	feed(b, 200, map[ascache.PolicyType]float64{ascache.LRU: 0.9, ascache.TinyLFU: 0.2}, 1000)
	feed(b, 5, map[ascache.PolicyType]float64{ascache.LRU: 0.2, ascache.TinyLFU: 0.9}, 1000)

	assert.Equal(t, ascache.LRU, winner(b, 200))
}

func TestThompson_ExploresAnArmWithNoEvidence(t *testing.T) {
	// An arm nobody has measured must not be pinned at zero and never tried;
	// the uniform prior is what keeps it reachable.
	b := NewThompson(1, 3)
	feed(b, 5, map[ascache.PolicyType]float64{ascache.LRU: 0.5}, 100)
	b.RecordStats(ascache.ShadowStats{Policy: ascache.TinyLFU})

	drawn := make(map[ascache.PolicyType]int)
	for range 500 {
		drawn[b.SelectPolicy()]++
	}

	assert.Positive(t, drawn[ascache.TinyLFU], "an unmeasured arm must still be explored")
	assert.Positive(t, drawn[ascache.LRU])
}

func TestThompson_RejectsAnOutOfRangeDiscount(t *testing.T) {
	for _, discount := range []float64{0, -1, 1.5} {
		b := NewThompson(discount, 1)
		assert.InDelta(t, 1.0, b.discount, 1e-9, "an out-of-range discount falls back to never forgetting")
	}
}

func TestThompson_ArmsReportsWhatItHasSeen(t *testing.T) {
	b := NewThompson(1, 1)
	feed(b, 1, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.LFU: 0.5}, 10)

	assert.ElementsMatch(t, []ascache.PolicyType{ascache.LRU, ascache.LFU}, b.Arms())
}

func TestGreedy_TakesTheBestMeasuredArm(t *testing.T) {
	b := NewGreedy()
	feed(b, 1, map[ascache.PolicyType]float64{ascache.LRU: 0.3, ascache.TinyLFU: 0.8}, 1000)

	assert.Equal(t, ascache.TinyLFU, b.SelectPolicy())
}

func TestGreedy_UndefinedBeforeAnyEvidence(t *testing.T) {
	assert.Equal(t, ascache.Undefined, NewGreedy().SelectPolicy())
}

func TestGreedy_QuietEpochLeavesThePreviousRateAlone(t *testing.T) {
	// An arm that saw no requests this epoch has not become bad, it has just
	// not been measured. Scoring it zero would evict it from contention on the
	// strength of no evidence at all.
	b := NewGreedy()
	feed(b, 1, map[ascache.PolicyType]float64{ascache.LRU: 0.3, ascache.TinyLFU: 0.8}, 1000)

	b.RecordStats(ascache.ShadowStats{Policy: ascache.TinyLFU})
	assert.Equal(t, ascache.TinyLFU, b.SelectPolicy())
}

func TestGreedy_TieBreaksDeterministically(t *testing.T) {
	// Ranging a map alone would let equally-performing arms swap places on
	// every call, so an unchanged cache would keep switching for no reason.
	for range 50 {
		b := NewGreedy()
		feed(b, 1, map[ascache.PolicyType]float64{
			ascache.LRU:      0.5,
			ascache.LFU:      0.5,
			ascache.TwoQueue: 0.5,
			ascache.TinyLFU:  0.5,
		}, 100)

		assert.Equal(t, ascache.LRU, b.SelectPolicy())
	}
}
