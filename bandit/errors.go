package bandit

import "errors"

// ErrNilStore is returned by NewDistributed when Config.Store is nil. There is
// no useful default: a distributed bandit with nowhere to coordinate is just a
// local one, and silently becoming one would hide a misconfiguration behind
// behaviour that looks fine.
var ErrNilStore = errors.New("bandit: store must not be nil")

// ErrInvalidCoordinationEpoch is returned by NewDistributed when
// Config.CoordinationEpoch is zero or negative.
var ErrInvalidCoordinationEpoch = errors.New("bandit: coordination epoch must be positive")

// ErrInvalidWindow is returned by NewDistributed when Config.Window is
// negative.
var ErrInvalidWindow = errors.New("bandit: window must not be negative")

// ErrInvalidDecay is returned by NewDistributed when Config.Decay falls
// outside (0,1].
var ErrInvalidDecay = errors.New("bandit: decay must be in (0,1]")

// ErrInvalidJitter is returned by NewDistributed when Config.Jitter falls
// outside [0,0.5). Half an epoch of jitter would let one replica's tick
// overtake another's by a whole bucket.
var ErrInvalidJitter = errors.New("bandit: jitter must be in [0,0.5)")

// ErrEmptyNamespace is returned by NewDistributed when Config.Namespace is
// empty. Two unrelated fleets sharing a store and pooling each other's
// evidence is a failure with no symptom other than bad decisions, so the name
// is required rather than defaulted.
var ErrEmptyNamespace = errors.New("bandit: namespace must not be empty")

// ErrShadowOnlyUnderLeader is returned by NewDistributed for the combination
// of ModeLeader and EvidenceShadowOnly.
//
// Under leader election every replica runs the same active policy, so that
// policy is nobody's shadow and shadow-only evidence contains nothing about
// it. Its posterior would stay at the uniform prior forever: it could still be
// drawn, but only by chance, and never on the strength of how it is actually
// performing. Discarding evidence for the one arm that is serving all the
// traffic is not a tuning choice, it is a broken configuration.
var ErrShadowOnlyUnderLeader = errors.New(
	"bandit: EvidenceShadowOnly cannot be used with ModeLeader: " +
		"the fleet-wide active policy has no shadow measurements anywhere")
