package ascache

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrEmptyPolicies = errors.New("must provide non zero policies size")

type EvictCallback[K comparable, V any] func(key K, value V)

type Settings struct {
	EpochDuration time.Duration
	// Run change policy when cache capacity size is full
	EvictPartialCapacityFilling bool
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

type AdaptiveCache[K comparable, V any] struct {
	mu sync.RWMutex

	// --- Data Plane ---
	activePolicy PolicyType
	oldPolicy    PolicyType
	policies     map[PolicyType]Policy[K, V]
	onEvict      EvictCallback[K, V]

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
			changed := c.tryChangePolicy()
			if changed {
				// c.stats.UpdatedPolicy()
			}

			c.epochID++
		}
	}
}

func (c *AdaptiveCache[K, V]) tryChangePolicy() (changed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	currectPolicy := c.activePolicy

	if !c.settings.EvictPartialCapacityFilling &&
		c.policies[currectPolicy].Len() != c.policies[currectPolicy].Cap() {
		return
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

	// 3. Попросить бандита принять решение
	newPolicy := c.bandit.SelectPolicy()

	// 4. Применить решение (переключить "руку")
	if newPolicy != currectPolicy {
		// ВАЖНО: Здесь будет логика "постепенного перелива"
		// или "холодной" замены.
		// Для прототипа просто меняем указатель.
		// log.Printf("MAB Agent: Switching active policy to %s", newPolicyName)
		// нужно переливать данные при операциях обращения к кешу, а не в фоне
		c.activePolicy = newPolicy
		c.oldPolicy = currectPolicy

		changed = true
		// При "холодном" старте мы бы очищали кеш.
		// При "переливе" мы бы запустили процесс миграции.
	}

	return
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
	c.mu.RUnlock()

	// 3. (Опционально) Обновляем глобальную статистику
	// ...

	return val, found
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

	return c.policies[c.activePolicy].Remove(key)
}

func (c *AdaptiveCache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, policy := range c.policies {
		policy.Purge()
	}
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

func (c *AdaptiveCache[K, V]) Close() error {
	c.cancel()
	c.epochTicker.Stop()

	return nil
}
