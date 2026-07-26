package ascache

// hitRate returns the fraction of requests that were hits, or 0 when no
// requests were observed.
func hitRate(s PolicyStats) float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}

	return float64(s.Hits) / float64(total)
}

// switchGated reports whether the stability settings are configured to gate
// switches at all. When none are set, AdaptiveCache applies every bandit
// selection, which is the behaviour of a zero-valued Settings.
func (s *Settings) switchGated() bool {
	return s.MinHitRateImprovement > 0 || s.SwitchCooldownEpochs > 0 || s.MinEpochRequests > 0
}

// allowSwitchLocked reports whether the bandit's selection of candidate should
// actually be applied, given the stability settings and the stats measured in
// the epoch that just ended. A rejected switch leaves the active policy in
// place; the bandit still keeps the posterior it learned this epoch, so a
// genuinely better policy wins again on a later epoch.
//
// It must be called while the write lock is held, immediately after
// selectPolicyLocked, which populates epochStats.
func (c *AdaptiveCache[K, V]) allowSwitchLocked(candidate PolicyType) bool {
	if !c.settings.switchGated() {
		return true
	}

	if c.settings.SwitchCooldownEpochs > 0 &&
		c.epochID-c.lastSwitchEpoch < c.settings.SwitchCooldownEpochs {
		return false
	}

	active, okActive := c.epochStats[c.activePolicy]
	cand, okCandidate := c.epochStats[candidate]
	if !okActive || !okCandidate {
		// The epoch produced no comparable measurement (see the
		// EvictPartialCapacityFilling gate in selectPolicyLocked). Hold the
		// current policy rather than switch on no evidence.
		return false
	}

	if c.settings.MinEpochRequests > 0 &&
		(active.Hits+active.Misses < c.settings.MinEpochRequests ||
			cand.Hits+cand.Misses < c.settings.MinEpochRequests) {
		return false
	}

	if c.settings.MinHitRateImprovement > 0 &&
		hitRate(cand)-hitRate(active) < c.settings.MinHitRateImprovement {
		return false
	}

	return true
}
