package bandit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
)

// replica is one simulated cache: a bandit driven by hand, plus the traffic
// pattern its arms measure.
type replica struct {
	bandit *Distributed
	active ascache.PolicyType
}

// report feeds one epoch's measurements, expressed as a hit rate per arm.
func (r *replica) report(t *testing.T, requests int64, rates map[ascache.PolicyType]float64) {
	t.Helper()

	arms := make([]ascache.PolicyType, 0, len(rates))
	for policy := range rates {
		arms = append(arms, policy)
	}
	// EpochReport documents its Stats as PolicyType-ordered, and the regime
	// fingerprint depends on that order, so a test must honour it too.
	sortPolicies(arms)

	stats := make([]ascache.ShadowStats, 0, len(arms))
	for _, policy := range arms {
		hits := int64(float64(requests) * rates[policy])
		stats = append(stats, ascache.ShadowStats{
			Policy: policy,
			Hits:   hits,
			Misses: requests - hits,
		})
	}

	r.bandit.RecordEpoch(ascache.EpochReport{
		Active:     r.active,
		Stats:      stats,
		Capacity:   1000,
		SampleRate: 1,
	})
}

func sortPolicies(arms []ascache.PolicyType) {
	for i := 1; i < len(arms); i++ {
		for j := i; j > 0 && arms[j] < arms[j-1]; j-- {
			arms[j], arms[j-1] = arms[j-1], arms[j]
		}
	}
}

// fleet builds n replicas sharing one store, each with its own bandit, and
// with the coordination goroutine left unstarted so the test drives sync
// rounds itself.
func fleet(t *testing.T, n int, store Store, clock *testClock, tune func(*Config)) []*replica {
	t.Helper()

	replicas := make([]*replica, 0, n)
	for i := range n {
		cfg := Config{
			Store:             store,
			Namespace:         "test",
			NodeID:            string(rune('a' + i)),
			CoordinationEpoch: testEpoch,
			Seed:              uint64(i + 1),
			Now:               clock.now,
			Jitter:            0,
		}
		if tune != nil {
			tune(&cfg)
		}

		bandit, err := newDistributed(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = bandit.Close() })

		replicas = append(replicas, &replica{bandit: bandit, active: ascache.LRU})
	}

	return replicas
}

func newFleetStore(t *testing.T) (*MemStore, *testClock) {
	t.Helper()

	clock := newTestClock()
	store := NewMemStore()
	store.SetClock(clock.now)
	t.Cleanup(func() { _ = store.Close() })

	return store, clock
}

// ---------------------------------------------------------------------------
// The contract that matters most: nothing here touches the network
// ---------------------------------------------------------------------------

// blockingStore blocks forever on every call. A bandit that did its I/O on the
// cache's path would deadlock the cache against it; this one does not, so the
// test simply completes.
type blockingStore struct {
	entered chan struct{}
	once    sync.Once
}

func (s *blockingStore) enter(ctx context.Context) error {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()

	return ctx.Err()
}

func (s *blockingStore) Sync(ctx context.Context, _ SyncRequest) (SyncResult, error) {
	return SyncResult{}, s.enter(ctx)
}

func (s *blockingStore) Window(ctx context.Context, _ string, _, _ Bucket) ([]WindowCounts, error) {
	return nil, s.enter(ctx)
}

func (s *blockingStore) Decide(
	ctx context.Context,
	_ string,
	_ Bucket,
	_ ascache.PolicyType,
	_ time.Duration,
) (ascache.PolicyType, error) {
	return ascache.Undefined, s.enter(ctx)
}

func (s *blockingStore) Close() error { return nil }

func TestDistributed_RecordAndSelectNeverWaitOnTheStore(t *testing.T) {
	// The cache calls both of these while holding its write lock, so a round
	// trip on either path stalls every Get in the process. This is the
	// invariant the whole design is arranged around.
	store := &blockingStore{entered: make(chan struct{})}
	clock := newTestClock()

	bandit, err := newDistributed(Config{
		Store:             store,
		Namespace:         "test",
		CoordinationEpoch: testEpoch,
		SyncTimeout:       time.Hour,
		Now:               clock.now,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bandit.Close() })

	r := &replica{bandit: bandit, active: ascache.LRU}
	r.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.9})

	// Start a sync and wait until it is genuinely inside the store and stuck.
	syncing := make(chan struct{})
	go func() {
		defer close(syncing)
		bandit.sync()
	}()
	<-store.entered

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			r.report(t, 10, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.9})
			bandit.SelectPolicy()
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RecordEpoch or SelectPolicy blocked behind an in-flight store call")
	}

	require.NoError(t, bandit.Close())
	<-syncing
}

// ---------------------------------------------------------------------------
// Leader mode
// ---------------------------------------------------------------------------

func TestDistributed_FleetConvergesOnTheBetterArm(t *testing.T) {
	store, clock := newFleetStore(t)
	replicas := fleet(t, 8, store, clock, nil)

	// Each replica sees a twentieth of the traffic: 50 requests an epoch is
	// too thin to rank arms locally, which is the whole reason to pool.
	rates := map[ascache.PolicyType]float64{ascache.LRU: 0.40, ascache.TinyLFU: 0.75}

	for range 12 {
		for _, r := range replicas {
			r.report(t, 50, rates)
			r.bandit.sync()
		}
		clock.advance(testEpoch)
	}

	for _, r := range replicas {
		assert.Equal(t, ascache.TinyLFU, r.bandit.SelectPolicy(),
			"every replica should be running the arm the fleet's evidence favours")
	}
}

func TestDistributed_FleetRunsOnePolicyAtATime(t *testing.T) {
	store, clock := newFleetStore(t)
	replicas := fleet(t, 6, store, clock, nil)

	// Arms close enough together that exploration keeps moving the decision.
	rates := map[ascache.PolicyType]float64{ascache.LRU: 0.50, ascache.TinyLFU: 0.51}

	for range 15 {
		for _, r := range replicas {
			r.report(t, 200, rates)
			r.bandit.sync()
		}
		clock.advance(testEpoch)

		// Everyone syncs again in the new bucket so the leader's decision has
		// reached them all.
		for _, r := range replicas {
			r.bandit.sync()
		}

		selections := make(map[ascache.PolicyType]int)
		for _, r := range replicas {
			selections[r.bandit.SelectPolicy()]++
		}
		assert.Len(t, selections, 1,
			"under leader election the fleet applies one decision: %v", selections)
	}
}

func TestDistributed_ExactlyOneReplicaLeadsEachBucket(t *testing.T) {
	store, clock := newFleetStore(t)
	replicas := fleet(t, 10, store, clock, nil)

	const buckets = 6
	for range buckets {
		for _, r := range replicas {
			r.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})
			r.bandit.sync()
		}
		clock.advance(testEpoch)
	}

	total := int64(0)
	for _, r := range replicas {
		total += r.bandit.Snapshot().Leaderships
	}
	assert.Equal(t, int64(buckets), total)
}

func TestDistributed_LeaderAppliesItsOwnDecisionImmediately(t *testing.T) {
	store, clock := newFleetStore(t)
	replicas := fleet(t, 3, store, clock, nil)

	for range 4 {
		for _, r := range replicas {
			r.report(t, 500, map[ascache.PolicyType]float64{ascache.LRU: 0.2, ascache.TinyLFU: 0.9})
			r.bandit.sync()
		}
		clock.advance(testEpoch)
	}

	// Whichever replica led last must already be on the decision it published,
	// not waiting a further round to read it back.
	led := false
	for _, r := range replicas {
		snapshot := r.bandit.Snapshot()
		if snapshot.Leaderships == 0 {
			continue
		}
		led = true
		assert.Positive(t, snapshot.Decisions, "%s led but applied nothing", snapshot.NodeID)
	}
	require.True(t, led, "no replica ever led")
}

// ---------------------------------------------------------------------------
// Shared-posterior mode
// ---------------------------------------------------------------------------

func TestDistributed_SharedPosteriorSelectsWithoutALeader(t *testing.T) {
	store, clock := newFleetStore(t)
	replicas := fleet(t, 6, store, clock, func(cfg *Config) {
		cfg.Mode = ModeSharedPosterior
	})

	rates := map[ascache.PolicyType]float64{ascache.LRU: 0.30, ascache.TinyLFU: 0.80}

	for range 10 {
		for _, r := range replicas {
			r.report(t, 100, rates)
			r.bandit.sync()
		}
		clock.advance(testEpoch)
	}

	for _, r := range replicas {
		snapshot := r.bandit.Snapshot()
		assert.Zero(t, snapshot.Leaderships, "shared-posterior mode elects nobody")
		assert.Equal(t, ascache.TinyLFU.String(), snapshot.Selection)
		assert.NotEmpty(t, snapshot.Fleet, "every replica reads the window for itself")
	}
}

func TestDistributed_SharedPosteriorRejectsShadowOnlyUnderLeader(t *testing.T) {
	_, err := NewDistributed(Config{
		Store:             NewMemStore(),
		Namespace:         "test",
		CoordinationEpoch: testEpoch,
		Mode:              ModeLeader,
		Evidence:          EvidenceShadowOnly,
	})
	assert.ErrorIs(t, err, ErrShadowOnlyUnderLeader)
}

func TestDistributed_ShadowOnlyIgnoresTheActiveArmsFlattery(t *testing.T) {
	store, clock := newFleetStore(t)
	replicas := fleet(t, 4, store, clock, func(cfg *Config) {
		cfg.Mode = ModeSharedPosterior
		cfg.Evidence = EvidenceShadowOnly
	})

	// LRU is the active arm everywhere and reports far better than it measures
	// as a shadow - the systematic role gap, exaggerated. Shadow-only evidence
	// should see straight through it.
	for range 10 {
		for _, r := range replicas {
			r.bandit.RecordEpoch(ascache.EpochReport{
				Active: ascache.LRU,
				Stats: []ascache.ShadowStats{
					{Policy: ascache.LRU, Hits: 950, Misses: 50},
					{Policy: ascache.TinyLFU, Hits: 700, Misses: 300},
				},
				Capacity:   1000,
				SampleRate: 1,
			})
			r.bandit.sync()
		}
		clock.advance(testEpoch)
	}

	for _, r := range replicas {
		snapshot := r.bandit.Snapshot()
		assert.Equal(t, ascache.TinyLFU.String(), snapshot.Selection,
			"the incumbent's active-role numbers must not decide this")
	}
}

// ---------------------------------------------------------------------------
// Failure and recovery
// ---------------------------------------------------------------------------

func TestDistributed_FallsBackToLocalWhenTheStoreIsUnreachable(t *testing.T) {
	store, clock := newFleetStore(t)
	replicas := fleet(t, 3, store, clock, nil)

	rates := map[ascache.PolicyType]float64{ascache.LRU: 0.2, ascache.TinyLFU: 0.9}
	for range 3 {
		for _, r := range replicas {
			r.report(t, 200, rates)
			r.bandit.sync()
		}
		clock.advance(testEpoch)
	}

	subject := replicas[0]
	require.False(t, subject.bandit.Snapshot().Fallback)

	store.Fail(errors.New("connection refused"))

	// Inside the grace period the replica holds the fleet's last decision
	// rather than reacting to one failed round trip.
	subject.report(t, 200, rates)
	subject.bandit.sync()
	assert.False(t, subject.bandit.Snapshot().Fallback, "one failure is not an outage")

	clock.advance(4 * testEpoch)
	subject.report(t, 200, rates)
	subject.bandit.sync()

	snapshot := subject.bandit.Snapshot()
	assert.True(t, snapshot.Fallback)
	assert.Contains(t, snapshot.LastError, "connection refused")
	assert.Equal(t, ascache.TinyLFU.String(), snapshot.Selection,
		"the local bandit has been learning all along, so it does not start from nothing")
}

func TestDistributed_RecoversWhenTheStoreComesBack(t *testing.T) {
	store, clock := newFleetStore(t)
	replicas := fleet(t, 3, store, clock, nil)
	subject := replicas[0]

	store.Fail(errors.New("down"))
	subject.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})
	subject.bandit.sync()
	require.True(t, subject.bandit.Snapshot().Fallback)

	store.Fail(nil)
	for range 4 {
		for _, r := range replicas {
			r.report(t, 200, map[ascache.PolicyType]float64{ascache.LRU: 0.2, ascache.TinyLFU: 0.9})
			r.bandit.sync()
		}
		clock.advance(testEpoch)
	}

	snapshot := subject.bandit.Snapshot()
	assert.False(t, snapshot.Fallback)
	assert.Positive(t, snapshot.Syncs)
	assert.Equal(t, ascache.TinyLFU.String(), snapshot.Selection)
}

func TestDistributed_OutageCountsAreDiscardedNotReplayed(t *testing.T) {
	store, clock := newFleetStore(t)
	subject := fleet(t, 1, store, clock, nil)[0]

	store.Fail(errors.New("down"))
	for range 5 {
		subject.report(t, 1000, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})
		subject.bandit.sync()
	}
	store.Fail(nil)

	subject.report(t, 10, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})
	result, err := store.Sync(t.Context(), testSyncRequest(subject.bandit.namespace, "probe"))
	require.NoError(t, err)
	subject.bandit.sync()

	window, err := store.Window(t.Context(), subject.bandit.namespace, result.Bucket, result.Bucket)
	require.NoError(t, err)
	require.Len(t, window, 1)

	published := int64(0)
	for _, stats := range window[0].Arms {
		published += stats.Hits + stats.Misses
	}
	// 10 requests across two arms. The 5000 measured during the outage stayed
	// out: evidence that missed its window would land in the wrong bucket and
	// describe traffic that is no longer current.
	assert.Equal(t, int64(20), published)
}

func TestDistributed_NeverSyncedFallsBackWithoutWaiting(t *testing.T) {
	store, clock := newFleetStore(t)
	store.Fail(errors.New("down from the start"))

	subject := fleet(t, 1, store, clock, func(cfg *Config) {
		cfg.FallbackAfter = time.Hour
	})[0]

	subject.report(t, 500, map[ascache.PolicyType]float64{ascache.LRU: 0.2, ascache.TinyLFU: 0.9})
	subject.bandit.sync()

	snapshot := subject.bandit.Snapshot()
	assert.True(t, snapshot.Fallback,
		"a grace period measured from a sync that never happened would never expire")
	assert.Equal(t, ascache.TinyLFU.String(), snapshot.Selection)
}

func TestDistributed_RefusesADecisionForAnArmItDoesNotHave(t *testing.T) {
	store, clock := newFleetStore(t)
	subject := fleet(t, 1, store, clock, nil)[0]

	subject.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})
	subject.bandit.sync()
	before := subject.bandit.SelectPolicy()

	// Something else takes the next bucket's leadership and publishes a
	// decision for a policy this cache was not built with. The namespace
	// fingerprint is supposed to prevent it, so reaching here means a genuine
	// misconfiguration - and applying it would hand the cache a policy it
	// cannot look up.
	clock.advance(testEpoch)
	strangerReq := testSyncRequest(subject.bandit.namespace, "stranger")
	strangerReq.Lead = true
	result, err := store.Sync(t.Context(), strangerReq)
	require.NoError(t, err)
	require.True(t, result.Leader)

	inForce, err := store.Decide(t.Context(), subject.bandit.namespace, result.Bucket, ascache.ARC, time.Minute)
	require.NoError(t, err)
	require.Equal(t, ascache.ARC, inForce)

	subject.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})
	subject.bandit.sync()

	snapshot := subject.bandit.Snapshot()
	assert.Positive(t, snapshot.Rejected)
	assert.Equal(t, before, subject.bandit.SelectPolicy())
	assert.NotEqual(t, ascache.ARC.String(), snapshot.Selection)
}

// ---------------------------------------------------------------------------
// Regime changes
// ---------------------------------------------------------------------------

func TestDistributed_ResizeMovesTheReplicaToItsOwnNamespace(t *testing.T) {
	store, clock := newFleetStore(t)
	subject := fleet(t, 1, store, clock, nil)[0]

	subject.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})
	before := subject.bandit.Snapshot().Namespace

	subject.bandit.RecordEpoch(ascache.EpochReport{
		Active: ascache.LRU,
		Stats: []ascache.ShadowStats{
			{Policy: ascache.LRU, Hits: 1, Misses: 1},
			{Policy: ascache.TinyLFU, Hits: 1, Misses: 1},
		},
		Capacity:   4000, // resized
		SampleRate: 1,
	})

	after := subject.bandit.Snapshot().Namespace
	assert.NotEqual(t, before, after,
		"a hit rate measured at 4000 entries says nothing about one measured at 1000")
	assert.Contains(t, after, "test:")
}

func TestDistributed_ReplicasWithDifferentShapesDoNotPool(t *testing.T) {
	store, clock := newFleetStore(t)
	replicas := fleet(t, 2, store, clock, nil)

	replicas[0].report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})
	replicas[1].bandit.RecordEpoch(ascache.EpochReport{
		Active: ascache.LRU,
		Stats: []ascache.ShadowStats{
			{Policy: ascache.LRU, Hits: 50, Misses: 50},
			{Policy: ascache.TinyLFU, Hits: 60, Misses: 40},
		},
		Capacity:   100, // a much smaller cache
		SampleRate: 1,
	})

	assert.NotEqual(t,
		replicas[0].bandit.Snapshot().Namespace,
		replicas[1].bandit.Snapshot().Namespace)
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func TestDistributed_CloseCancelsAnInFlightRoundTrip(t *testing.T) {
	// Close waits for the coordination goroutine, which may be inside a store
	// call. Deriving that call's context from Background rather than from the
	// bandit's own would make shutting down behind an unreachable store take
	// the whole SyncTimeout - an hour here, and in production however long the
	// timeout was set to.
	store := &blockingStore{entered: make(chan struct{})}

	bandit, err := newDistributed(Config{
		Store:             store,
		Namespace:         "test",
		CoordinationEpoch: testEpoch,
		SyncTimeout:       time.Hour,
	})
	require.NoError(t, err)

	r := &replica{bandit: bandit, active: ascache.LRU}
	r.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.9})

	bandit.wg.Add(1)
	go func() {
		defer bandit.wg.Done()
		bandit.sync()
	}()
	<-store.entered

	closed := make(chan error, 1)
	go func() { closed <- bandit.Close() }()

	select {
	case err := <-closed:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited on an in-flight store call instead of cancelling it")
	}
}

func TestDistributed_CloseIsIdempotentAndStopsTheGoroutine(t *testing.T) {
	store, _ := newFleetStore(t)

	bandit, err := NewDistributed(Config{
		Store:             store,
		Namespace:         "test",
		CoordinationEpoch: time.Millisecond,
	})
	require.NoError(t, err)

	require.NoError(t, bandit.Close())
	require.NoError(t, bandit.Close())
	require.NoError(t, bandit.Close())
}

func TestDistributed_SyncBeforeAnyReportPublishesNothing(t *testing.T) {
	store, clock := newFleetStore(t)
	subject := fleet(t, 1, store, clock, nil)[0]

	// The measurement regime is unknown until the cache reports, so there is
	// nothing to key the counts by.
	subject.bandit.sync()

	snapshot := subject.bandit.Snapshot()
	assert.Zero(t, snapshot.Syncs)
	assert.Zero(t, snapshot.SyncFailures)
	assert.Equal(t, ascache.Undefined.String(), snapshot.Selection)
}

func TestDistributed_SelectPolicyIsUndefinedUntilTheFirstRound(t *testing.T) {
	store, clock := newFleetStore(t)
	subject := fleet(t, 1, store, clock, nil)[0]

	subject.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})

	// The cache reads Undefined as "no change", which is what stops it acting
	// on a bandit that has not heard from the fleet yet.
	assert.Equal(t, ascache.Undefined, subject.bandit.SelectPolicy())
}

func TestDistributed_RecordStatsStandsInForAPlainBandit(t *testing.T) {
	store, clock := newFleetStore(t)
	subject := fleet(t, 1, store, clock, nil)[0]

	// A Distributed used through the plain Bandit interface still works; it
	// just cannot tell which arm was active.
	for range 3 {
		subject.bandit.RecordStats(ascache.ShadowStats{Policy: ascache.LRU, Hits: 20, Misses: 80})
		subject.bandit.RecordStats(ascache.ShadowStats{Policy: ascache.TinyLFU, Hits: 90, Misses: 10})
	}

	assert.Equal(t, ascache.TinyLFU, subject.bandit.local.SelectPolicy())
}

func TestDistributed_ConcurrentReportsAndSelections(t *testing.T) {
	store, clock := newFleetStore(t)
	subject := fleet(t, 1, store, clock, nil)[0]

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				subject.report(t, 10, map[ascache.PolicyType]float64{ascache.LRU: 0.5, ascache.TinyLFU: 0.6})
				subject.bandit.SelectPolicy()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			subject.bandit.sync()
			subject.bandit.Snapshot()
		}
	}()

	wg.Wait()
}
