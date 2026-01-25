package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	ascache "github.com/sshaplygin/as-cache"
	slfu "github.com/sshaplygin/as-cache/lfu"

	hlru "github.com/hashicorp/golang-lru/v2"
	"github.com/stitchfix/mab"
)

type UserProfile struct {
	Name      string
	Email     string
	CreatedAt time.Time
}

func main() {
	lruCache, err := hlru.New[string, *UserProfile](100)
	if err != nil {
		panic(err)
	}

	policiesList := []ascache.Policy[string, *UserProfile]{
		ascache.NewCache[string, *UserProfile](lruCache, ascache.LRU, 100),
	}

	lfuCache, err := slfu.New[string, *UserProfile](100)
	if err != nil {
		panic(err)
	}

	policiesList = append(policiesList, ascache.NewCache[string, *UserProfile](lfuCache, ascache.LFU, 100))

	armNames := []ascache.PolicyType{ascache.LRU, ascache.LFU}

	myBandit := NewThompsonBanditAdapter(
		armNames,
	)

	cache, err := ascache.NewAdaptiveCache(
		policiesList,
		myBandit,
		&ascache.Settings{
			EpochDuration: 5 * time.Minute,
		},
	)
	if err != nil {
		panic(err)
	}

	defer cache.Close()

	val, ok := cache.Get("key")
	fmt.Println(val, ok)

	mux := http.NewServeMux()

	mux.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		val, ok := cache.Get(key)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Write([]byte(val.Name))
	})

	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		name := r.URL.Query().Get("name")
		if name == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		email := r.URL.Query().Get("email")
		if email == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		_ = cache.Add(key, &UserProfile{})
		w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Println("server started on :8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Panicf("error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	<-stop
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Panicf("shutdown error: %v", err)
	}

	log.Println("server stopped")
}

func NewThompsonBanditAdapter(armNames []ascache.PolicyType) *StitchFixBanditAdapter {
	rewardStore := NewCacheRewardSource(armNames)

	return &StitchFixBanditAdapter{
		bandit: &mab.Bandit{
			RewardSource: rewardStore,
			Strategy:     mab.NewThompson(nil),
			Sampler:      mab.NewSha1Sampler(),
		},
		rewardStore: rewardStore,
		armNames:    armNames,
		unitID:      "adaptive-selection-cache",
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
	stats map[ascache.PolicyType]*armStats
}

func NewCacheRewardSource(armNames []ascache.PolicyType) *CacheRewardSource {
	crs := &CacheRewardSource{
		stats: make(map[ascache.PolicyType]*armStats, len(armNames)),
	}
	for _, name := range armNames {
		crs.stats[name] = &armStats{}
	}
	return crs
}

// GetRewards — это "Pull" метод. stitchfix/mab вызывает его,
// когда ему нужно принять решение.
func (crs *CacheRewardSource) GetRewards(ctx context.Context, banditContext interface{}) ([]mab.Dist, error) {
	crs.mu.RLock()
	defer crs.mu.RUnlock()

	distributions := make([]mab.Dist, len(crs.stats))
	for i, arm := range crs.stats {
		distributions[i] = mab.Beta(arm.Hits+1, arm.Misses+1)
	}

	return distributions, nil
}

// updateStats — это наш "Push" метод, который будет
// вызываться из MAB-агента.
func (crs *CacheRewardSource) updateStats(policy ascache.PolicyType, hits, misses int64) {
	crs.mu.Lock()
	defer crs.mu.Unlock()

	s, ok := crs.stats[policy]
	if !ok {
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
	armNames    []ascache.PolicyType
	// "unit" — это ID для детерминированного выбора.
	// Т.к. у нас один "системный" кеш, мы можем
	// использовать одну и ту же константу.
	unitID string
}

// RecordStats — реализует наш интерфейс Bandit ("Push").
// Он принимает статистику из ShadowCache и кладет ее в RewardSource.
func (s *StitchFixBanditAdapter) RecordStats(stats ascache.ShadowStats) {
	s.rewardStore.updateStats(stats.Policy, stats.Hits, stats.Misses)
}

// SelectPolicy — реализует наш интерфейс Bandit ("Pull").
// Он просит бандита stitchfix выбрать лучшую руку,
// который, в свою очередь, опрашивает наш rewardStore.
func (s *StitchFixBanditAdapter) SelectPolicy() ascache.PolicyType {
	// Мы передаем контекст, ID юнита и список всех доступных "рук"
	selectedArm, err := s.bandit.SelectArm(context.Background(), s.unitID, s.armNames)
	if err != nil {
		// В случае ошибки (например, rewardStore вернул ошибку),
		// возвращаем первую руку как fallback.
		return s.armNames[0]
	}

	return s.armNames[selectedArm.Arm]
}
