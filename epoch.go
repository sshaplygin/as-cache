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
// the call that completes it.
//
// It must be called with no lock held: runEpoch takes the write lock.
//
// Exactly one caller per epoch observes the count equal to the limit, so
// exactly one epoch runs however many goroutines are in Get at once. The limit
// is then subtracted rather than the counter reset, so requests that arrived
// during the crossing are still counted towards the next epoch instead of
// being dropped.
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

// runEpoch performs one epoch tick: it selects the next policy, migrates data
// when the policy changes and the stability gates allow it, and advances the
// epoch counter. The entire sequence runs under the write lock so concurrent
// cache operations never observe a half-applied switch (a torn activePolicy or
// partially migrated state).
func (c *AdaptiveCache[K, V]) runEpoch() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// A gradual migration window lasts at most one epoch. Left open it would
	// never close on a workload that stops touching the keys still pending:
	// the source would hold real values at full capacity indefinitely, compete
	// as an arm measured at a capacity no other shadow runs at, and keep every
	// Get on the write-locked path. Closing here also demotes it, so it is a
	// comparable miniature by the time stats are collected below.
	c.closeMigrationLocked()

	newPolicy := c.selectPolicyLocked()
	if c.settings.ObserveOnly {
		// Measure, report, advise - but never act. The cache keeps behaving
		// exactly like the policy it was built with.
		c.epochID++

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

	c.epochID++
}

// hasPolicy reports whether the cache holds the named policy as one of its
// arms. It must be called while at least the read lock is held.
func (c *AdaptiveCache[K, V]) hasPolicy(policyType PolicyType) bool {
	_, ok := c.policies[policyType]

	return ok
}

// tryChangePolicy records every policy's stats with the bandit (nothing on a
// gated epoch — see selectPolicyLocked) and returns the policy selected for
// the next epoch. It acquires the write lock and performs no migration. It
// exists as a lock-acquiring entry point; callers that already hold the lock
// must use selectPolicyLocked instead.
func (c *AdaptiveCache[K, V]) tryChangePolicy() PolicyType {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.selectPolicyLocked()
}

// selectPolicyLocked reports every policy's stats to the bandit — the active
// policy included, so its posterior does not go stale — and returns the
// bandit's chosen policy for the next epoch. When
// EvictPartialCapacityFilling is false and the active policy is not yet full,
// it returns early without reporting or resetting anything; counters then
// accumulate until the next reporting epoch. On a reporting epoch counters
// are reset after delivery; the active policy's counts are folded into
// globalStats first so Stats() stays cumulative and no active-tenure counts
// leak into a policy's first shadow epoch after demotion. It must be called
// while the write lock is held.
func (c *AdaptiveCache[K, V]) selectPolicyLocked() PolicyType {
	currentPolicy := c.activePolicy

	// The capacity gate exists to avoid switching on the strength of a
	// half-full cache. In ObserveOnly mode nothing switches, so the gate would
	// only suppress the measurement the caller is running the cache for.
	if !c.settings.ObserveOnly && !c.settings.EvictPartialCapacityFilling &&
		c.policies[currentPolicy].Len() != c.policies[currentPolicy].Cap() {
		// Nothing was measured this epoch: drop the previous epoch's numbers
		// so the stability gates never compare against stale evidence.
		clear(c.epochStats)
		return currentPolicy
	}

	if c.epochStats == nil {
		c.epochStats = make(map[PolicyType]PolicyStats, len(c.policies))
	}
	if c.tenureStats == nil {
		c.tenureStats = make(map[PolicyType]PolicyStats, len(c.policies))
	}
	c.reportingEpochs++

	// An EpochBandit is handed the whole epoch in one call, so its report is
	// collected here rather than delivered arm by arm. The slice is allocated
	// per epoch and never reused, so the bandit may retain it.
	var report []ShadowStats
	if c.epochBandit != nil {
		report = make([]ShadowStats, 0, len(c.policyOrder))
	}

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

		armStats := ShadowStats{
			Policy: policy.GetType(),
			Hits:   reported.Hits,
			Misses: reported.Misses,
		}

		if c.epochBandit != nil {
			report = append(report, armStats)
			continue
		}
		c.bandit.RecordStats(armStats)
	}

	if c.epochBandit != nil {
		c.epochBandit.RecordEpoch(EpochReport{
			EpochID:    c.epochID,
			Active:     currentPolicy,
			Stats:      report,
			Capacity:   c.nominalCap[currentPolicy],
			SampleRate: c.sampler.rate,
		})
	}

	return c.bandit.SelectPolicy()
}
