package ascache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowBandit blocks inside SelectPolicy until it is released, and announces
// that it has been entered. It stands in for any bandit whose selection is not
// instantaneous: one that reads a shared store, takes a contended lock, or is
// simply descheduled at the wrong moment.
type slowBandit struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int64
	pick    PolicyType
}

func newSlowBandit(pick PolicyType) *slowBandit {
	return &slowBandit{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		pick:    pick,
	}
}

func (b *slowBandit) RecordStats(ShadowStats) {}

func (b *slowBandit) SelectPolicy() PolicyType {
	b.calls.Add(1)
	b.once.Do(func() { close(b.entered) })
	<-b.release

	return b.pick
}

// TestEpoch_SlowBanditDoesNotBlockReaders is the property this whole
// arrangement exists to provide: a bandit that takes its time delays the
// switch it is deciding, and nothing else.
//
// Go's RWMutex queues new readers behind a waiting writer, so a bandit called
// while the cache's write lock is held stalls every Get in the process for its
// full duration -- a store timeout becomes a cache outage. The epoch therefore
// has to snapshot under the lock, release it, consult the bandit, and take the
// lock again to apply the result.
func TestEpoch_SlowBanditDoesNotBlockReaders(t *testing.T) {
	t.Parallel()

	lru := newEvictingPolicy[string, int](LRU, 4)
	lfu := newEvictingPolicy[string, int](LFU, 4)
	bandit := newSlowBandit(LFU)

	// The ticker drives epochs, so the goroutine that parks in the bandit is
	// not one of the readers. (With EpochRequests the caller completing an
	// epoch runs it, and that one caller does wait for the bandit -- by
	// design, and documented on the setting. Every other caller must not.)
	cache, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{lru, lfu},
		bandit,
		&Settings{EpochDuration: time.Millisecond, EvictPartialCapacityFilling: true},
	)
	require.NoError(t, err)
	defer cache.Close()
	defer close(bandit.release)

	select {
	case <-bandit.entered:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the bandit was never consulted")
	}

	// The epoch is now mid-flight with the bandit blocked. A reader must not
	// be waiting on it.
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		cache.Get("reader")
		done <- time.Since(start)
	}()

	select {
	case took := <-done:
		assert.Less(t, took, 2*time.Second,
			"Get waited on the bandit; it must only wait on the cache's own state")
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Get blocked behind the bandit: a slow bandit is a cache outage")
	}
}

// conservingBandit records every arm report it is given, so a test can assert
// that an epoch's evidence is delivered exactly once.
type conservingBandit struct {
	mu      sync.Mutex
	byArm   map[PolicyType]int64
	reports int64
	pick    PolicyType
}

func newConservingBandit(pick PolicyType) *conservingBandit {
	return &conservingBandit{byArm: map[PolicyType]int64{}, pick: pick}
}

func (b *conservingBandit) RecordStats(s ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.byArm[s.Policy] += s.Hits + s.Misses
	b.reports++
}

func (b *conservingBandit) SelectPolicy() PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.pick
}

func (b *conservingBandit) totals() (perArm map[PolicyType]int64, reports int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make(map[PolicyType]int64, len(b.byArm))
	for k, v := range b.byArm {
		out[k] = v
	}

	return out, b.reports
}

// TestEpoch_TickerAndRequestEpochsDoNotLoseCounts drives both epoch clocks at
// once from many goroutines. Each arm's counters are read and reset under the
// write lock, so whatever an epoch collects is delivered to the bandit exactly
// once and nothing is dropped between the two.
//
// The invariant checked is conservation: every request the cache counted for
// an arm reaches the bandit, or is still sitting in that arm's live counters.
// A snapshot delivered twice inflates the total; one collected and dropped on
// the way to the bandit deflates it.
func TestEpoch_TickerAndRequestEpochsDoNotLoseCounts(t *testing.T) {
	t.Parallel()

	lru := newEvictingPolicy[string, int](LRU, 64)
	lfu := newEvictingPolicy[string, int](LFU, 64)
	bandit := newConservingBandit(LRU)

	cache, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{lru, lfu},
		bandit,
		&Settings{
			EpochDuration:               time.Millisecond,
			EpochRequests:               64,
			EvictPartialCapacityFilling: true,
			ShadowSampleRate:            1,
		},
	)
	require.NoError(t, err)

	const (
		goroutines = 8
		perRoutine = 1250
	)

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perRoutine {
				key := string(rune('a' + (g+i)%26))
				cache.Add(key, i)
				cache.Get(key)
			}
		}()
	}
	wg.Wait()
	cache.Close()

	delivered, reports := bandit.totals()
	require.Positive(t, reports, "no epoch ever reported")

	// Whatever has not been delivered must still be live in the arm.
	for _, policy := range []*evictingPolicy[string, int]{lru, lfu} {
		live := policy.GetStats()
		got := delivered[policy.GetType()] + live.Hits + live.Misses
		assert.Equal(t, int64(goroutines*perRoutine), got,
			"%s: %d delivered plus %d live should account for every request",
			policy.GetType(), delivered[policy.GetType()], live.Hits+live.Misses)
	}
}

// steppedBandit lets a test hold one epoch inside SelectPolicy while it drives
// another epoch to completion, which is the only way to produce a decision made
// from evidence that has since been superseded.
type steppedBandit struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	picks   atomic.Int64
	first   PolicyType
	rest    PolicyType
}

func newSteppedBandit(first, rest PolicyType) *steppedBandit {
	return &steppedBandit{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		first:   first,
		rest:    rest,
	}
}

func (b *steppedBandit) RecordStats(ShadowStats) {}

func (b *steppedBandit) SelectPolicy() PolicyType {
	if b.picks.Add(1) == 1 {
		b.once.Do(func() { close(b.entered) })
		<-b.release

		return b.first
	}

	return b.rest
}

// TestEpoch_StaleSelectionIsDiscarded pins the re-check that releasing the
// lock makes necessary. A decision is computed from one epoch's evidence; if
// another epoch has collected and reported since, that decision describes a
// cache state that no longer exists -- and the gates it was checked against
// were fed statistics that have since been overwritten. It is dropped, and the
// newer epoch decides instead.
func TestEpoch_StaleSelectionIsDiscarded(t *testing.T) {
	t.Parallel()

	lru := newEvictingPolicy[string, int](LRU, 4)
	lfu := newEvictingPolicy[string, int](LFU, 4)
	// The first selection asks for a switch; every later one asks for no
	// change. If the stale decision is applied the cache ends on LFU.
	bandit := newSteppedBandit(LFU, LRU)

	cache, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{lru, lfu},
		bandit,
		&Settings{EpochRequests: 1, EvictPartialCapacityFilling: true},
	)
	require.NoError(t, err)
	defer cache.Close()

	require.Equal(t, LRU, cache.ActivePolicy())

	// Epoch one collects, then parks inside the bandit holding no cache lock.
	go func() { cache.Get("first") }()
	select {
	case <-bandit.entered:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the bandit was never consulted")
	}

	// Epoch two collects while epoch one is still parked -- it takes only the
	// cache lock to do so, which epoch one is not holding. From here epoch
	// one's decision describes a state two epochs old.
	go func() { cache.Get("second") }()
	require.Eventually(t, func() bool {
		cache.mu.RLock()
		defer cache.mu.RUnlock()

		return cache.epochsCollected >= 2
	}, 5*time.Second, time.Millisecond, "the second epoch never collected")

	// Now let epoch one's decision arrive. It must be dropped.
	close(bandit.release)

	require.Eventually(t, func() bool { return bandit.picks.Load() >= 2 },
		5*time.Second, time.Millisecond, "the second epoch never reached the bandit")

	assert.Equal(t, LRU, cache.ActivePolicy(),
		"a selection superseded by a later epoch must not be applied")
}
