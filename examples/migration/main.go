// Package main demonstrates policy migration in as-cache.
//
// Run with:
//
//	go run . --strategy=cold    # keys lost after switch
//	go run . --strategy=warm    # keys immediately available after switch
//	go run . --strategy=gradual # keys lazily promoted on first access
//
// Endpoints:
//
//	GET  /get?key=K            retrieve a cached value
//	POST /set?key=K&value=V    store a key-value pair
//	GET  /keys                 list all keys in the active policy
//	GET  /stats                active policy, key count, hit/miss stats
//	POST /switch?to=lfu|lru    schedule a policy switch for the next epoch tick
//	GET  /demo                 run the full migration demo and return a report
package main

import (
	"context"
	"encoding/json"
	"flag"
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

// ─── Thompson Sampling reward source ─────────────────────────────────────────

type armStats struct {
	Hits   float64
	Misses float64
}

// cacheRewardSource implements mab.RewardSource. It accumulates per-policy
// hit/miss counters pushed by the AdaptiveCache bandit hooks.
type cacheRewardSource struct {
	mu    sync.RWMutex
	arms  []ascache.PolicyType
	stats map[ascache.PolicyType]*armStats
}

func newCacheRewardSource(arms []ascache.PolicyType) *cacheRewardSource {
	crs := &cacheRewardSource{
		arms:  arms,
		stats: make(map[ascache.PolicyType]*armStats, len(arms)),
	}
	for _, a := range arms {
		crs.stats[a] = &armStats{}
	}
	return crs
}

// GetRewards returns Beta distributions for each arm in arm-index order.
func (crs *cacheRewardSource) GetRewards(_ context.Context, _ interface{}) ([]mab.Dist, error) {
	crs.mu.RLock()
	defer crs.mu.RUnlock()

	dists := make([]mab.Dist, len(crs.arms))
	for i, arm := range crs.arms {
		s := crs.stats[arm]
		dists[i] = mab.Beta(s.Hits+1, s.Misses+1)
	}
	return dists, nil
}

func (crs *cacheRewardSource) update(p ascache.PolicyType, hits, misses int64) {
	crs.mu.Lock()
	defer crs.mu.Unlock()

	if s, ok := crs.stats[p]; ok {
		s.Hits += float64(hits)
		s.Misses += float64(misses)
	}
}

// ─── Bandit adapter ───────────────────────────────────────────────────────────

// stitchfixAdapter wraps the stitchfix/mab Thompson Sampling bandit and
// implements the ascache.Bandit interface.
type stitchfixAdapter struct {
	bandit      *mab.Bandit
	rewardStore *cacheRewardSource
	arms        []ascache.PolicyType
	unitID      string
}

func newStitchfixAdapter(arms []ascache.PolicyType) *stitchfixAdapter {
	rs := newCacheRewardSource(arms)
	return &stitchfixAdapter{
		bandit: &mab.Bandit{
			RewardSource: rs,
			Strategy:     mab.NewThompson(nil),
			Sampler:      mab.NewSha1Sampler(),
		},
		rewardStore: rs,
		arms:        arms,
		unitID:      "migration-example",
	}
}

func (a *stitchfixAdapter) RecordStats(stats ascache.ShadowStats) {
	a.rewardStore.update(stats.Policy, stats.Hits, stats.Misses)
}

func (a *stitchfixAdapter) SelectPolicy() ascache.PolicyType {
	result, err := a.bandit.SelectArm(context.Background(), a.unitID, a.arms)
	if err != nil {
		return a.arms[0]
	}
	return a.arms[result.Arm]
}

// ─── Controllable bandit ──────────────────────────────────────────────────────

// controllableBandit wraps an adaptive bandit and allows the demo server to
// force the next policy selection via the /switch HTTP endpoint.
type controllableBandit struct {
	mu     sync.Mutex
	forced ascache.PolicyType
	inner  *stitchfixAdapter
}

func (b *controllableBandit) RecordStats(stats ascache.ShadowStats) {
	b.inner.RecordStats(stats)
}

func (b *controllableBandit) SelectPolicy() ascache.PolicyType {
	b.mu.Lock()
	forced := b.forced
	b.forced = ascache.Undefined
	b.mu.Unlock()

	if forced != ascache.Undefined {
		return forced
	}
	return b.inner.SelectPolicy()
}

// forceNext makes the next SelectPolicy() call return p regardless of what the
// adaptive algorithm would have chosen.
func (b *controllableBandit) forceNext(p ascache.PolicyType) {
	b.mu.Lock()
	b.forced = p
	b.mu.Unlock()
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// server holds the adaptive cache and exposes HTTP handlers.
type server struct {
	cache    *ascache.AdaptiveCache[string, string]
	bandit   *controllableBandit
	epochDur time.Duration
	strategy ascache.MigrationStrategy
	logger   *log.Logger
}

// statsResponse is the JSON payload returned by GET /stats.
type statsResponse struct {
	ActivePolicy      string `json:"active_policy"`
	MigrationStrategy string `json:"migration_strategy"`
	EpochDuration     string `json:"epoch_duration"`
	KeyCount          int    `json:"key_count"`
	Hits              int64  `json:"hits"`
	Misses            int64  `json:"misses"`
	HitRatePct        string `json:"hit_rate_pct"`
}

// demoReport is the JSON payload returned by GET /demo.
type demoReport struct {
	MigrationStrategy string            `json:"migration_strategy"`
	PolicyBefore      string            `json:"policy_before"`
	PolicyAfter       string            `json:"policy_after"`
	SeededKeys        []string          `json:"seeded_keys"`
	Results           map[string]string `json:"results"`
	SurvivedCount     int               `json:"survived_count"`
	TotalKeys         int               `json:"total_keys"`
	Note              string            `json:"note"`
}

func (s *server) handleGet(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "key query param is required", http.StatusBadRequest)
		return
	}

	val, ok := s.cache.Get(key)
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, val)
}

func (s *server) handleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	key := r.URL.Query().Get("key")
	value := r.URL.Query().Get("value")
	if key == "" || value == "" {
		http.Error(w, "key and value query params are required", http.StatusBadRequest)
		return
	}

	existed := s.cache.Add(key, value)
	if existed {
		fmt.Fprint(w, "updated")
	} else {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, "created")
	}
}

func (s *server) handleKeys(w http.ResponseWriter, r *http.Request) {
	keys := s.cache.Keys()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(keys); err != nil {
		s.logger.Printf("handleKeys encode: %v", err)
	}
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	gs := s.cache.Stats()
	total := gs.Hits + gs.Misses

	hitRate := "N/A"
	if total > 0 {
		hitRate = fmt.Sprintf("%.1f%%", float64(gs.Hits)/float64(total)*100)
	}

	resp := statsResponse{
		ActivePolicy:      policyName(s.cache.ActivePolicy()),
		MigrationStrategy: strategyName(s.strategy),
		EpochDuration:     s.epochDur.String(),
		KeyCount:          s.cache.Len(),
		Hits:              gs.Hits,
		Misses:            gs.Misses,
		HitRatePct:        hitRate,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Printf("handleStats encode: %v", err)
	}
}

func (s *server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	to := r.URL.Query().Get("to")
	var policy ascache.PolicyType
	switch to {
	case "lru", "LRU":
		policy = ascache.LRU
	case "lfu", "LFU":
		policy = ascache.LFU
	default:
		http.Error(w, `to must be "lru" or "lfu"`, http.StatusBadRequest)
		return
	}

	s.bandit.forceNext(policy)
	s.logger.Printf("policy switch to %s scheduled (takes effect on next epoch tick in ≤%s)", to, s.epochDur)
	fmt.Fprintf(w, "policy switch to %s scheduled; takes effect within %s\n", to, s.epochDur)
}

// handleDemo seeds the cache, forces a policy switch, waits one epoch, then
// checks every seeded key and reports which survived migration.
func (s *server) handleDemo(w http.ResponseWriter, r *http.Request) {
	const nKeys = 10

	// Seed keys into the currently active policy.
	policyBefore := policyName(s.cache.ActivePolicy())
	seeds := make([]string, nKeys)
	for i := 0; i < nKeys; i++ {
		key := fmt.Sprintf("demo-key-%02d", i)
		value := fmt.Sprintf("value-%02d", i)
		s.cache.Add(key, value)
		seeds[i] = key
	}
	s.logger.Printf("demo: seeded %d keys under %s", nKeys, policyBefore)

	// Schedule a switch to LFU (or back to LRU if already on LFU).
	target := ascache.LFU
	if policyBefore == "LFU" {
		target = ascache.LRU
	}
	s.bandit.forceNext(target)
	s.logger.Printf("demo: switch to %s scheduled, waiting for epoch tick (~%s)...", policyName(target), s.epochDur)

	// Wait for the epoch tick to fire and for the switch to complete.
	time.Sleep(s.epochDur + 300*time.Millisecond)

	policyAfter := policyName(s.cache.ActivePolicy())
	s.logger.Printf("demo: epoch passed, active policy is now %s", policyAfter)

	// Check every seeded key. For MigrationGradual, the Get call itself
	// triggers lazy promotion from the old policy.
	results := make(map[string]string, nKeys)
	survived := 0
	for _, key := range seeds {
		_, ok := s.cache.Get(key)
		if ok {
			results[key] = "hit"
			survived++
		} else {
			results[key] = "miss"
		}
	}

	note := migrationNote(s.strategy, survived, nKeys)
	s.logger.Printf("demo: %d/%d keys survived with strategy=%s", survived, nKeys, strategyName(s.strategy))

	resp := demoReport{
		MigrationStrategy: strategyName(s.strategy),
		PolicyBefore:      policyBefore,
		PolicyAfter:       policyAfter,
		SeededKeys:        seeds,
		Results:           results,
		SurvivedCount:     survived,
		TotalKeys:         nKeys,
		Note:              note,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Printf("handleDemo encode: %v", err)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func policyName(p ascache.PolicyType) string {
	switch p {
	case ascache.LRU:
		return "LRU"
	case ascache.LFU:
		return "LFU"
	default:
		return "unknown"
	}
}

func strategyName(s ascache.MigrationStrategy) string {
	switch s {
	case ascache.MigrationCold:
		return "cold"
	case ascache.MigrationWarm:
		return "warm"
	case ascache.MigrationGradual:
		return "gradual"
	default:
		return "unknown"
	}
}

func migrationNote(s ascache.MigrationStrategy, survived, total int) string {
	switch s {
	case ascache.MigrationCold:
		return fmt.Sprintf(
			"Cold migration: new policy started empty. %d/%d keys were lost "+
				"(expected: all keys lost on first switch).",
			total-survived, total,
		)
	case ascache.MigrationWarm:
		return fmt.Sprintf(
			"Warm migration: all key/value pairs were copied at switch time. "+
				"%d/%d keys survived immediately.",
			survived, total,
		)
	case ascache.MigrationGradual:
		return fmt.Sprintf(
			"Gradual migration: each Get() promoted a key from the old policy "+
				"on first access. %d/%d keys survived (promoted on demand).",
			survived, total,
		)
	default:
		return ""
	}
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	strategyFlag := flag.String("strategy", "warm", "migration strategy: cold | warm | gradual")
	epochSec := flag.Int("epoch", 5, "epoch duration in seconds")
	addr := flag.String("addr", ":8081", "listen address")
	flag.Parse()

	logger := log.New(os.Stdout, "[migration] ", log.LstdFlags)

	var migrationStrategy ascache.MigrationStrategy
	switch *strategyFlag {
	case "cold":
		migrationStrategy = ascache.MigrationCold
	case "warm":
		migrationStrategy = ascache.MigrationWarm
	case "gradual":
		migrationStrategy = ascache.MigrationGradual
	default:
		logger.Fatalf("unknown strategy %q; use cold, warm, or gradual", *strategyFlag)
	}

	epochDur := time.Duration(*epochSec) * time.Second

	lruCache, err := hlru.New[string, string](100)
	if err != nil {
		logger.Fatalf("LRU init: %v", err)
	}

	lfuCache, err := slfu.New[string, string](100)
	if err != nil {
		logger.Fatalf("LFU init: %v", err)
	}

	arms := []ascache.PolicyType{ascache.LRU, ascache.LFU}
	inner := newStitchfixAdapter(arms)
	bandit := &controllableBandit{inner: inner}

	policies := []ascache.Policy[string, string]{
		ascache.NewCache[string, string](lruCache, ascache.LRU, 100),
		ascache.NewCache[string, string](lfuCache, ascache.LFU, 100),
	}

	cache, err := ascache.NewAdaptiveCache(
		policies,
		bandit,
		&ascache.Settings{
			EpochDuration:               epochDur,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           migrationStrategy,
		},
	)
	if err != nil {
		logger.Fatalf("cache init: %v", err)
	}
	defer cache.Close()

	logger.Printf("strategy=%s  epoch=%s  addr=%s", *strategyFlag, epochDur, *addr)

	s := &server{
		cache:    cache,
		bandit:   bandit,
		epochDur: epochDur,
		strategy: migrationStrategy,
		logger:   logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/get", s.handleGet)
	mux.HandleFunc("/set", s.handleSet)
	mux.HandleFunc("/keys", s.handleKeys)
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/switch", s.handleSwitch)
	mux.HandleFunc("/demo", s.handleDemo)

	// The /demo endpoint sleeps for up to epochDur, so timeouts must be larger.
	timeout := epochDur*2 + 10*time.Second

	httpServer := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Printf("listening — try: curl %s/demo", *addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Panicf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop

	logger.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Panicf("shutdown: %v", err)
	}
	logger.Println("stopped")
}
