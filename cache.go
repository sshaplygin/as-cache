package ascache

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// AdaptiveCache is a cache that automatically selects the best replacement
// policy at runtime using a Multi-Armed Bandit algorithm.
type AdaptiveCache[K comparable, V any] struct {
	mu sync.RWMutex

	// --- Data Plane ---
	activePolicy PolicyType
	policies     map[PolicyType]Policy[K, V]

	// sampler decides which keys shadow policies track. It is shared by every
	// policy so they all measure the same substream, and is fixed for the
	// lifetime of the cache.
	sampler *keySampler[K]

	// nominalCap is each policy's capacity as the caller built it, restored
	// when the policy takes over active duty. shadowCap is the miniature
	// capacity it runs at while shadowing, and minShadowCap is the floor
	// applied when recomputing that capacity after a Resize.
	nominalCap   map[PolicyType]int
	shadowCap    map[PolicyType]int
	minShadowCap int

	// activeSampledHits and activeSampledMisses count the active policy's
	// results for sampled keys only. The bandit is fed these rather than the
	// policy's full counters so that every arm is judged on the same sampled
	// substream, with the same weight of evidence. They are mutated on the
	// read path, so they must be atomic.
	activeSampledHits   atomic.Int64
	activeSampledMisses atomic.Int64

	// globalStats accumulates the hit/miss counts the active policy earned up
	// to the last reporting epoch. Per-policy counters are reset at each
	// reporting epoch after being delivered to the bandit (epochs gated by
	// EvictPartialCapacityFilling skip both the report and the reset), so
	// cumulative totals must be kept here.
	globalStats GlobalStats

	// --- Migration (gradual) ---
	migrating         bool
	migrateFrom       PolicyType
	migrationKeys     []K
	migrationRealKeys map[K]struct{}

	// --- Control Plane ---
	bandit Bandit

	// epochStats holds the per-policy stats measured in the epoch the last
	// report covered, keyed by policy. The switch-stability gates in
	// allowSwitchLocked read it; it is empty on epochs that skipped reporting.
	epochStats map[PolicyType]PolicyStats

	// tenureStats accumulates a policy's measurements for as long as it stays
	// in one role, which is what Advice draws on. Per-epoch counters are reset
	// after each report, so an answer about the traffic has to be accumulated
	// somewhere.
	//
	// It is cleared for both policies involved in a switch. Pooling a policy's
	// active tenure with its shadow tenure would mix two different measurement
	// regimes - full capacity over all traffic against miniature capacity over
	// a sample - and, worse, would leave the just-demoted policy's long good
	// history outweighing the promoted one's short history, so Advice would
	// recommend reverting a switch the cache had just made correctly.
	tenureStats map[PolicyType]PolicyStats

	// reportingEpochs counts only the epochs that actually measured something.
	// epochID counts ticks, including those the capacity gate skipped, and
	// reporting that as the evidence behind a recommendation would overstate
	// it - sometimes by thousands of epochs to none at all.
	reportingEpochs int64

	// lastSwitchEpoch is the epoch in which the active policy last changed,
	// used by the SwitchCooldownEpochs gate.
	lastSwitchEpoch int64

	// --- Settings ---
	epochID     int64
	epochTicker *time.Ticker
	settings    *Settings

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// recordActiveSample counts the active policy's result for a key that is part
// of the measured sample. Unsampled keys are served normally but not counted,
// so the active arm's evidence covers the same substream as every shadow's.
func (c *AdaptiveCache[K, V]) recordActiveSample(sampled, hit bool) {
	if !sampled {
		return
	}

	if hit {
		c.activeSampledHits.Add(1)
	} else {
		c.activeSampledMisses.Add(1)
	}
}

func (c *AdaptiveCache[K, V]) Get(key K) (V, bool) {
	sampled := c.sampler.sampled(key)

	c.mu.RLock()
	if !c.migrating {
		if sampled {
			for _, policy := range c.policies {
				if policy.GetType() == c.activePolicy {
					continue
				}
				policy.Get(key)
			}
		}

		val, found := c.policies[c.activePolicy].Get(key)
		c.mu.RUnlock()
		c.recordActiveSample(sampled, found)

		return val, found
	}
	c.mu.RUnlock()

	// Gradual migration window: resolve the whole lookup under the write lock,
	// promoting an eligible key into the active policy BEFORE its Get is
	// counted. The active policy then records a hit for a request the cache
	// serves; promoting after the Get would leave a spurious miss in the
	// active arm's stats for a served request, skewing both Stats() and the
	// bandit's posterior toward the demoted policy.
	c.mu.Lock()
	defer c.mu.Unlock()

	if sampled {
		for _, policy := range c.policies {
			if policy.GetType() == c.activePolicy {
				continue
			}
			policy.Get(key)
		}
	}

	// Re-check: the window may have closed between the RUnlock and this Lock.
	if c.migrating {
		c.promoteLocked(key)
	}

	val, found := c.policies[c.activePolicy].Get(key)
	c.recordActiveSample(sampled, found)

	return val, found
}

func (c *AdaptiveCache[K, V]) Add(key K, value V) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sampler.sampled(key) {
		for _, policy := range c.policies {
			if policy.GetType() == c.activePolicy {
				continue
			}
			var zeroValue V
			_ = policy.Add(key, zeroValue)
		}
	}

	if c.migrating {
		// The key is about to be written to the active policy with its real
		// value, so it needs no promotion; and if the shadow pass above ran,
		// it just overwrote the value held by the migration source. Either
		// way the key must not be promoted later.
		delete(c.migrationRealKeys, key)
		if len(c.migrationRealKeys) == 0 {
			c.closeMigrationLocked()
		} else {
			// Opportunistically migrate one additional key per Add call.
			c.drainOneKey()
		}
	}

	return c.policies[c.activePolicy].Add(key, value)
}

// Stats returns the cumulative hits and misses served by the cache: totals
// folded up to the last reporting epoch (globalStats) plus the active
// policy's counters accumulated since then.
func (c *AdaptiveCache[K, V]) Stats() GlobalStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ps := c.policies[c.activePolicy].GetStats()
	return GlobalStats{
		Hits:   c.globalStats.Hits + ps.Hits,
		Misses: c.globalStats.Misses + ps.Misses,
	}
}

func (c *AdaptiveCache[K, V]) Remove(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, policy := range c.policies {
		if policy.GetType() == c.activePolicy {
			continue
		}
		policy.Remove(key)
	}

	if c.migrating {
		delete(c.migrationRealKeys, key)
		// Close the window when the last pending key is removed; a lingering
		// window would keep routing every Get through the write lock.
		if len(c.migrationRealKeys) == 0 {
			c.closeMigrationLocked()
		}
	}

	return c.policies[c.activePolicy].Remove(key)
}

func (c *AdaptiveCache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, policy := range c.policies {
		policy.Purge()
	}
	c.closeMigrationLocked()
}

// Resize sets the cache's capacity to size and returns the total number of
// entries evicted across all policies. Shadow policies are resized to the
// miniature capacity that corresponds to size rather than to size itself, so
// they stay faithful simulations of a cache of the requested capacity.
//
// The sample rate itself is fixed for the life of the cache: changing it would
// change which keys are sampled, invalidating every shadow's accumulated state.
// The miniature capacity therefore follows the rate directly here, without the
// MinShadowCapacity floor that construction applies - see scaledCapacity.
func (c *AdaptiveCache[K, V]) Resize(size int) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	shadowSize := scaledCapacity(size, c.sampler.rate)

	evicted := 0
	for policyType, policy := range c.policies {
		c.nominalCap[policyType] = size
		c.shadowCap[policyType] = shadowSize

		target := shadowSize
		// The active policy serves every key, and a policy that is still
		// draining into it under a gradual migration holds the only copy of
		// everything not yet promoted. Shrinking either to miniature capacity
		// would evict real data the cache is still responsible for.
		if policyType == c.activePolicy || (c.migrating && policyType == c.migrateFrom) {
			target = size
		}
		evicted += policy.Resize(target)
	}

	return evicted
}

func (c *AdaptiveCache[K, V]) Contains(key K) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.policies[c.activePolicy].Contains(key)
}

func (c *AdaptiveCache[K, V]) Keys() []K {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.policies[c.activePolicy].Keys()
}

func (c *AdaptiveCache[K, V]) Values() []V {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.policies[c.activePolicy].Values()
}

func (c *AdaptiveCache[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.policies[c.activePolicy].Len()
}

func (c *AdaptiveCache[K, V]) Peek(key K) (value V, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.policies[c.activePolicy].Peek(key)
}

// ActivePolicy returns the PolicyType that is currently serving cache
// operations. It is safe to call concurrently.
func (c *AdaptiveCache[K, V]) ActivePolicy() PolicyType {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.activePolicy
}

// Close stops the background epoch goroutine and waits for it to exit. It is
// idempotent and safe to call concurrently; every call returns nil after the
// goroutine has stopped.
func (c *AdaptiveCache[K, V]) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.wg.Wait()
	})

	return nil
}
