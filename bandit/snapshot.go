package bandit

import (
	"fmt"
	"sort"
	"strings"
	"time"

	ascache "github.com/sshaplygin/as-cache"
)

// ArmEvidence is one arm's pooled, decayed evidence as the fleet last reported
// it.
type ArmEvidence struct {
	Policy string `json:"policy"`
	// Hits and Misses are weighted sums over the window, so they are
	// fractional and are not request counts. HitRate is what the decision was
	// actually made on.
	Hits    float64 `json:"hits"`
	Misses  float64 `json:"misses"`
	HitRate float64 `json:"hit_rate"`
}

// Snapshot is what a distributed bandit is doing, for monitoring.
//
// A cache that changes its own eviction policy is only safe to run if you can
// see what it is doing, and one that takes that decision from other machines
// needs one more thing visible: whether it is actually hearing from them.
// Fallback is the field to alert on. It is not an error - the cache keeps
// working, deciding locally - but a fleet where every replica has quietly
// fallen back is no longer a fleet, and nothing else about the cache looks any
// different.
type Snapshot struct {
	// Selection is the policy this replica's bandit is currently returning.
	Selection string `json:"selection"`
	// Mode is "leader" or "shared-posterior".
	Mode string `json:"mode"`
	// NodeID identifies this replica, and Namespace is the fingerprinted
	// namespace it coordinates under - the plain namespace plus a hash of the
	// cache's arms, capacity and sample rate. Two replicas that should be
	// pooling but are not will differ here, and nowhere else.
	NodeID    string `json:"node_id"`
	Namespace string `json:"namespace"`
	// Regime is the human-readable form of what that fingerprint covers.
	Regime string `json:"regime"`

	// Fallback reports that the store has been unreachable for longer than
	// FallbackAfter and this replica is deciding on its own evidence.
	Fallback bool `json:"fallback"`
	// LastSyncAge is how long ago the last successful round trip completed.
	LastSyncAge time.Duration `json:"last_sync_age_ns"`
	// LastError is the most recent failure, empty if the last round trip
	// succeeded.
	LastError string `json:"last_error,omitempty"`
	// LastBucket is the coordination bucket of the last successful sync, by
	// the store's clock.
	LastBucket int64 `json:"last_bucket"`

	// Syncs and SyncFailures count round trips. Leaderships counts the buckets
	// this replica led, Decisions the fleet decisions it applied, and Rejected
	// the ones it refused because they named a policy it does not have.
	Syncs        int64 `json:"syncs"`
	SyncFailures int64 `json:"sync_failures"`
	Leaderships  int64 `json:"leaderships"`
	Decisions    int64 `json:"decisions"`
	Rejected     int64 `json:"rejected"`

	// Fleet holds the pooled evidence behind the last decision this replica
	// computed, best hit rate first.
	//
	// It is only populated on a replica that read the window: always under
	// ModeSharedPosterior, and only while leading under ModeLeader. A follower
	// reporting an empty Fleet is working correctly - it is applying a
	// decision, not making one - so read it from whichever replica has
	// Leaderships climbing.
	Fleet []ArmEvidence `json:"fleet"`
}

// Snapshot reads the bandit's current state. It is safe to call at any time
// and does not disturb coordination.
func (d *Distributed) Snapshot() Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()

	mode := "leader"
	if d.cfg.Mode == ModeSharedPosterior {
		mode = "shared-posterior"
	}

	snapshot := Snapshot{
		Selection:    ascache.PolicyType(d.selection.Load()).String(),
		Mode:         mode,
		NodeID:       d.cfg.NodeID,
		Namespace:    d.namespace,
		Fallback:     d.state.fallback,
		LastBucket:   int64(d.state.lastBucket),
		Syncs:        d.state.syncs,
		SyncFailures: d.state.syncFailures,
		Leaderships:  d.state.leaderships,
		Decisions:    d.state.decisions,
		Rejected:     d.state.rejected,
		Fleet:        make([]ArmEvidence, 0, len(d.state.fleet)),
	}

	if d.haveShape {
		snapshot.Regime = d.shape.String()
	}
	if d.state.lastErr != nil {
		snapshot.LastError = d.state.lastErr.Error()
	}
	if !d.state.lastSync.IsZero() {
		snapshot.LastSyncAge = d.cfg.Now().Sub(d.state.lastSync)
	}

	for policy, arm := range d.state.fleet {
		snapshot.Fleet = append(snapshot.Fleet, ArmEvidence{
			Policy:  policy.String(),
			Hits:    arm.Hits,
			Misses:  arm.Misses,
			HitRate: arm.hitRate(),
		})
	}

	// Ties broken by name so an unchanged fleet renders identically on every
	// scrape; ranging a map alone would reorder equal arms on each call.
	sort.SliceStable(snapshot.Fleet, func(i, j int) bool {
		if snapshot.Fleet[i].HitRate != snapshot.Fleet[j].HitRate {
			return snapshot.Fleet[i].HitRate > snapshot.Fleet[j].HitRate
		}

		return snapshot.Fleet[i].Policy < snapshot.Fleet[j].Policy
	})

	return snapshot
}

// String renders the snapshot as a short human-readable summary.
func (s Snapshot) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s running %s, %s mode", s.NodeID, s.Selection, s.Mode)
	if s.Fallback {
		fmt.Fprintf(&b, " (FALLBACK: store unreachable for %s)", s.LastSyncAge.Round(time.Millisecond))
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "namespace %s [%s]\n", s.Namespace, s.Regime)
	fmt.Fprintf(&b, "%d syncs, %d failures, %d led, %d decisions applied, %d rejected\n",
		s.Syncs, s.SyncFailures, s.Leaderships, s.Decisions, s.Rejected)

	if len(s.Fleet) == 0 {
		return b.String()
	}

	fmt.Fprintf(&b, "\n%-10s %9s %14s %14s\n", "policy", "hit rate", "hits", "misses")
	for _, arm := range s.Fleet {
		fmt.Fprintf(&b, "%-10s %8.2f%% %14.0f %14.0f\n",
			arm.Policy, arm.HitRate*100, arm.Hits, arm.Misses)
	}

	return b.String()
}
