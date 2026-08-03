package bandit

import (
	"math"
	"math/rand/v2"
	"slices"

	ascache "github.com/sshaplygin/as-cache"
)

// weighted is one arm's pooled evidence after the window has been discounted.
// It is fractional because the weighting is, which is also why it is not
// ascache.PolicyStats.
type weighted struct {
	Hits   float64
	Misses float64
}

func (w weighted) total() float64 { return w.Hits + w.Misses }

func (w weighted) hitRate() float64 {
	if w.total() == 0 {
		return 0
	}

	return w.Hits / w.total()
}

// aggregate pools a window of per-bucket fleet counts into one posterior per
// arm, weighting each bucket by decay raised to its age relative to newest.
//
// Ageing is done here rather than in the store because the store has many
// writers. Decaying a shared counter in place would apply the multiplication
// once per replica per epoch, so a fleet of fifty would forget fifty times
// faster than a fleet of one, and the same configuration would mean something
// different at every scale. Plain sums in the store, weighting on read, and
// the arithmetic is identical whatever the fleet size.
func aggregate(
	window []WindowCounts,
	newest Bucket,
	decay float64,
	mode EvidenceMode,
) map[ascache.PolicyType]weighted {
	pooled := make(map[ascache.PolicyType]weighted)

	for _, bucket := range window {
		age := int64(newest - bucket.Bucket)
		if age < 0 {
			// A bucket newer than the reference is still being written by the
			// rest of the fleet, so it is a partial count that would weight
			// whichever replicas happen to have arrived already.
			continue
		}

		weight := math.Pow(decay, float64(age))
		if weight == 0 {
			continue
		}

		for key, stats := range bucket.Arms {
			if mode == EvidenceShadowOnly && key.Role == RoleActive {
				continue
			}

			arm := pooled[key.Policy]
			arm.Hits += weight * float64(stats.Hits)
			arm.Misses += weight * float64(stats.Misses)
			pooled[key.Policy] = arm
		}
	}

	return pooled
}

// capEvidence scales each arm's counts down to at most maxEvidence
// observations, preserving its hit rate exactly.
//
// Pooling is what makes this necessary. A Beta posterior's width shrinks with
// the square root of the evidence behind it, and a fleet supplies evidence in
// proportion to its size: a thousand replicas hand Thompson sampling a
// posterior so sharp that every draw returns the same arm, and the bandit
// stops exploring at exactly the scale where it has the most to gain from
// noticing a change. Capping the effective sample size keeps the rate estimate
// and throws away the surplus certainty.
//
// At the default cap an arm sitting at a 50% hit rate has a posterior standard
// deviation of about 0.16 points, so arms half a point apart are reliably
// separated and arms within a tenth of a point keep being explored.
func capEvidence(pooled map[ascache.PolicyType]weighted, maxEvidence float64) {
	if maxEvidence <= 0 {
		return
	}

	for policy, arm := range pooled {
		total := arm.total()
		if total <= maxEvidence {
			continue
		}

		scale := maxEvidence / total
		pooled[policy] = weighted{Hits: arm.Hits * scale, Misses: arm.Misses * scale}
	}
}

// draw picks an arm by Thompson sampling over the pooled posteriors.
//
// Arms are visited in PolicyType order and ties are broken towards the lower
// PolicyType, so the only nondeterminism is the sampling itself. Ranging the
// map directly would make a fleet's decisions depend on Go's map seed, which
// is invisible, unreproducible, and would quietly break the tie-breaking that
// keeps an unchanged fleet on an unchanged policy.
func draw(rng *rand.Rand, pooled map[ascache.PolicyType]weighted) ascache.PolicyType {
	arms := make([]ascache.PolicyType, 0, len(pooled))
	for policy := range pooled {
		arms = append(arms, policy)
	}
	slices.Sort(arms)

	best := ascache.Undefined
	bestSample := -1.0

	for _, policy := range arms {
		arm := pooled[policy]
		// The +1s are a uniform prior: an arm with no evidence is sampled
		// across the whole range rather than pinned at zero and never tried.
		sample := betaSample(rng, 1+arm.Hits, 1+arm.Misses)
		if sample > bestSample {
			best, bestSample = policy, sample
		}
	}

	return best
}
