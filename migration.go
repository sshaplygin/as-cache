package ascache

// migrateData transfers key/value pairs from the old active policy to the new
// one according to the configured MigrationStrategy. It always abandons any
// in-progress gradual window first. It must be called while the write lock is
// held.
//
// MigrationCold: purge stale shadow entries from target, start fresh.
// MigrationWarm: purge stale shadow entries from target, copy all key/value pairs.
// MigrationGradual: purge stale shadow entries from target, snapshot key list,
// and open the gradual migration window (unless the source is empty).
func (c *AdaptiveCache[K, V]) migrateData(from, to PolicyType) {
	// Abandon any incomplete gradual migration from the previous epoch.
	c.clearMigrationState()

	switch c.settings.MigrationStrategy {
	// Cold is the default, so it is the default arm rather than a named case.
	// Every strategy has to purge the target's zero-value shadow entries, and
	// a strategy value this switch did not recognise would otherwise fall
	// straight through and skip that - leaving the new active policy holding
	// shadow zeros and serving them to callers as real data, which is the one
	// invariant this library is built on.
	default:
		c.policies[to].Purge()
		return

	case MigrationWarm:
		fromPolicy := c.policies[from]
		toPolicy := c.policies[to]

		// Remove stale zero-value shadow entries so callers never observe a zero
		// value as if it were a real cached result.
		toPolicy.Purge()

		keys := fromPolicy.Keys()
		for _, key := range keys {
			val, ok := fromPolicy.Peek(key)
			if !ok {
				continue
			}
			toPolicy.Add(key, val)
		}

	case MigrationGradual:
		// Remove stale zero-value shadow entries from the new active policy.
		c.policies[to].Purge()

		keys := c.policies[from].Keys()
		if len(keys) == 0 {
			// Nothing to migrate: opening an empty window would only force
			// Gets through the write lock until something closed it.
			return
		}
		realKeys := make(map[K]struct{}, len(keys))
		for _, k := range keys {
			realKeys[k] = struct{}{}
		}

		c.migrating = true
		c.migrateFrom = from
		c.migrationKeys = keys
		c.migrationRealKeys = realKeys
	}
}

// clearMigrationState resets all gradual migration fields. It must be called
// while the write lock is held.
func (c *AdaptiveCache[K, V]) clearMigrationState() {
	c.migrating = false
	c.migrateFrom = Undefined
	c.migrationKeys = nil
	c.migrationRealKeys = nil
}

// drainOneKey migrates one pending key from the migration source policy into
// the current active policy. It must be called while the write lock is held.
func (c *AdaptiveCache[K, V]) drainOneKey() {
	for len(c.migrationKeys) > 0 {
		// Pop from the end (O(1)).
		key := c.migrationKeys[len(c.migrationKeys)-1]
		c.migrationKeys = c.migrationKeys[:len(c.migrationKeys)-1]

		// Skip keys already promoted via Get, or written directly by the
		// caller through Add, which drops them from the pending set.
		if _, ok := c.migrationRealKeys[key]; !ok {
			continue
		}

		val, ok := c.policies[c.migrateFrom].Peek(key)
		if !ok {
			delete(c.migrationRealKeys, key)
			continue
		}

		c.policies[c.activePolicy].Add(key, val)
		delete(c.migrationRealKeys, key)

		// Close the migration window when the last real key is drained.
		if len(c.migrationRealKeys) == 0 {
			c.closeMigrationLocked()
		}
		return
	}

	// Queue exhausted with no promotable keys remaining.
	c.closeMigrationLocked()
}

// promoteLocked moves key from the migration source policy into the current
// active policy if it is still eligible: keys already promoted, or written
// directly by the caller through Add, are skipped. It closes the migration
// window when no eligible keys remain, whichever path emptied the set
// (promotion here or an earlier Remove). It must be called while the write
// lock is held during a gradual migration window.
//
// A note on why the source is trustworthy here. It is not the active policy,
// so anything walking c.policies and skipping only activePolicy would treat it
// as a shadow and fill it with zero values - and the Peek below cannot tell
// such a zero from a real value still pending, so it would promote the zero
// and serve it to a caller as a hit. fanOutReadLocked therefore skips the
// source while a window is open. "Not active" is not the same as "is a
// shadow": for the duration of a gradual window there are three roles, not
// two, and anything iterating the policies has to say what it means to do to
// this one.
func (c *AdaptiveCache[K, V]) promoteLocked(key K) {
	// Skip keys the caller has since written directly, or already promoted.
	if _, ok := c.migrationRealKeys[key]; ok {
		if val, ok := c.policies[c.migrateFrom].Peek(key); ok {
			c.policies[c.activePolicy].Add(key, val)
		}
		delete(c.migrationRealKeys, key)
	}

	if len(c.migrationRealKeys) == 0 {
		c.closeMigrationLocked()
	}
}
