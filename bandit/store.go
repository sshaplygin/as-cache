package bandit

import (
	"context"
	"time"

	ascache "github.com/sshaplygin/as-cache"
)

// Bucket identifies one coordination epoch fleet-wide. It is derived from the
// store's clock, never from a replica's, so a machine with a skewed clock
// cannot write its counts into a window nobody else is reading.
type Bucket int64

// Role distinguishes how an arm's counts were measured. It matters because the
// two are not measured the same way: the active arm runs at the cache's full
// capacity, and every shadow runs on a miniature of it. The rates are
// comparable by design, but shadows measure 1 to 3 points pessimistic in
// practice, so pooling one arm's active-role counts with another's shadow-role
// counts hands the first a systematic advantage.
type Role uint8

const (
	// RoleShadow is an arm measured as a shadow: miniature capacity, no real
	// values, no traffic served.
	RoleShadow Role = iota + 1
	// RoleActive is the arm that was serving traffic on the reporting replica.
	RoleActive
)

// String names the role, for keys and for logs.
func (r Role) String() string {
	if r == RoleActive {
		return "active"
	}

	return "shadow"
}

// ArmCounts is one arm's measurements from one replica, in one role.
type ArmCounts struct {
	Policy ascache.PolicyType
	Role   Role
	Hits   int64
	Misses int64
}

// SyncRequest is the once-per-coordination-epoch call every replica makes.
//
// It bundles three things that would otherwise be separate round trips, all of
// which every replica needs on every tick: publish what I measured, tell me
// which bucket that landed in, and let me lead this bucket if nobody has
// claimed it.
type SyncRequest struct {
	// Namespace scopes every key this call touches. Replicas pool with each
	// other exactly when their namespaces match, which is why it carries a
	// fingerprint of the cache's shape - see Config.Namespace.
	Namespace string

	// NodeID identifies the replica, for leadership.
	NodeID string

	// Counts is what this replica measured since its last sync. It may be
	// empty on a tick where the cache reported nothing.
	Counts []ArmCounts

	// EpochMillis is the coordination epoch length. The store divides its own
	// clock by it to derive the bucket, so every replica agrees on bucket
	// boundaries without their clocks having to agree on anything.
	EpochMillis int64

	// CounterTTL is how long a bucket's counters should outlive it. It must
	// comfortably exceed Window * the epoch length, or the leader will read a
	// window with holes in it where buckets have already expired.
	CounterTTL time.Duration

	// LeaderTTL is how long a leadership claim lasts. It bounds nothing
	// important: leadership is per-bucket and the decision it produces is
	// written once, so a claim that outlives its usefulness only means the
	// bucket has no leader, and the next bucket gets one.
	LeaderTTL time.Duration

	// Lead asks to claim leadership of the current bucket if it is unclaimed.
	// A replica configured for shared-posterior mode never sets it.
	Lead bool
}

// SyncResult is what the store knew at the moment of the sync.
type SyncResult struct {
	// Bucket is the bucket the counts were added to, by the store's clock.
	Bucket Bucket

	// Leader reports whether this replica claimed leadership of Bucket. At
	// most one replica per bucket ever sees true.
	Leader bool

	// Decision is the policy published for Bucket, and HasDecision reports
	// whether one had been published when the sync ran. A replica that syncs
	// before its leader has decided sees no decision and keeps using the
	// previous one, which is the mode's one epoch of built-in staleness.
	Decision    ascache.PolicyType
	HasDecision bool
}

// WindowCounts is the fleet's aggregated measurements for one bucket.
type WindowCounts struct {
	Bucket Bucket
	// Arms holds the summed counts across every replica that published into
	// the bucket, keyed by policy and role.
	Arms map[ArmKey]ascache.PolicyStats
}

// ArmKey identifies one arm's counts in one role.
type ArmKey struct {
	Policy ascache.PolicyType
	Role   Role
}

// Store is the shared state a fleet of caches coordinates through.
//
// It is deliberately dumb: it counts, it claims, it reads back. Every decision
// about what the numbers mean - how far back to look, how much to discount,
// which arm wins - stays in this package, so backing it with a different store
// never changes the selection behaviour.
//
// Implementations must be safe for concurrent use, and must respect the
// context: a replica whose store has stopped responding falls back to deciding
// locally, and it can only do that if these calls actually return.
type Store interface {
	// Sync publishes one replica's counts and reports the bucket they landed
	// in, whether this replica leads that bucket, and any decision already
	// published for it.
	Sync(ctx context.Context, req SyncRequest) (SyncResult, error)

	// Window reads the aggregated counts for buckets first through last
	// inclusive. Buckets that have expired or were never written are omitted
	// rather than reported as zero, so a caller can tell a quiet epoch from a
	// missing one. Only the leader of a bucket calls it.
	Window(ctx context.Context, namespace string, first, last Bucket) ([]WindowCounts, error)

	// Decide publishes the policy the fleet should run for a bucket and
	// returns the decision that is actually in force for it.
	//
	// It must not overwrite a decision already published for that bucket: the
	// decision is what replicas act on, and one that changed underneath them
	// would move the fleet mid-epoch for no reason. Publishing to an
	// already-decided bucket is not an error, and returns the decision that
	// was already there - which is why this returns a policy at all. A leader
	// that acted on its own draw while the fleet acted on someone else's would
	// put the one replica making the decisions out of step with every replica
	// following them.
	Decide(
		ctx context.Context,
		namespace string,
		bucket Bucket,
		policy ascache.PolicyType,
		ttl time.Duration,
	) (ascache.PolicyType, error)

	// Close releases whatever the store holds. It does not close a connection
	// the caller supplied.
	Close() error
}
