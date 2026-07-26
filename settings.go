package ascache

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// Settings configures the behaviour of AdaptiveCache.
type Settings struct {
	EpochDuration time.Duration
	// EvictPartialCapacityFilling allows policy switching even when the cache
	// is not yet full.
	EvictPartialCapacityFilling bool
	// MigrationStrategy determines how data is moved when the active policy
	// changes. Defaults to MigrationCold (zero value).
	MigrationStrategy MigrationStrategy

	// MinHitRateImprovement is the hit-rate advantage, as an absolute
	// difference in [0,1], that the bandit's selection must hold over the
	// active policy in the epoch just measured before the switch is applied.
	// It damps oscillation between policies that perform almost identically.
	// Zero (the default) applies every selection the bandit makes.
	MinHitRateImprovement float64

	// SwitchCooldownEpochs is the number of epochs that must elapse after a
	// policy switch before another switch is allowed, counted from the last
	// switch or from cache creation if none has happened yet. Zero (the
	// default) allows a switch on every epoch.
	SwitchCooldownEpochs int64

	// MinEpochRequests is the number of requests (hits plus misses) both the
	// active policy and the candidate must have observed in the measured
	// epoch before a switch is allowed, so the cache does not react to a
	// handful of samples. Zero (the default) imposes no minimum.
	//
	// The requests counted are the ones the bandit sees, which under
	// ShadowSampleRate means sampled requests: at a rate of 0.05 a threshold
	// of 100 is reached after roughly 2000 real requests.
	MinEpochRequests int64

	// ShadowSampleRate is the fraction of the keyspace, in (0,1], that shadow
	// policies track. Shadows exist only to estimate a hit rate, and a hit
	// rate can be estimated from a sample: at 0.05 a shadow skips 95% of the
	// operations it would otherwise mirror, which is where the bulk of the
	// adaptive layer's overhead goes.
	//
	// Shadows shrink with the rate so each remains a faithful miniature of a
	// full-size cache, and every shadow samples the same keys so their hit
	// rates stay comparable. The active policy still serves every key; only
	// the measurement is sampled, and it is sampled for the active policy too
	// so that all arms carry equally weighted evidence.
	//
	// Zero (the default) means 1: shadows mirror every key, which is the
	// behaviour of earlier versions.
	ShadowSampleRate float64

	// ObserveOnly runs the cache as a measurement instrument: every policy is
	// still measured each epoch and reported to the bandit, but the active
	// policy never changes and no migration ever happens.
	//
	// This is the zero-risk way to adopt the library. The cache behaves
	// exactly like the single policy you gave it first, while Advice() answers
	// the question that is otherwise expensive to ask: would a different
	// eviction policy serve this traffic better, and by how much. Once the
	// answer is in, either switch to that policy directly or turn this off and
	// let the bandit do it.
	ObserveOnly bool

	// MinShadowCapacity is the floor on a shadow's miniature capacity. A
	// miniature of a handful of entries measures noise rather than a policy,
	// so when the sample rate would shrink a shadow below this floor the
	// effective rate is raised instead, up to the point where sampling
	// disables itself entirely. Zero (the default) applies
	// DefaultMinShadowCapacity.
	MinShadowCapacity int
}

// DefaultMinShadowCapacity is the miniature capacity floor applied when
// Settings.MinShadowCapacity is zero.
const DefaultMinShadowCapacity = 256

// NewAdaptiveCache validates its inputs and starts the background epoch
// goroutine. Callers must call Close to stop that goroutine.
func NewAdaptiveCache[K comparable, V any](
	policies []Policy[K, V],
	bandit Bandit,
	settings *Settings,
) (*AdaptiveCache[K, V], error) {
	if len(policies) == 0 {
		return nil, ErrEmptyPolicies
	}
	if settings == nil {
		return nil, ErrNilSettings
	}
	if bandit == nil {
		// Observing needs no strategy: nothing is ever selected. Requiring a
		// bandit for the zero-risk adoption path would be friction for no
		// reason, since implementing one is the fiddliest part of using this
		// library.
		if !settings.ObserveOnly {
			return nil, ErrNilBandit
		}
		bandit = observerBandit{}
	}
	if settings.EpochDuration <= 0 {
		return nil, fmt.Errorf("%w: got %s", ErrInvalidEpochDuration, settings.EpochDuration)
	}

	availablePolicies := make(map[PolicyType]Policy[K, V], len(policies))
	policyOrder := make([]PolicyType, 0, len(policies))
	for _, policy := range policies {
		if policy == nil {
			return nil, ErrNilPolicy
		}
		if _, exists := availablePolicies[policy.GetType()]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicatePolicy, policy.GetType())
		}
		availablePolicies[policy.GetType()] = policy
		policyOrder = append(policyOrder, policy.GetType())
	}
	slices.Sort(policyOrder)

	ctx, cancel := context.WithCancel(context.Background())

	ac := &AdaptiveCache[K, V]{
		policies:     availablePolicies,
		policyOrder:  policyOrder,
		activePolicy: policies[0].GetType(),
		bandit:       bandit,
		epochTicker:  time.NewTicker(settings.EpochDuration),
		ctx:          ctx,
		cancel:       cancel,
		settings:     settings,
	}

	// A bandit that wants whole epochs gets them instead of the per-arm
	// stream, never as well as: RecordEpoch carries the same counts, so
	// delivering both would double every arm's evidence.
	if epochBandit, ok := bandit.(EpochBandit); ok {
		ac.epochBandit = epochBandit
	}

	sampleRate := settings.ShadowSampleRate
	if sampleRate <= 0 {
		sampleRate = 1
	}
	minShadowCap := settings.MinShadowCapacity
	if minShadowCap <= 0 {
		minShadowCap = DefaultMinShadowCapacity
	}
	ac.minShadowCap = minShadowCap
	ac.initShadowDutyLocked(sampleRate, minShadowCap)

	ac.wg.Add(1)
	go ac.runAdaptiveSelect()

	return ac, nil
}
