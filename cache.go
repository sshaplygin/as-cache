package ascache

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrEmptyPolicies = errors.New("empty input policies")

// TODO: add settings for use bandit to selec policies
type Settings struct {
	EpochDuration time.Duration
}

func NewAdaptiveCache[K comparable, V any](
	policies []EvictionPolicy[K, V],
	shadowCaches []ShadowCache[K],
	bandit Bandit,
	settings *Settings,
) (*AdaptiveCache[K, V], error) {
	if len(policies) == 0 {
		return nil, ErrEmptyPolicies
	}

	ctx, cancel := context.WithCancel(context.Background())

	availablePolicies := make(map[string]EvictionPolicy[K, V], len(policies))
	for _, policy := range policies {
		availablePolicies[policy.Name()] = policy
	}

	ac := &AdaptiveCache[K, V]{
		policies:     availablePolicies,
		activePolicy: policies[0],
		bandit:       bandit,
		shadowCaches: shadowCaches,
		epochTicker:  time.NewTicker(settings.EpochDuration),
		ctx:          ctx,
		cancel:       cancel,
	}

	go ac.runAdaptiveSelect()

	return ac, nil
}

type AdaptiveCache[K comparable, V any] struct {
	// Блокировка для Get/Put и смены политики
	mu sync.RWMutex

	// --- Data Plane ---
	activePolicy EvictionPolicy[K, V]
	policies     map[string]EvictionPolicy[K, V]

	// --- Control Plane ---
	bandit       Bandit
	shadowCaches []ShadowCache[K]

	// --- Settings ---
	epochTicker *time.Ticker
	ctx         context.Context
	cancel      context.CancelFunc
}

func (c *AdaptiveCache[K, V]) runAdaptiveSelect() {
	for {
		select {
		case <-c.ctx.Done():
			c.epochTicker.Stop()
			return
		case <-c.epochTicker.C:
			c.mu.Lock()

			for _, shadow := range c.shadowCaches {
				stats := shadow.GetStatsAndReset()
				c.bandit.RecordStats(stats)
			}

			// 3. Попросить бандита принять решение
			newPolicyName := c.bandit.SelectPolicy()

			// 4. Применить решение (переключить "руку")
			if c.policies[newPolicyName] != c.activePolicy {
				// ВАЖНО: Здесь будет логика "постепенного перелива"
				// или "холодной" замены.
				// Для прототипа просто меняем указатель.
				// log.Printf("MAB Agent: Switching active policy to %s", newPolicyName)
				c.activePolicy = c.policies[newPolicyName]

				// При "холодном" старте мы бы очищали кеш.
				// При "переливе" мы бы запустили процесс миграции.
			}

			c.mu.Unlock()
		}
	}
}

func (c *AdaptiveCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	// 1. Симулируем доступ во всех теневых кешах ("сенсоры")
	for _, shadow := range c.shadowCaches {
		shadow.Access(key) // Мы не блокируем Get ради этого
	}

	// 2. Идем в реальный кеш за данными
	val, found := c.activePolicy.Get(key)
	c.mu.RUnlock()

	// 3. (Опционально) Обновляем глобальную статистику
	// ...

	return val, found
}

func (c *AdaptiveCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, shadow := range c.shadowCaches {
		shadow.Access(key)
	}

	// 2. Кладем данные в реальный кеш
	c.activePolicy.Set(key, value)
}

func (c *AdaptiveCache[K, V]) Stats() GlobalStats {
	// ... реализация сбора общей статистики ...
	return GlobalStats{}
}

func (c *AdaptiveCache[K, V]) Del(key K) {
}

func (c *AdaptiveCache[K, V]) Close() error {
	c.cancel()
	c.epochTicker.Stop()

	return nil
}
