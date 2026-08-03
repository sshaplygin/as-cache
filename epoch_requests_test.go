package ascache

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingBandit counts how many epochs it was asked to decide, which is the
// only externally visible consequence of an epoch ending.
type countingBandit struct {
	mu       sync.Mutex
	next     PolicyType
	selected int
}

func (b *countingBandit) RecordStats(_ ShadowStats) {}

func (b *countingBandit) SelectPolicy() PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selected++

	return b.next
}

func (b *countingBandit) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.selected
}

// newRequestDrivenCache builds a cache with no wall clock at all: every epoch
// it runs is one this test asked for by calling Get.
func newRequestDrivenCache(t *testing.T, every int64, bandit Bandit) *AdaptiveCache[string, int] {
	t.Helper()

	cache, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{
			newEvictingPolicy[string, int](LRU, 32),
			newEvictingPolicy[string, int](LFU, 32),
		},
		bandit,
		&Settings{EpochRequests: every, EvictPartialCapacityFilling: true},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })

	return cache
}

func TestEpochRequests_EndsAnEpochEveryNGets(t *testing.T) {
	t.Parallel()

	const every = 10
	bandit := &countingBandit{next: LRU}
	cache := newRequestDrivenCache(t, every, bandit)

	for range every - 1 {
		cache.Get("k")
	}
	assert.Equal(t, 0, bandit.count(), "no epoch may run before the limit is reached")

	cache.Get("k")
	assert.Equal(t, 1, bandit.count(), "the Nth Get must end the epoch")

	for range every * 4 {
		cache.Get("k")
	}
	assert.Equal(t, 5, bandit.count(), "one epoch per N gets, no more and no fewer")
}

// TestEpochRequests_OnlyGetCounts pins the documented unit. Hits and misses are
// recorded in Get and nowhere else, so an epoch ended by any other operation
// would be reporting evidence nothing had added to.
func TestEpochRequests_OnlyGetCounts(t *testing.T) {
	t.Parallel()

	bandit := &countingBandit{next: LRU}
	cache := newRequestDrivenCache(t, 4, bandit)

	for i := range 20 {
		cache.Add("k", i)
		cache.Contains("k")
		cache.Peek("k")
		cache.Len()
		cache.Keys()
	}
	assert.Equal(t, 0, bandit.count(), "only Get advances the request clock")

	for range 4 {
		cache.Get("k")
	}
	assert.Equal(t, 1, bandit.count())
}

// TestEpochRequests_ConcurrentGetsLoseNoRequests is the property that makes the
// count trustworthy under load: whatever the interleaving, the number of epochs
// is exactly the number of Gets divided by the limit. A reset-to-zero instead
// of a subtract would discard the requests that arrived mid-crossing and run
// too few epochs; a >= comparison would let several goroutines trigger the same
// crossing and run too many.
func TestEpochRequests_ConcurrentGetsLoseNoRequests(t *testing.T) {
	t.Parallel()

	const (
		every        = 8
		goroutines   = 16
		perGoroutine = 250
	)

	bandit := &countingBandit{next: LRU}
	cache := newRequestDrivenCache(t, every, bandit)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				cache.Get("k")
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, goroutines*perGoroutine/every, bandit.count(),
		"every request must be counted exactly once")
}

// TestEpochRequests_SameTraceSameResult is the reason this setting exists. A
// wall-clock cache replaying one trace twice re-evaluates a different number of
// times on a busy machine and lands on a different hit rate; a request-counted
// one cannot.
func TestEpochRequests_SameTraceSameResult(t *testing.T) {
	t.Parallel()

	trace := make([]string, 0, 2000)
	for i := range 2000 {
		// A small, skewed keyspace so the policies actually disagree and
		// switching has something to change.
		trace = append(trace, "k"+string(rune('a'+i%37)))
	}

	replay := func() (GlobalStats, PolicyType, int) {
		bandit := &countingBandit{next: LFU}
		cache := newRequestDrivenCache(t, 64, bandit)

		for i, key := range trace {
			if _, ok := cache.Get(key); !ok {
				cache.Add(key, i)
			}
		}

		return cache.Stats(), cache.ActivePolicy(), bandit.count()
	}

	firstStats, firstPolicy, firstEpochs := replay()
	secondStats, secondPolicy, secondEpochs := replay()

	assert.Equal(t, firstStats, secondStats, "hit and miss counts must reproduce exactly")
	assert.Equal(t, firstPolicy, secondPolicy, "the same trace must end on the same policy")
	assert.Equal(t, firstEpochs, secondEpochs, "the same trace must run the same number of epochs")
	assert.Positive(t, firstEpochs, "the replay must actually have ended some epochs")
}

func TestEpochRequests_RunsAlongsideTheWallClock(t *testing.T) {
	t.Parallel()

	bandit := &countingBandit{next: LRU}
	cache, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{newEvictingPolicy[string, int](LRU, 8)},
		bandit,
		&Settings{
			EpochDuration:               time.Millisecond,
			EpochRequests:               5,
			EvictPartialCapacityFilling: true,
		},
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, cache.Close()) }()

	for range 5 {
		cache.Get("k")
	}
	// The request clock alone accounts for one; the ticker adds its own on its
	// own schedule, so this asserts only that both are live.
	assert.GreaterOrEqual(t, bandit.count(), 1)

	assert.Eventually(t, func() bool { return bandit.count() > 1 }, time.Second, 5*time.Millisecond,
		"the wall clock must keep running when EpochRequests is also set")
}

func TestNewAdaptiveCache_EpochClockValidation(t *testing.T) {
	t.Parallel()

	policies := func() []Policy[string, int] {
		return []Policy[string, int]{newEvictingPolicy[string, int](LRU, 8)}
	}

	t.Run("negative request count is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := NewAdaptiveCache(policies(), &mockBandit{next: LRU}, &Settings{EpochRequests: -1})
		require.ErrorIs(t, err, ErrInvalidEpochRequests)
	})

	t.Run("neither clock set is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := NewAdaptiveCache(policies(), &mockBandit{next: LRU}, &Settings{})
		require.ErrorIs(t, err, ErrInvalidEpochDuration)
	})

	t.Run("requests alone is enough", func(t *testing.T) {
		t.Parallel()

		// No EpochDuration: this must not panic in time.NewTicker, and Close
		// must still return once the goroutine that has no ticker exits.
		cache, err := NewAdaptiveCache(policies(), &mockBandit{next: LRU}, &Settings{EpochRequests: 1})
		require.NoError(t, err)
		require.Nil(t, cache.epochTicker, "a request-driven cache needs no ticker")
		require.NoError(t, cache.Close())
	})
}
