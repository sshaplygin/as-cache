package bench

import (
	"math"
	"math/rand/v2"
	"sync"

	ascache "github.com/sshaplygin/as-cache"
)

// ThompsonBandit picks a policy by Thompson sampling over Beta posteriors.
//
// Each arm's hit rate is modelled as a Beta distribution updated from the
// hits and misses reported for it. Selecting means drawing one sample per arm
// and taking the best draw, so an arm is chosen in proportion to the
// probability that it is genuinely the best - which explores uncertain arms
// without ever committing to a fixed exploration schedule.
//
// Evidence is discounted as it ages. Without that the posteriors only sharpen,
// and an arm that was best over the first ten thousand requests keeps winning
// long after the workload has moved on. Discounting is what makes the bandit
// able to change its mind, which is the entire point on a shifting workload.
//
// The as-cache module deliberately ships no Bandit implementation, so this
// exists here to make the benchmarks runnable. It is small enough to copy.
type ThompsonBandit struct {
	mu sync.Mutex
	// hits and misses hold the discounted evidence per arm.
	hits   map[ascache.PolicyType]float64
	misses map[ascache.PolicyType]float64
	// discount multiplies existing evidence at each update, in (0,1]. A value
	// of 1 never forgets.
	discount float64
	rng      *rand.Rand
}

// NewThompsonBandit returns a bandit that discounts prior evidence by the
// given factor on each report. A discount of 1 never forgets; 0.7 is a
// reasonable starting point for a workload expected to change.
func NewThompsonBandit(discount float64, seed uint64) *ThompsonBandit {
	if discount <= 0 || discount > 1 {
		discount = 1
	}

	return &ThompsonBandit{
		hits:     map[ascache.PolicyType]float64{},
		misses:   map[ascache.PolicyType]float64{},
		discount: discount,
		rng:      newRNG(seed),
	}
}

// RecordStats folds one policy's epoch result into its posterior.
func (b *ThompsonBandit) RecordStats(stats ascache.ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.hits[stats.Policy] = b.hits[stats.Policy]*b.discount + float64(stats.Hits)
	b.misses[stats.Policy] = b.misses[stats.Policy]*b.discount + float64(stats.Misses)
}

// SelectPolicy draws one sample from each arm's posterior and returns the arm
// with the highest draw.
func (b *ThompsonBandit) SelectPolicy() ascache.PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()

	best := ascache.Undefined
	bestSample := -1.0

	for policy, hits := range b.hits {
		// Beta(1+hits, 1+misses): the +1s are a uniform prior, so an arm with
		// no evidence yet is sampled across the whole range rather than being
		// pinned at zero and never tried.
		sample := betaSample(b.rng, 1+hits, 1+b.misses[policy])
		if sample > bestSample {
			best, bestSample = policy, sample
		}
	}

	return best
}

// Arms returns the arms the bandit has seen, for reporting.
func (b *ThompsonBandit) Arms() []ascache.PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()

	arms := make([]ascache.PolicyType, 0, len(b.hits))
	for policy := range b.hits {
		arms = append(arms, policy)
	}

	return arms
}

// betaSample draws from Beta(a, b) as the ratio of two Gamma draws, which is
// the standard construction: if X ~ Gamma(a,1) and Y ~ Gamma(b,1) then
// X/(X+Y) ~ Beta(a,b).
func betaSample(rng *rand.Rand, a, b float64) float64 {
	x := gammaSample(rng, a)
	y := gammaSample(rng, b)
	if x+y == 0 {
		return 0
	}

	return x / (x + y)
}

// gammaSample draws from Gamma(shape, 1) using Marsaglia and Tsang's method,
// with the standard boost for shapes below 1.
func gammaSample(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		// Gamma(a) == Gamma(a+1) * U^(1/a) for a < 1.
		return gammaSample(rng, shape+1) * math.Pow(rng.Float64(), 1/shape)
	}

	d := shape - 1.0/3.0
	c := 1 / math.Sqrt(9*d)

	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v

		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// GreedyBandit always selects the arm with the best hit rate so far. It is a
// useful control: it shows what the adaptive layer achieves without any
// exploration, and it makes switching behaviour deterministic in tests.
type GreedyBandit struct {
	mu     sync.Mutex
	rates  map[ascache.PolicyType]float64
	counts map[ascache.PolicyType]int64
}

// NewGreedyBandit returns a bandit that always picks the best-measured arm.
func NewGreedyBandit() *GreedyBandit {
	return &GreedyBandit{
		rates:  map[ascache.PolicyType]float64{},
		counts: map[ascache.PolicyType]int64{},
	}
}

// RecordStats stores the arm's hit rate for the epoch just measured.
func (b *GreedyBandit) RecordStats(stats ascache.ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()

	total := stats.Hits + stats.Misses
	if total == 0 {
		return
	}
	b.rates[stats.Policy] = float64(stats.Hits) / float64(total)
	b.counts[stats.Policy] = total
}

// SelectPolicy returns the arm with the highest measured hit rate.
func (b *GreedyBandit) SelectPolicy() ascache.PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()

	best := ascache.Undefined
	bestRate := -1.0
	for policy, rate := range b.rates {
		if rate > bestRate {
			best, bestRate = policy, rate
		}
	}

	return best
}
