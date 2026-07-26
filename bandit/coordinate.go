// This file holds everything that runs on the coordination goroutine: the sync
// loop and the one round trip it makes per coordination epoch. It is separate
// from distributed.go, which holds the surface the cache itself calls, because
// the two run under completely different constraints - nothing here is allowed
// to be slow, and nothing there is allowed to block at all.

package bandit

import (
	"context"
	"time"

	ascache "github.com/sshaplygin/as-cache"
)

// coordinate runs the sync loop until Close.
func (d *Distributed) coordinate() {
	defer d.wg.Done()

	timer := time.NewTimer(d.nextInterval())
	defer timer.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-timer.C:
			d.sync()
			timer.Reset(d.nextInterval())
		}
	}
}

// nextInterval returns the coordination epoch with jitter applied, so a large
// fleet spreads its round trips across the epoch instead of arriving together
// on every boundary. Buckets are assigned by the store's clock, so jitter can
// never put a replica's counts in the wrong window.
func (d *Distributed) nextInterval() time.Duration {
	if d.cfg.Jitter <= 0 {
		return d.cfg.CoordinationEpoch
	}

	spread := float64(d.cfg.CoordinationEpoch) * d.cfg.Jitter
	offset := (d.rng.Float64()*2 - 1) * spread

	return time.Duration(float64(d.cfg.CoordinationEpoch) + offset)
}

// sync performs one coordination round: publish this replica's counts, then
// work out what the fleet should be running.
func (d *Distributed) sync() {
	req, ok := d.drain()
	if !ok {
		// No report has arrived yet, so the measurement regime is unknown and
		// there is nothing to publish or to key by.
		return
	}

	ctx, cancel := context.WithTimeout(d.ctx, d.cfg.SyncTimeout)
	defer cancel()

	result, err := d.cfg.Store.Sync(ctx, req)
	if err != nil {
		d.recordFailure(err)
		return
	}

	decision, decided := result.Decision, result.HasDecision

	switch {
	case d.cfg.Mode == ModeSharedPosterior:
		// Every replica reads the window and draws for itself. No leader is
		// elected and the decision key is never written or read.
		decision, decided = d.drawShared(ctx, req.Namespace, result.Bucket)

	case result.Leader:
		// This replica won the bucket. It decides for the fleet, and applies
		// its own decision now rather than reading it back a round later.
		if choice, ok := d.leadBucket(ctx, req.Namespace, result.Bucket); ok {
			decision, decided = choice, true
		}
	}

	d.applyResult(result.Bucket, decision, decided)
}

// drain takes everything buffered since the last sync and builds the request.
// The buffer is cleared whether or not the sync goes on to succeed: counts
// that missed their window are not worth carrying forward.
func (d *Distributed) drain() (SyncRequest, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.haveShape {
		return SyncRequest{}, false
	}

	counts := make([]ArmCounts, 0, len(d.pending))
	for key, stats := range d.pending {
		counts = append(counts, ArmCounts{
			Policy: key.Policy,
			Role:   key.Role,
			Hits:   stats.Hits,
			Misses: stats.Misses,
		})
	}
	clear(d.pending)

	return SyncRequest{
		Namespace:   d.namespace,
		NodeID:      d.cfg.NodeID,
		Counts:      counts,
		EpochMillis: d.cfg.CoordinationEpoch.Milliseconds(),
		CounterTTL:  d.cfg.counterTTL(),
		LeaderTTL:   d.cfg.leaderTTL(),
		Lead:        d.cfg.Mode == ModeLeader,
	}, true
}

// readPooled reads the fleet's recent buckets and returns the pooled, decayed,
// evidence-capped posterior per arm.
//
// The window stops one bucket short of the current one: the fleet is still
// writing into that bucket, so including it would weight whichever replicas
// happened to have synced already.
func (d *Distributed) readPooled(
	ctx context.Context,
	namespace string,
	bucket Bucket,
) (map[ascache.PolicyType]weighted, bool) {
	newest := bucket - 1
	first := newest - Bucket(d.cfg.Window) + 1

	window, err := d.cfg.Store.Window(ctx, namespace, first, newest)
	if err != nil {
		d.recordFailure(err)
		return nil, false
	}

	pooled := aggregate(window, newest, d.cfg.Decay, d.cfg.Evidence)
	capEvidence(pooled, d.cfg.MaxEvidence)
	d.recordFleet(pooled)

	return pooled, true
}

// leadBucket decides for the fleet and publishes the decision. A failure
// anywhere leaves the bucket without one, which every replica reads as "keep
// doing what you are doing".
func (d *Distributed) leadBucket(
	ctx context.Context,
	namespace string,
	bucket Bucket,
) (ascache.PolicyType, bool) {
	d.mu.Lock()
	d.state.leaderships++
	d.mu.Unlock()

	pooled, ok := d.readPooled(ctx, namespace, bucket)
	if !ok {
		return ascache.Undefined, false
	}

	choice := draw(d.rng, pooled)
	if choice == ascache.Undefined {
		// Nothing in the window: the fleet has published no evidence yet.
		return ascache.Undefined, false
	}

	// The decision in force is what the store reports, not what was drawn: if
	// another replica somehow published first, the leader follows the fleet
	// rather than being the one machine running something else.
	inForce, err := d.cfg.Store.Decide(ctx, namespace, bucket, choice, d.cfg.decisionTTL())
	if err != nil {
		d.recordFailure(err)
		return ascache.Undefined, false
	}

	return inForce, inForce != ascache.Undefined
}

// drawShared reads the fleet's window and draws this replica's own selection
// from it. Replicas share the evidence but not the draw, so they explore
// independently and may run different policies at the same time.
func (d *Distributed) drawShared(
	ctx context.Context,
	namespace string,
	bucket Bucket,
) (ascache.PolicyType, bool) {
	pooled, ok := d.readPooled(ctx, namespace, bucket)
	if !ok {
		return ascache.Undefined, false
	}

	choice := draw(d.rng, pooled)

	return choice, choice != ascache.Undefined
}

// applyResult folds a successful sync into the selection and the observable
// state.
func (d *Distributed) applyResult(bucket Bucket, decision ascache.PolicyType, decided bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.state.lastSync = d.cfg.Now()
	d.state.lastBucket = bucket
	d.state.lastErr = nil
	d.state.syncs++
	d.state.fallback = false

	switch {
	case !decided:
		// No decision this round - the leader has not published yet, or the
		// window was empty. Keeping the previous selection is the mode's one
		// coordination epoch of built-in staleness, and it is bounded: the
		// decision will be there on the next sync.

	case !d.knownArmLocked(decision):
		// Something is publishing decisions for a policy this cache does not
		// have. The fingerprint in the namespace is supposed to make that
		// impossible, so this is a real misconfiguration rather than a race:
		// refuse it, count it, and keep serving.
		d.state.rejected++

	default:
		d.state.decisions++
		d.selection.Store(uint64(decision))
	}
}

func (d *Distributed) knownArmLocked(policy ascache.PolicyType) bool {
	_, ok := d.arms[policy]

	return ok
}

// recordFleet stores the pooled posterior for Snapshot to report.
func (d *Distributed) recordFleet(pooled map[ascache.PolicyType]weighted) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.state.fleet = pooled
}

// recordFailure notes a failed round trip and, once the store has been
// unreachable for longer than FallbackAfter, hands selection to the local
// bandit.
func (d *Distributed) recordFailure(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.state.lastErr = err
	d.state.syncFailures++

	// A replica that has never synced falls back immediately rather than
	// waiting out a grace period measured from a sync that never happened.
	stale := d.state.lastSync.IsZero() ||
		d.cfg.Now().Sub(d.state.lastSync) > d.cfg.FallbackAfter
	if !stale {
		return
	}

	d.state.fallback = true

	choice := d.local.SelectPolicy()
	if choice == ascache.Undefined || !d.knownArmLocked(choice) {
		return
	}
	d.selection.Store(uint64(choice))
}
