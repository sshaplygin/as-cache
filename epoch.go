package ascache

import "time"

func (c *AdaptiveCache[K, V]) runAdaptiveSelect() {
	defer c.wg.Done()

	// A cache driven only by Settings.EpochRequests has no ticker. Receiving
	// from a nil channel blocks forever, so the select then waits on ctx
	// alone and this goroutine exists purely to be stopped by Close.
	var ticks <-chan time.Time
	if c.epochTicker != nil {
		defer c.epochTicker.Stop()
		ticks = c.epochTicker.C
	}

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticks:
			c.runEpoch()
		}
	}
}

// countRequest advances the request-driven epoch clock and runs the epoch on
// the call that completes it. Caller must hold no lock: runEpoch takes the
// write lock.
//
// Exactly one caller per epoch sees the count equal the limit, so exactly one
// epoch runs however many goroutines are in Get. The limit is subtracted
// rather than the counter reset, so requests arriving mid-crossing still
// count towards the next epoch.
func (c *AdaptiveCache[K, V]) countRequest() {
	limit := c.settings.EpochRequests
	if limit <= 0 {
		return
	}

	if c.epochRequests.Add(1) != limit {
		return
	}
	c.epochRequests.Add(-limit)

	c.runEpoch()
}

// runEpoch performs one epoch tick in three phases, because the middle one
// must not hold the cache's lock.
//
//  1. under the write lock: close any gradual window, snapshot and reset every
//     arm's counters, advance epochID;
//  2. holding no cache lock: deliver that snapshot to the bandit and ask it to
//     select;
//  3. under the write lock again: check the decision is still current, then
//     apply it.
//
// Phase 2 is the whole reason for the split. Go's RWMutex queues new readers
// behind a waiting writer, so a bandit called with the write lock held stalls
// every Get in the process for its full duration -- a store timeout becomes a
// cache outage.
//
// Phases 1 and 3 are each atomic, so no caller observes a half-applied switch.
// Between them the cache is fully usable and its contents may change; nothing
// in phase 3 assumes otherwise.
func (c *AdaptiveCache[K, V]) runEpoch() {
	snapshot := c.collectEpoch()
	newPolicy := c.consultBandit(snapshot)

	c.applySelection(snapshot, newPolicy)
}

// epochSnapshot is one epoch's evidence, taken under the write lock and then
// carried out of it. It is a value, not a view: the counters were read and
// reset in the same critical section, so nothing here can change underneath
// the bandit.
type epochSnapshot struct {
	// epochID identifies the epoch this evidence was collected for. It is what
	// the report carries and what a switch records as its epoch.
	epochID int64
	// collected is the value of epochsCollected at the moment of collection.
	// Phase 3 compares it against the current one to recognise a decision a
	// later epoch has already superseded.
	collected int64
	active    PolicyType
	// report is the per-arm evidence in policyOrder. It is delivered arm by
	// arm to a plain Bandit and whole to an EpochBandit.
	report []ShadowStats
	// reported is false on an epoch the capacity gate skipped: nothing was
	// measured, so the bandit is not consulted and nothing is applied.
	reported   bool
	capacity   int
	sampleRate float64
}

// collectEpoch is phase 1: it closes any gradual migration window and takes
// the epoch's evidence.
func (c *AdaptiveCache[K, V]) collectEpoch() epochSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	// A gradual migration window lasts at most one epoch. Left open it would
	// never close on a workload that stops touching the keys still pending:
	// the source would hold real values at full capacity indefinitely, compete
	// as an arm measured at a capacity no other shadow runs at, and keep every
	// Get on the write-locked path. Closing here also demotes it, so it is a
	// comparable miniature by the time stats are collected below.
	c.closeMigrationLocked()

	c.epochsCollected++
	snapshot := c.snapshotEpochLocked()
	snapshot.collected = c.epochsCollected

	return snapshot
}

// consultBandit is phase 2: it delivers the epoch's evidence and asks for the
// next policy. It holds no cache lock, so a bandit that blocks here delays the
// switch it is deciding and nothing else.
//
// banditMu still serialises the call. Overlapping epochs deliver disjoint
// evidence -- each snapshot was taken and reset in one critical section -- but
// a Bandit is caller-supplied code with no stated concurrency contract, and it
// used to be entered under the cache's write lock. That guarantee is kept.
func (c *AdaptiveCache[K, V]) consultBandit(snapshot epochSnapshot) PolicyType {
	if !snapshot.reported {
		return snapshot.active
	}

	c.banditMu.Lock()
	defer c.banditMu.Unlock()

	if c.epochBandit != nil {
		c.epochBandit.RecordEpoch(EpochReport{
			EpochID:    snapshot.epochID,
			Active:     snapshot.active,
			Stats:      snapshot.report,
			Capacity:   snapshot.capacity,
			SampleRate: snapshot.sampleRate,
		})
	} else {
		for _, armStats := range snapshot.report {
			c.bandit.RecordStats(armStats)
		}
	}

	return c.bandit.SelectPolicy()
}

// applySelection is phase 3: it applies the bandit's choice if that choice is
// still the current epoch's to make.
//
// A decision is dropped when another epoch has collected since this one did.
// Such a decision was computed from evidence two epochs old, and worse, the
// gates would check it against c.epochStats that the newer epoch has already
// overwritten -- so it would be admitted or rejected on numbers that do not
// belong to it. The newer epoch decides instead.
func (c *AdaptiveCache[K, V]) applySelection(snapshot epochSnapshot, newPolicy PolicyType) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// epochID counts ticks, including the ones the capacity gate skipped and
	// the ones whose decision is dropped below, and it advances here so the
	// stability gates see exactly the counter they saw before the epoch was
	// split into phases.
	defer func() { c.epochID++ }()

	// ObserveOnly measures, reports and advises, but never acts: the cache
	// keeps behaving exactly like the policy it was built with.
	if !snapshot.reported || c.settings.ObserveOnly {
		return
	}

	if c.epochsCollected != snapshot.collected {
		return
	}

	// A Bandit is caller-supplied code, and nothing constrains what it returns.
	// A selection naming a policy this cache does not hold - Undefined most
	// often, from a bandit that has not yet formed an opinion - would reach
	// switchLocked, look the missing policy up in the map, and dereference a
	// nil interface, panicking the epoch goroutine and taking the process with
	// it. An unrecognised selection means no change.
	if c.activePolicy != newPolicy && c.hasPolicy(newPolicy) && c.allowSwitchLocked(newPolicy) {
		c.switchLocked(c.activePolicy, newPolicy)
		c.lastSwitchEpoch = c.epochID
	}
}

// hasPolicy reports whether the cache holds the named policy as one of its
// arms. It must be called while at least the read lock is held.
func (c *AdaptiveCache[K, V]) hasPolicy(policyType PolicyType) bool {
	_, ok := c.policies[policyType]

	return ok
}

// tryChangePolicy takes one epoch's evidence, delivers it to the bandit and
// returns the policy selected for the next epoch. It performs no migration and
// advances no epoch counter. It exists as a lock-acquiring entry point;
// callers inside the epoch use collectEpoch and consultBandit directly.
func (c *AdaptiveCache[K, V]) tryChangePolicy() PolicyType {
	c.mu.Lock()
	snapshot := c.snapshotEpochLocked()
	c.mu.Unlock()

	return c.consultBandit(snapshot)
}

// snapshotEpochLocked reads and resets every policy's counters and returns the
// epoch's evidence -- the active policy included, so its posterior does not go
// stale.
//
// With EvictPartialCapacityFilling false and the active policy not yet full it
// collects nothing, resets nothing and reports reported=false; counters
// accumulate until the next reporting epoch. Otherwise counters reset here,
// the active policy's folded into globalStats first so Stats() stays
// cumulative and no active-tenure count leaks into a first shadow epoch after
// demotion.
//
// Counters are read and reset in this one critical section, which is what lets
// the result leave the lock: the evidence cannot then be counted twice, and
// nothing the cache does next can change it.
//
// Caller must hold the write lock.
func (c *AdaptiveCache[K, V]) snapshotEpochLocked() epochSnapshot {
	currentPolicy := c.activePolicy
	snapshot := epochSnapshot{epochID: c.epochID, active: currentPolicy}

	// The capacity gate exists to avoid switching on the strength of a
	// half-full cache. In ObserveOnly mode nothing switches, so the gate would
	// only suppress the measurement the caller is running the cache for.
	if !c.settings.ObserveOnly && !c.settings.EvictPartialCapacityFilling &&
		c.policies[currentPolicy].Len() != c.policies[currentPolicy].Cap() {
		// Nothing was measured this epoch: drop the previous epoch's numbers
		// so the stability gates never compare against stale evidence.
		clear(c.epochStats)

		return snapshot
	}

	if c.epochStats == nil {
		c.epochStats = make(map[PolicyType]PolicyStats, len(c.policies))
	}
	if c.tenureStats == nil {
		c.tenureStats = make(map[PolicyType]PolicyStats, len(c.policies))
	}
	c.reportingEpochs++

	// The slice is allocated per epoch and never reused, so an EpochBandit
	// handed the whole of it may retain it.
	report := make([]ShadowStats, 0, len(c.policyOrder))

	// policyOrder rather than ranging the map: a map's order is random, and an
	// epoch's evidence should be reproducible for anything that hashes,
	// serialises or logs it.
	for _, policyType := range c.policyOrder {
		policy := c.policies[policyType]

		stats := policy.GetStats()
		policy.ResetStats()

		reported := stats
		if policy.GetType() == currentPolicy {
			// Stats() reports everything the cache served, so the active
			// policy's full counters are what accumulate there.
			c.globalStats.Hits += stats.Hits
			c.globalStats.Misses += stats.Misses

			// The bandit instead sees the active policy measured over the
			// sampled substream, the same one the shadows are measured over,
			// so no arm is judged on more evidence than another.
			reported = PolicyStats{
				Hits:   c.activeSampledHits.Swap(0),
				Misses: c.activeSampledMisses.Swap(0),
			}
		}

		c.epochStats[policy.GetType()] = reported

		tenure := c.tenureStats[policy.GetType()]
		tenure.Hits += reported.Hits
		tenure.Misses += reported.Misses
		c.tenureStats[policy.GetType()] = tenure

		report = append(report, ShadowStats{
			Policy: policy.GetType(),
			Hits:   reported.Hits,
			Misses: reported.Misses,
		})
	}

	snapshot.report = report
	snapshot.reported = true
	snapshot.capacity = c.nominalCap[currentPolicy]
	snapshot.sampleRate = c.sampler.rate

	return snapshot
}
