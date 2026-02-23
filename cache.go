package ascache

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrEmptyPolicies = errors.New("must provide non zero policies size")

// Settings configures the behaviour of AdaptiveCache.
type Settings struct {
	EpochDuration time.Duration
	// EvictPartialCapacityFilling allows policy switching even when the cache
	// is not yet full.
	EvictPartialCapacityFilling bool
	// MigrationStrategy determines how data is moved when the active policy
	// changes. Defaults to MigrationCold (zero value).
	MigrationStrategy MigrationStrategy
}

func NewAdaptiveCache[K comparable, V any](
	policies []Policy[K, V],
	bandit Bandit,
	settings *Settings,
) (*AdaptiveCache[K, V], error) {
	if len(policies) == 0 {
		return nil, ErrEmptyPolicies
	}

	ctx, cancel := context.WithCancel(context.Background())

	availablePolicies := make(map[PolicyType]Policy[K, V], len(policies))
	for _, policy := range policies {
		availablePolicies[policy.GetType()] = policy
	}

	ac := &AdaptiveCache[K, V]{
		policies:     availablePolicies,
		activePolicy: policies[0].GetType(),
		bandit:       bandit,
		epochTicker:  time.NewTicker(settings.EpochDuration),
		ctx:          ctx,
		cancel:       cancel,
		settings:     settings,
	}

	go ac.runAdaptiveSelect()

	return ac, nil
}

// AdaptiveCache is a cache that automatically selects the best replacement
// policy at runtime using a Multi-Armed Bandit algorithm.
type AdaptiveCache[K comparable, V any] struct {
	mu sync.RWMutex

	// --- Data Plane ---
	activePolicy PolicyType
	policies     map[PolicyType]Policy[K, V]

	// --- Migration (gradual) ---
	migrating         bool
	migrateFrom       PolicyType
	migrationKeys     []K
	migrationRealKeys map[K]struct{}

	// --- Control Plane ---
	bandit Bandit

	// --- Settings ---
	epochID     int64
	epochTicker *time.Ticker
	settings    *Settings

	ctx    context.Context
	cancel context.CancelFunc
}

func (c *AdaptiveCache[K, V]) runAdaptiveSelect() {
	for {
		select {
		case <-c.ctx.Done():
			c.epochTicker.Stop()
			return
		case <-c.epochTicker.C:
			newPolicy := c.tryChangePolicy()
			if c.activePolicy != newPolicy {
				c.migrateData(c.activePolicy, newPolicy)
				c.activePolicy = newPolicy
			}

			c.epochID++
		}
	}
}

func (c *AdaptiveCache[K, V]) tryChangePolicy() PolicyType {
	c.mu.Lock()
	defer c.mu.Unlock()

	currentPolicy := c.activePolicy

	if !c.settings.EvictPartialCapacityFilling &&
		c.policies[currentPolicy].Len() != c.policies[currentPolicy].Cap() {
		return currentPolicy
	}

	for _, policy := range c.policies {
		if policy.GetType() == c.activePolicy {
			continue
		}

		stats := policy.GetStats()
		policy.ResetStats()

		c.bandit.RecordStats(ShadowStats{
			Policy: policy.GetType(),
			Hits:   stats.Hits,
			Misses: stats.Misses,
		})
	}

	return c.bandit.SelectPolicy()
}

// migrateData transfers key/value pairs from the old active policy to the new
// one according to the configured MigrationStrategy. It must be called while
// the write lock is held.
//
// MigrationCold: no-op.
// MigrationWarm: purge stale shadow entries from target, copy all key/value pairs.
// MigrationGradual: purge stale shadow entries from target, snapshot key list,
// and set up the gradual migration window.
func (c *AdaptiveCache[K, V]) migrateData(from, to PolicyType) {
	// Abandon any incomplete gradual migration from the previous epoch.
	c.clearMigrationState()

	switch c.settings.MigrationStrategy {
	case MigrationCold:
		// Purge zero-value shadow entries so callers never observe a cached
		// zero as if it were a real value.
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

		// Skip keys already promoted via Get or overwritten by a shadow Add.
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
			c.clearMigrationState()
		}
		return
	}

	// Queue exhausted with no promotable keys remaining.
	c.clearMigrationState()
}

// tryPromote attempts to move key from the migration source policy into the
// current active policy. It acquires a write lock and must NOT be called while
// any lock is held.
func (c *AdaptiveCache[K, V]) tryPromote(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check: migration may have ended between the RUnlock and this Lock.
	if !c.migrating {
		var zero V
		return zero, false
	}

	// Skip keys whose values have been overwritten by a shadow Add.
	if _, ok := c.migrationRealKeys[key]; !ok {
		var zero V
		return zero, false
	}

	val, ok := c.policies[c.migrateFrom].Peek(key)
	if !ok {
		delete(c.migrationRealKeys, key)
		var zero V
		return zero, false
	}

	c.policies[c.activePolicy].Add(key, val)
	delete(c.migrationRealKeys, key)

	return val, true
}

func (c *AdaptiveCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	for _, policy := range c.policies {
		if policy.GetType() == c.activePolicy {
			continue
		}
		policy.Get(key)
	}

	val, found := c.policies[c.activePolicy].Get(key)
	migrating := c.migrating
	c.mu.RUnlock()

	if found || !migrating {
		return val, found
	}

	// Miss in the new active policy during a gradual migration window: attempt
	// to promote the key from the old active policy.
	return c.tryPromote(key)
}

func (c *AdaptiveCache[K, V]) Add(key K, value V) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, policy := range c.policies {
		if policy.GetType() == c.activePolicy {
			continue
		}
		var zeroValue V
		_ = policy.Add(key, zeroValue)
	}

	if c.migrating {
		// The shadow Add above just overwrote this key's real value in the
		// migration source. Mark it as corrupted so it is never promoted.
		delete(c.migrationRealKeys, key)
		// Opportunistically migrate one additional key per Add call.
		c.drainOneKey()
	}

	return c.policies[c.activePolicy].Add(key, value)
}

func (c *AdaptiveCache[K, V]) Stats() GlobalStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ps := c.policies[c.activePolicy].GetStats()
	return GlobalStats{
		Hits:   ps.Hits,
		Misses: ps.Misses,
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
	}

	return c.policies[c.activePolicy].Remove(key)
}

func (c *AdaptiveCache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, policy := range c.policies {
		policy.Purge()
	}
	c.clearMigrationState()
}

func (c *AdaptiveCache[K, V]) Resize(size int) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	evicted := 0
	for _, policy := range c.policies {
		evicted += policy.Resize(size)
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

func (c *AdaptiveCache[K, V]) Close() error {
	c.cancel()

	return nil
}
