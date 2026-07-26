package ascache

func (c *AdaptiveCache[K, V]) runAdaptiveSelect() {
	defer c.wg.Done()
	defer c.epochTicker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.epochTicker.C:
			c.runEpoch()
		}
	}
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
	if c.activePolicy != newPolicy && c.allowSwitchLocked(newPolicy) {
		c.switchLocked(c.activePolicy, newPolicy)
		c.lastSwitchEpoch = c.epochID
	}

	c.epochID++
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

	if !c.settings.EvictPartialCapacityFilling &&
		c.policies[currentPolicy].Len() != c.policies[currentPolicy].Cap() {
		// Nothing was measured this epoch: drop the previous epoch's numbers
		// so the stability gates never compare against stale evidence.
		clear(c.epochStats)
		return currentPolicy
	}

	if c.epochStats == nil {
		c.epochStats = make(map[PolicyType]PolicyStats, len(c.policies))
	}

	for _, policy := range c.policies {
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

		c.bandit.RecordStats(ShadowStats{
			Policy: policy.GetType(),
			Hits:   reported.Hits,
			Misses: reported.Misses,
		})
	}

	return c.bandit.SelectPolicy()
}
