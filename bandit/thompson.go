package bandit

import (
	"math/rand/v2"
	"sync"

	ascache "github.com/sshaplygin/as-cache"
)

var _ ascache.Bandit = (*Thompson)(nil)

// Thompson picks a policy by Thompson sampling over Beta posteriors.
//
// Each arm's hit rate is modelled as a Beta distribution updated from the hits
// and misses reported for it. Selecting means drawing one sample per arm and
// taking the best draw, so an arm is chosen in proportion to the probability
// that it is genuinely the best - which explores uncertain arms without ever
// committing to a fixed exploration schedule.
//
// Evidence is discounted as it ages. Without that the posteriors only sharpen,
// and an arm that was best over the first ten thousand requests keeps winning
// long after the workload has moved on. Discounting is what makes the bandit
// able to change its mind, which is the entire point on a shifting workload.
type Thompson struct {
	mu sync.Mutex
	// hits and misses hold the discounted evidence per arm.
	hits   map[ascache.PolicyType]float64
	misses map[ascache.PolicyType]float64
	// discount multiplies existing evidence at each update, in (0,1]. A value
	// of 1 never forgets.
	discount float64
	rng      *rand.Rand
}

// NewThompson returns a bandit that discounts prior evidence by the given
// factor on each report. A discount of 1 never forgets; 0.7 is a reasonable
// starting point for a workload expected to change. A discount outside (0,1]
// is treated as 1.
func NewThompson(discount float64, seed uint64) *Thompson {
	if discount <= 0 || discount > 1 {
		discount = 1
	}

	return &Thompson{
		hits:     map[ascache.PolicyType]float64{},
		misses:   map[ascache.PolicyType]float64{},
		discount: discount,
		//nolint:gosec // deliberate: a seeded, reproducible source, not a secret
		rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
	}
}

// RecordStats folds one policy's epoch result into its posterior.
func (b *Thompson) RecordStats(stats ascache.ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.recordLocked(stats)
}

func (b *Thompson) recordLocked(stats ascache.ShadowStats) {
	b.hits[stats.Policy] = b.hits[stats.Policy]*b.discount + float64(stats.Hits)
	b.misses[stats.Policy] = b.misses[stats.Policy]*b.discount + float64(stats.Misses)
}

// SelectPolicy draws one sample from each arm's posterior and returns the arm
// with the highest draw. It returns [ascache.Undefined] before any arm has
// reported, which the cache reads as "no change".
func (b *Thompson) SelectPolicy() ascache.PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()

	best := ascache.Undefined
	bestSample := -1.0

	for policy, hits := range b.hits {
		// Beta(1+hits, 1+misses): the +1s are a uniform prior, so an arm with
		// no evidence yet is sampled across the whole range rather than being
		// pinned at zero and never tried.
		sample := betaSample(b.rng, 1+hits, 1+b.misses[policy])
		if sample > bestSample || (sample == bestSample && policy < best) {
			best, bestSample = policy, sample
		}
	}

	return best
}

// Arms returns the arms the bandit has seen, in no particular order.
func (b *Thompson) Arms() []ascache.PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()

	arms := make([]ascache.PolicyType, 0, len(b.hits))
	for policy := range b.hits {
		arms = append(arms, policy)
	}

	return arms
}

var _ ascache.Bandit = (*Greedy)(nil)

// Greedy always selects the arm with the best hit rate so far. It is a useful
// control: it shows what the adaptive layer achieves without any exploration,
// and it makes switching behaviour deterministic in tests.
type Greedy struct {
	mu    sync.Mutex
	rates map[ascache.PolicyType]float64
}

// NewGreedy returns a bandit that always picks the best-measured arm.
func NewGreedy() *Greedy {
	return &Greedy{rates: map[ascache.PolicyType]float64{}}
}

// RecordStats stores the arm's hit rate for the epoch just measured. An epoch
// in which an arm saw no requests leaves its previous rate in place rather
// than scoring it zero.
func (b *Greedy) RecordStats(stats ascache.ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()

	total := stats.Hits + stats.Misses
	if total == 0 {
		return
	}
	b.rates[stats.Policy] = float64(stats.Hits) / float64(total)
}

// SelectPolicy returns the arm with the highest measured hit rate, ties broken
// by PolicyType so the answer does not depend on map iteration order.
func (b *Greedy) SelectPolicy() ascache.PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()

	best := ascache.Undefined
	bestRate := -1.0
	for policy, rate := range b.rates {
		if rate > bestRate || (rate == bestRate && policy < best) {
			best, bestRate = policy, rate
		}
	}

	return best
}
