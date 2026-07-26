package ascache

// demoteLocked puts a policy that has just stopped being active onto shadow
// duty: it releases the policy's hold on real values and shrinks it to the
// miniature capacity it simulates at.
//
// Values are dropped rather than kept because a demoted policy no longer
// serves anyone. Its keys still matter - they are the eviction bookkeeping
// that makes its hit-rate estimate meaningful - so entries are rewritten to
// the zero value instead of being purged. Rewriting in Keys() order preserves
// the ordering the policy maintains: for a recency policy the oldest-to-newest
// walk re-establishes the same recency order, and for a frequency policy every
// surviving key gains exactly one access, which leaves the relative ordering
// untouched.
//
// Keys outside the sample are removed outright, so what remains is the
// substream every other shadow is measuring.
//
// It must be called while the write lock is held, and only after the new state
// has been published, so a reader holding a stale view cannot observe a value
// being dropped and mistake the zero for real data.
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

	// Whatever this policy measured in its previous role was measured at a
	// different capacity, and over all traffic rather than the sample. Carrying
	// those counts into its first shadow epoch would misreport it to the bandit.
	policy.ResetStats()
}

// promoteLockedCapacity restores a policy to its full nominal capacity as it
// takes over active duty. The caller purges it afterwards - a policy arriving
// from shadow duty holds only zero values - so no real data is resized away.
//
// It must be called while the write lock is held.
func (c *AdaptiveCache[K, V]) promoteLockedCapacity(policyType PolicyType) {
	policy, ok := c.policies[policyType]
	if !ok {
		return
	}

	if capacity := c.nominalCap[policyType]; capacity > 0 && policy.Cap() != capacity {
		policy.Resize(capacity)
	}
}

// switchLocked applies a policy change end to end: it restores the incoming
// policy to full capacity, migrates data according to the configured strategy,
// makes it active, and puts the outgoing policy onto shadow duty.
//
// The order of those steps is load-bearing, and the rule generalises:
//
//	Every mutation of a policy must happen while that policy is not the
//	active one.
//
// So the incoming policy is resized and migrated into before it is made
// active, and the outgoing policy has its values dropped only after it has
// stopped being active. Reversing either half would let a caller observe a
// policy mid-rewrite - most damagingly, read a dropped value and take the zero
// for real data. The rule is what keeps that impossible, and it is what any
// future move to lock-free reads would rest on: a reader can only ever hold a
// policy that is not being mutated.
//
// The capacity is restored before migrateData runs for the same reason it is
// restored at all: a warm migration must copy into a full-size policy rather
// than a miniature that would evict most of what it is handed.
//
// Demotion of the outgoing policy is deferred when a gradual window opens,
// because that window serves promotions out of the outgoing policy's real
// values; closeMigrationLocked performs it once the window closes.
//
// It must be called while the write lock is held.
func (c *AdaptiveCache[K, V]) switchLocked(from, to PolicyType) {
	// Abandon any window still open from a previous switch, demoting its
	// source now that nothing will promote out of it again.
	c.closeMigrationLocked()

	c.promoteLockedCapacity(to)
	c.migrateData(from, to)
	c.activePolicy = to

	// Both policies just changed role, so what they measured in the previous
	// one no longer describes them. Advice compares them from here.
	delete(c.tenureStats, from)
	delete(c.tenureStats, to)

	if !c.migrating {
		c.demoteLocked(from)
	}
}

// closeMigrationLocked ends a gradual migration window and puts the source
// policy onto shadow duty, the demotion that was deferred while the window
// still needed the source's real values.
//
// It must be called while the write lock is held.
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

	// The sample must be identical for every shadow or their hit rates are not
	// comparable, so one effective rate is derived from the smallest policy:
	// that is the capacity most at risk of shrinking into noise.
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
