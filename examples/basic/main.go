package main

import (
	"context"
	"fmt"
	"sync"

	ascache "github.com/sshaplygin/as-cache"
	slfu "github.com/sshaplygin/as-cache/lfu"

	hlru "github.com/hashicorp/golang-lru/v2"
	"github.com/stitchfix/mab"
)

func main() {
	// policiesList := []string{"lru", "lfu"}

	var policiesList []ascache.EvictionPolicy[string, string]
	lruCache, err := hlru.New[string, string](100)
	if err != nil {
		panic(err)
	}

	policiesList = append(policiesList, lruCache)

	lfuCache, err := slfu.New[string, string](100)
	if err != nil {
		panic(err)
	}

	policiesList = append(policiesList, lfuCache)

	armNames := []string{"lru", "lfu"}

	myBandit := NewThompsonBanditAdapter(
		armNames,
	)

	cache, err := ascache.NewAdaptiveCache(
		policiesList,
		nil,
		// []ShadowCache[string]{lruShadow, lfuShadow},
		myBandit,
		&ascache.Settings{},
	)
	if err != nil {
		panic(err)
	}

	defer cache.Close()
}

func NewThompsonBanditAdapter(armNames []string) *StitchFixBanditAdapter {
	rewardStore := NewCacheRewardSource(armNames)

	return &StitchFixBanditAdapter{
		bandit: &mab.Bandit{
			RewardSource: rewardStore,
			Strategy:     mab.NewThompson(nil),
			Sampler:      mab.NewSha1Sampler(),
		},
		rewardStore: rewardStore,
		armNames:    armNames,
		unitID:      "adaptive-cache-system",
	}
}

type armStats struct {
	// Для Бета-распределения: Alpha = Успехи + 1, Beta = Неудачи + 1
	// Мы будем хранить Hits (успехи) и Misses (неудачи).
	Hits   float64
	Misses float64
}

// CacheRewardSource — это наша реализация интерфейса mab.RewardSource.
// Она будет хранить статистику, которую ей "скармливает" наш MAB-агент.
type CacheRewardSource struct {
	mu    sync.RWMutex
	stats map[string]*armStats // "lru" -> {hits, misses}
}

// NewCacheRewardSource создает наш источник наград.
func NewCacheRewardSource(armNames []string) *CacheRewardSource {
	crs := &CacheRewardSource{
		stats: make(map[string]*armStats, len(armNames)),
	}
	for _, name := range armNames {
		crs.stats[name] = &armStats{}
	}
	return crs
}

// GetRewards — это "Pull" метод. stitchfix/mab вызывает его,
// когда ему нужно принять решение.
func (crs *CacheRewardSource) GetRewards(ctx context.Context, unit string, arms []string) (map[string]mab.Distribution, error) {
	crs.mu.RLock()
	defer crs.mu.RUnlock()

	distributions := make(map[string]mab.Distribution, len(arms))
	for _, arm := range arms {
		s, ok := crs.stats[arm]
		if !ok {
			return nil, fmt.Errorf("неизвестная 'рука': %s", arm)
		}

		// Мы используем Бета-распределение (mab.Beta),
		// т.к. оно идеально моделирует Hit Rate (вероятность успеха).
		// Alpha = Hits + 1, Beta = Misses + 1
		// (мы добавляем 1, чтобы избежать деления на ноль
		// и для корректной байесовской аппроксимации).
		distributions[arm] = mab.Beta(s.Hits+1, s.Misses+1)
	}

	return distributions, nil
}

// updateStats — это наш "Push" метод, который будет
// вызываться из MAB-агента.
func (crs *CacheRewardSource) updateStats(policyName string, hits, misses int64) {
	crs.mu.Lock()
	defer crs.mu.Unlock()

	s, ok := crs.stats[policyName]
	if !ok {
		// Такого быть не должно, если мы правильно инициализировали
		return
	}
	s.Hits += float64(hits)
	s.Misses += float64(misses)
}

//====================================================================
// 2. АДАПТЕР, РЕАЛИЗУЮЩИЙ НАШ ИНТЕРФЕЙС `Bandit`
//====================================================================

// StitchFixBanditAdapter оборачивает бандита из stitchfix
// и реализует наш простой интерфейс.
type StitchFixBanditAdapter struct {
	bandit      *mab.Bandit
	rewardStore *CacheRewardSource
	armNames    []string
	// "unit" — это ID для детерминированного выбора.
	// Т.к. у нас один "системный" кеш, мы можем
	// использовать одну и ту же константу.
	unitID string
}

// RecordStats — реализует наш интерфейс Bandit ("Push").
// Он принимает статистику из ShadowCache и кладет ее в RewardSource.
func (s *StitchFixBanditAdapter) RecordStats(stats ShadowStats) {
	s.rewardStore.updateStats(stats.PolicyName, stats.Hits, stats.Misses)
}

// SelectPolicy — реализует наш интерфейс Bandit ("Pull").
// Он просит бандита stitchfix выбрать лучшую руку,
// который, в свою очередь, опрашивает наш rewardStore.
func (s *StitchFixBanditAdapter) SelectPolicy() (policyName string) {
	// Мы передаем контекст, ID юнита и список всех доступных "рук"
	selectedArm, err := s.bandit.SelectArm(context.Background(), s.unitID, s.armNames)
	if err != nil {
		// В случае ошибки (например, rewardStore вернул ошибку),
		// возвращаем первую руку как fallback.
		return s.armNames[0]
	}
	return selectedArm.Arm
}
