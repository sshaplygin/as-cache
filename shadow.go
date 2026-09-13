package ascache

// fanOutReadLocked feeds one lookup to every shadow policy. A shadow that
// misses fills itself with the zero value, which is what makes its hit rate
// describe the policy rather than the incumbent's miss stream -- see
// docs/design.md, which records how far that measurement drifts without it.
//
// Caller must hold at least the read lock. Each policy is independently
// synchronised, so mutating one here is safe.
func (c *AdaptiveCache[K, V]) fanOutReadLocked(key K) {
	for _, policy := range c.policies {
		if policy.GetType() == c.activePolicy {
			continue
		}

		// The source of an open gradual window is not a shadow: it still holds
		// every value not yet promoted, and promoteLocked reads those back with
		// Peek, which cannot tell a real pending value from a zero written
		// here. Filling it would promote that zero and serve it as a hit.
		if c.migrating && policy.GetType() == c.migrateFrom {
			continue
		}

		if _, hit := policy.Get(key); !hit {
			var zeroValue V
			_ = policy.Add(key, zeroValue)
		}
	}
}

// demoteLocked puts a policy that has just stopped being active onto shadow
// duty: entries are rewritten to the zero value, keys outside the sample are
// removed, and the policy shrinks to its miniature capacity. Keys survive
// because they are the eviction bookkeeping its hit-rate estimate rests on.
//
// The rewrite walks Keys() so a recency policy re-establishes the same order
// and a frequency policy gains one access on every surviving key, leaving the
// relative order intact. For an LRU that depends on Keys() running oldest to
// newest, which golang-lru does not promise; policies'
// TestKeysOrder_LRUIsOldestToNewest pins it. The reasoning does not hold for
// the FIFO-queue policies; docs/policies.md records what demotion costs them.
//
// Caller must hold the write lock, and must have published the new state
// first, so no reader can observe a value being dropped.
func (c *AdaptiveCache[K, V]) demoteLocked(policyType PolicyType) {
	policy, ok := c.policies[policyType]
	if !ok {
		return
	}

	var zero V
	for _, key := range policy.Keys() {
		if c.sampler.sampled(key) {
			policy.Add(key, zero)
			continue
		}
		policy.Remove(key)
	}

	if capacity := c.shadowCap[policyType]; capacity > 0 {
		policy.Resize(capacity)
	}

	// The previous role measured a different capacity over different traffic.
	policy.ResetStats()
}

// promoteLockedCapacity restores a policy to its nominal capacity as it takes
// over active duty. The caller purges it afterwards -- a policy arriving from
// shadow duty holds only zero values -- so no real data is resized away.
// Caller must hold the write lock.
func (c *AdaptiveCache[K, V]) promoteLockedCapacity(policyType PolicyType) {
	policy, ok := c.policies[policyType]
	if !ok {
		return
	}

	if capacity := c.nominalCap[policyType]; capacity > 0 && policy.Cap() != capacity {
		policy.Resize(capacity)
	}
}

// switchLocked applies a policy change end to end: restore the incoming
// policy's capacity, migrate data into it, make it active, put the outgoing
// policy onto shadow duty.
//
// The order is load-bearing, and the rule generalises:
//
//	Every mutation of a policy must happen while that policy is not the
//	active one.
//
// Reverse either half and a caller can read a policy mid-rewrite, most
// damagingly taking a dropped value's zero for real data. Capacity is restored
// before migrateData so a warm migration copies into a full-size policy rather
// than a miniature that would evict most of what it is handed.
//
// Caller must hold the write lock.
func (c *AdaptiveCache[K, V]) switchLocked(from, to PolicyType) {
	// Abandon any window still open from a previous switch, demoting its
	// source now that nothing will promote out of it again.
	c.closeMigrationLocked()

	c.promoteLockedCapacity(to)
	c.migrateData(from, to)
	c.activePolicy = to

	// Both changed role, so neither's previous measurements describe it now.
	delete(c.tenureStats, from)
	delete(c.tenureStats, to)

	// The active arm's samples are counted on the cache rather than on a
	// policy, and everything counted since the last collection was served by
	// from -- including every Get that arrived while the bandit was deciding.
	// Left in place, the next epoch would report it as to's evidence. It is
	// dropped, as demotion drops from's own counters.
	c.activeSampledHits.Store(0)
	c.activeSampledMisses.Store(0)

	if !c.migrating {
		c.demoteLocked(from)
	}
}

// closeMigrationLocked ends a gradual migration window and demotes the source,
// which switchLocked deferred while the window still needed its real values.
// Caller must hold the write lock.
func (c *AdaptiveCache[K, V]) closeMigrationLocked() {
	source, wasMigrating := c.migrateFrom, c.migrating
	c.clearMigrationState()

	if wasMigrating && source != Undefined && source != c.activePolicy {
		c.demoteLocked(source)
	}
}

// initShadowDutyLocked records each policy's nominal capacity, computes the
// miniature capacity it runs at while shadowing, and puts every policy except
// the initially active one onto shadow duty. It runs once, during
// construction, before the cache is reachable by any caller.
func (c *AdaptiveCache[K, V]) initShadowDutyLocked(rate float64, minCapacity int) {
	c.nominalCap = make(map[PolicyType]int, len(c.policies))
	c.shadowCap = make(map[PolicyType]int, len(c.policies))

	// One rate for every shadow, or their hit rates are not comparable. It is
	// derived from the smallest policy, the one most at risk of shrinking into
	// noise.
	minNominal := 0
	for policyType, policy := range c.policies {
		capacity := policy.Cap()
		c.nominalCap[policyType] = capacity
		if capacity > 0 && (minNominal == 0 || capacity < minNominal) {
			minNominal = capacity
		}
	}

	_, effectiveRate := shadowCapacity(minNominal, rate, minCapacity)
	c.sampler = newKeySampler[K](effectiveRate)

	for policyType := range c.policies {
		capacity, _ := shadowCapacity(c.nominalCap[policyType], effectiveRate, minCapacity)
		c.shadowCap[policyType] = capacity

		if policyType != c.activePolicy {
			c.demoteLocked(policyType)
		}
	}
}
