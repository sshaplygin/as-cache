package bandit

import (
	"fmt"
	"math/rand/v2"
	"os"
	"time"
)

// Mode selects how a fleet turns pooled evidence into a policy choice.
type Mode uint8

const (
	// ModeLeader has one replica per coordination epoch read the fleet's
	// aggregate, choose, and publish that choice for every other replica to
	// apply. The fleet therefore runs one policy at a time.
	//
	// This is the default, and it is the mode that keeps the pooled numbers
	// honest. Every replica measures the same arm in the active role and every
	// other arm in the shadow role, so the systematic gap between the two
	// roles applies equally to every replica's report and largely cancels when
	// they are summed. See Role.
	ModeLeader Mode = iota + 1

	// ModeSharedPosterior has every replica draw its own selection from the
	// pooled posterior. No leader, no election, and a replica that loses the
	// store keeps working without any handover.
	//
	// The cost is that replicas may run different policies indefinitely, which
	// makes the role gap asymmetric: an arm active on most of the fleet is
	// mostly measured in the flattering role, so it accumulates an advantage
	// in proportion to how widely it is already deployed. Pair it with
	// EvidenceShadowOnly to remove that feedback, at the price of ignoring
	// what the serving policies measured.
	ModeSharedPosterior
)

// EvidenceMode selects which measurements feed the pooled posterior.
type EvidenceMode uint8

const (
	// EvidenceAll pools active-role and shadow-role counts together. It
	// matches what a single-node bandit sees exactly, and it is the default.
	EvidenceAll EvidenceMode = iota + 1

	// EvidenceShadowOnly discards active-role counts and compares arms purely
	// on shadow measurements, so every arm is measured the same way. It is
	// only meaningful under ModeSharedPosterior, where a diverse fleet leaves
	// every arm shadowing somewhere; under ModeLeader it is rejected.
	EvidenceShadowOnly
)

// Config configures a Distributed bandit.
type Config struct {
	// Store is where the fleet coordinates. Required.
	Store Store

	// Namespace names the fleet. Required, and it must be the same string on
	// every replica that should pool with each other, and different on any
	// that should not.
	//
	// It is not the whole key: a fingerprint of the cache's measurement regime
	// - its arms, its capacity and its sample rate - is appended, so replicas
	// that share a name but are not measuring the same thing pool separately
	// instead of pooling wrongly. A hit rate from a 1000-entry cache says
	// nothing about a 100-entry one, and averaging them describes neither.
	Namespace string

	// NodeID identifies this replica within the fleet, and needs only to be
	// unique. Defaults to hostname-pid-random, which is unique enough and
	// stays readable in a leadership key.
	NodeID string

	// CoordinationEpoch is how often this replica syncs with the store, and
	// therefore how often the fleet can change its mind. Required.
	//
	// It is a separate, much slower clock than Settings.EpochDuration, and
	// deliberately so. Cache epochs are tuned in tens of milliseconds, which
	// is below the round trip to the store and below the clock agreement a
	// fleet can be assumed to have. Measure on the fast clock, coordinate on
	// the slow one. A second is a sensible starting point.
	CoordinationEpoch time.Duration

	// Window is how many past buckets the leader reads. Defaults to
	// DefaultWindow. Zero after defaulting means only the previous bucket.
	Window int

	// Decay weights each bucket by Decay^age before summing, so recent
	// evidence counts for more and old evidence fades out of the decision
	// rather than sitting in it forever. Defaults to DefaultDecay. A value of
	// 1 weights the whole window equally.
	//
	// A shared counter cannot be decayed in place - every replica applying the
	// multiplication would compound it once per replica - so the store holds
	// plain per-bucket sums and the weighting happens here, on read.
	Decay float64

	// MaxEvidence caps how many observations an arm's posterior is allowed to
	// rest on, after weighting. Defaults to DefaultMaxEvidence; a negative
	// value disables the cap.
	//
	// It exists because pooling multiplies evidence by the size of the fleet.
	// A Beta posterior narrows with the square root of what it has seen, so a
	// thousand replicas produce posteriors sharp enough that every Thompson
	// draw returns the same arm - the bandit stops exploring precisely at the
	// scale where a workload change is most expensive to miss. Capping keeps
	// the measured rate and discards the surplus certainty.
	MaxEvidence float64

	// Mode selects leader-elected or shared-posterior selection. Defaults to
	// ModeLeader.
	Mode Mode

	// Evidence selects which roles' counts feed the posterior. Defaults to
	// EvidenceAll.
	Evidence EvidenceMode

	// FallbackAfter is how long the store may go unreachable before this
	// replica stops waiting for the fleet and decides locally. Defaults to
	// three coordination epochs.
	FallbackAfter time.Duration

	// SyncTimeout bounds a single round trip to the store. Defaults to the
	// coordination epoch. It bounds how far behind a hung store can push this
	// replica's ticks; it never affects the cache, which is not waiting on any
	// of this.
	SyncTimeout time.Duration

	// LocalDiscount is the discount factor of the local Thompson bandit that
	// takes over when the store is unreachable. Defaults to
	// DefaultLocalDiscount.
	LocalDiscount float64

	// Jitter spreads each replica's sync across a fraction of the coordination
	// epoch, so a large fleet does not arrive at the store in lockstep on
	// every bucket boundary. Defaults to DefaultJitter. Buckets come from the
	// store's clock, so jitter can never put a replica in the wrong one.
	Jitter float64

	// Seed seeds the local fallback bandit and the jitter. Zero draws a random
	// one, which is what you want: seeding a fleet identically would
	// synchronise the jitter it exists to break up.
	Seed uint64

	// Now is the clock used for staleness and jitter, a seam for tests.
	// Defaults to time.Now. It is never used to derive a bucket - that is the
	// store's job precisely so a replica's clock cannot matter.
	Now func() time.Time
}

// Defaults applied to a zero-valued Config field.
const (
	// DefaultWindow is how many buckets of history the leader reads. Ten
	// buckets of a one-second epoch is ten seconds of fleet evidence, which is
	// enough to be stable and short enough to still track a workload that
	// moves.
	DefaultWindow = 10
	// DefaultDecay weights each bucket at 0.8 of the one after it, so the
	// oldest bucket of a default window carries about a seventh of the weight
	// of the newest.
	DefaultDecay = 0.8
	// DefaultLocalDiscount is the fallback bandit's discount factor.
	DefaultLocalDiscount = 0.7
	// DefaultJitter spreads syncs over a tenth of the coordination epoch.
	DefaultJitter = 0.1
	// DefaultMaxEvidence caps an arm's posterior at a hundred thousand
	// weighted observations, which puts its standard deviation at roughly a
	// sixth of a percentage point: enough certainty to separate arms that
	// differ by half a point, little enough to keep exploring arms that do
	// not.
	DefaultMaxEvidence = 100_000.0
)

// validate fills in defaults and reports whatever cannot be defaulted.
func (c *Config) validate() error {
	if c.Store == nil {
		return ErrNilStore
	}
	if c.Namespace == "" {
		return ErrEmptyNamespace
	}
	if c.CoordinationEpoch <= 0 {
		return fmt.Errorf("%w: got %s", ErrInvalidCoordinationEpoch, c.CoordinationEpoch)
	}
	if c.Window < 0 {
		return fmt.Errorf("%w: got %d", ErrInvalidWindow, c.Window)
	}
	if c.Decay < 0 || c.Decay > 1 {
		return fmt.Errorf("%w: got %v", ErrInvalidDecay, c.Decay)
	}
	if c.Jitter < 0 || c.Jitter >= 0.5 {
		return fmt.Errorf("%w: got %v", ErrInvalidJitter, c.Jitter)
	}

	// Both enums number from one, so their zero value names no mode at all
	// rather than happening to name the default. That has to be resolved here,
	// before anything compares against it: an unresolved zero Mode is neither
	// ModeLeader nor ModeSharedPosterior, so the replica would claim no
	// leadership and follow no leader, and the fleet would sync forever
	// without ever deciding anything.
	if c.Mode == 0 {
		c.Mode = ModeLeader
	}
	if c.Evidence == 0 {
		c.Evidence = EvidenceAll
	}

	if c.Mode == ModeLeader && c.Evidence == EvidenceShadowOnly {
		return ErrShadowOnlyUnderLeader
	}

	if c.Window == 0 {
		c.Window = DefaultWindow
	}
	if c.Decay == 0 {
		c.Decay = DefaultDecay
	}
	if c.Jitter == 0 {
		c.Jitter = DefaultJitter
	}
	if c.LocalDiscount <= 0 || c.LocalDiscount > 1 {
		c.LocalDiscount = DefaultLocalDiscount
	}
	if c.MaxEvidence == 0 {
		c.MaxEvidence = DefaultMaxEvidence
	}
	if c.FallbackAfter <= 0 {
		c.FallbackAfter = 3 * c.CoordinationEpoch
	}
	if c.SyncTimeout <= 0 {
		c.SyncTimeout = c.CoordinationEpoch
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Seed == 0 {
		// Not a secret: it only has to differ between replicas, so that a
		// fleet's jitter is not synchronised.
		c.Seed = rand.Uint64() //nolint:gosec // see above
	}
	if c.NodeID == "" {
		c.NodeID = defaultNodeID(c.Seed)
	}

	return nil
}

// counterTTL is how long a bucket's counters outlive the bucket. The leader
// reads Window buckets back, so anything shorter would leave holes in the
// window; the margin covers a leader that ticks late.
func (c *Config) counterTTL() time.Duration {
	return time.Duration(c.Window+2) * c.CoordinationEpoch
}

// leaderTTL is how long a leadership claim survives. Leadership is per-bucket
// and produces one immutable decision, so an over-long claim costs nothing
// beyond that bucket; two epochs covers a leader whose sync is slow.
func (c *Config) leaderTTL() time.Duration {
	return 2 * c.CoordinationEpoch
}

// decisionTTL is how long a published decision remains readable. It outlives
// its bucket so a replica that ticks late still finds it.
func (c *Config) decisionTTL() time.Duration {
	return 3 * c.CoordinationEpoch
}

// defaultNodeID builds an identifier that is unique enough for leadership and
// still legible in a key.
func defaultNodeID(seed uint64) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}

	return fmt.Sprintf("%s-%d-%x", host, os.Getpid(), seed&0xffffff)
}
