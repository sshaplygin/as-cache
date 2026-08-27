package ascache

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// Settings configures the behaviour of AdaptiveCache.
type Settings struct {
	// EpochDuration is how often the cache re-evaluates its policies on a
	// wall clock. Either this or EpochRequests must be set; setting both
	// applies both, and whichever comes first ends the epoch.
	EpochDuration time.Duration

	// EpochRequests ends an epoch every N Get calls instead of on a clock,
	// which is what makes a replay reproducible on any machine. Get is the
	// unit because hits and misses are recorded there and nowhere else, so a
	// write-only workload never ends an epoch. The epoch runs on the
	// goroutine making the Nth Get, so that call pays for any switch it
	// triggers; production should prefer EpochDuration. Zero disables it.
	// See docs/benchmarking.md.
	EpochRequests int64
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
	// active policy and the candidate must have observed in the measured epoch
	// before a switch is allowed. These are the requests the bandit sees, so
	// under ShadowSampleRate they are sampled ones. Zero imposes no minimum.
	MinEpochRequests int64

	// ShadowSampleRate is the fraction of the keyspace, in (0,1], that shadow
	// policies track, and where most of the adaptive layer's overhead goes.
	// Shadows shrink with the rate to stay faithful miniatures, and every
	// shadow samples the same keys so their hit rates stay comparable. The
	// active policy still serves every key; only its measurement is sampled,
	// so all arms carry equally weighted evidence. Zero means 1, no sampling.
	// See docs/configuration.md.
	ShadowSampleRate float64

	// ObserveOnly runs the cache as a measurement instrument: every policy is
	// measured and reported each epoch, but the active policy never changes
	// and no migration happens, so the cache behaves exactly like the policy
	// it was built with. Advice() reports what the others would have served.
	// See docs/advisor-mode.md.
	ObserveOnly bool

	// MinShadowCapacity is the floor on a shadow's miniature capacity. When
	// the sample rate would shrink a shadow below it the effective rate is
	// raised instead, up to the point where sampling disables itself. Zero
	// applies DefaultMinShadowCapacity.
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
	if settings.EpochRequests < 0 {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidEpochRequests, settings.EpochRequests)
	}
	// An epoch has to be ended by something. Either clock is acceptable and
	// both together are fine; neither leaves a cache that measures every
	// policy forever and never acts on any of it.
	if settings.EpochDuration <= 0 && settings.EpochRequests == 0 {
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
		ctx:          ctx,
		cancel:       cancel,
		settings:     settings,
	}

	// No ticker when the cache is driven purely by request count:
	// time.NewTicker panics on a non-positive duration, and a cache that ends
	// its epochs on Get has nothing for a background clock to do.
	if settings.EpochDuration > 0 {
		ac.epochTicker = time.NewTicker(settings.EpochDuration)
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
