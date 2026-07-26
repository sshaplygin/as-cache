package ascache

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeObserver builds an observe-only cache over two mock policies. It passes
// no bandit at all, which is the point: observing should require no strategy.
func makeObserver(t *testing.T) (
	*AdaptiveCache[string, int],
	*mockPolicy[string, int],
	*mockPolicy[string, int],
) {
	t.Helper()

	lru := newMockPolicy[string, int](LRU, 100)
	lfu := newMockPolicy[string, int](LFU, 100)

	ac, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{lru, lfu},
		nil,
		&Settings{EpochDuration: 24 * time.Hour, ObserveOnly: true},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	return ac, lru, lfu
}

func TestObserveOnly_NeedsNoBandit(t *testing.T) {
	ac, _, _ := makeObserver(t)

	assert.Equal(t, LRU, ac.ActivePolicy())
}

func TestObserveOnly_StillRequiresBanditWhenSwitching(t *testing.T) {
	_, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{newMockPolicy[string, int](LRU, 10)},
		nil,
		&Settings{EpochDuration: time.Hour},
	)

	assert.ErrorIs(t, err, ErrNilBandit,
		"a cache that switches still needs a strategy to switch by")
}

// TestObserveOnly_NeverSwitches is the guarantee the mode exists to make: the
// cache behaves exactly like the policy it was built with, whatever the
// measurements say.
func TestObserveOnly_NeverSwitches(t *testing.T) {
	lru := newMockPolicy[string, int](LRU, 100)
	lfu := newMockPolicy[string, int](LFU, 100)

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lru, lfu},
		// A bandit that always demands the other policy. It must be ignored.
		&mockBandit{next: LFU},
		&Settings{EpochDuration: 24 * time.Hour, ObserveOnly: true},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	ac.Add("a", 1)
	primeActiveStats(ac, 1, 99)
	primeStats(lfu, 99, 1)

	for i := 0; i < 5; i++ {
		ac.runEpoch()
	}

	assert.Equal(t, LRU, ac.ActivePolicy(),
		"ObserveOnly must never change the active policy, however good another arm looks")
}

// TestObserveOnly_MeasuresWithoutAFullCache guards the capacity gate: it
// exists to avoid switching on thin evidence, but in observe mode nothing
// switches, so gating would only suppress the measurement being asked for.
func TestObserveOnly_MeasuresWithoutAFullCache(t *testing.T) {
	ac, _, lfu := makeObserver(t)

	// EvictPartialCapacityFilling is false and the cache holds 1 of 100
	// entries, which would normally gate the epoch entirely.
	ac.Add("a", 1)
	primeActiveStats(ac, 10, 90)
	primeStats(lfu, 90, 10)

	ac.runEpoch()

	advice := ac.Advice()
	require.Len(t, advice.Reports, 2, "both policies must be measured")
	assert.Equal(t, LFU, advice.Best)
}

func TestAdvice_IdentifiesTheBetterPolicy(t *testing.T) {
	ac, _, lfu := makeObserver(t)

	primeActiveStats(ac, 30, 70) // active LRU: 30%
	primeStats(lfu, 80, 20)      // shadow LFU: 80%
	ac.runEpoch()

	advice := ac.Advice()

	assert.Equal(t, LFU, advice.Best)
	assert.Equal(t, LRU, advice.Active)
	assert.InDelta(t, 0.50, advice.Improvement, 1e-9,
		"LFU beats LRU by 50 points and the advice should say so")
	assert.Equal(t, int64(1), advice.Epochs)

	summary := advice.String()
	assert.Contains(t, summary, "LFU beats LRU")
	assert.Contains(t, summary, "50.00 points")
}

func TestAdvice_AccumulatesAcrossEpochs(t *testing.T) {
	ac, _, lfu := makeObserver(t)

	for i := 0; i < 4; i++ {
		primeActiveStats(ac, 10, 10)
		primeStats(lfu, 15, 5)
		ac.runEpoch()
	}

	advice := ac.Advice()
	require.Equal(t, int64(4), advice.Epochs)

	byPolicy := map[PolicyType]PolicyReport{}
	for _, r := range advice.Reports {
		byPolicy[r.Policy] = r
	}

	assert.Equal(t, int64(40), byPolicy[LRU].Hits, "advice must span every epoch, not just the last")
	assert.Equal(t, int64(60), byPolicy[LFU].Hits)
	assert.InDelta(t, 0.75, byPolicy[LFU].HitRate(), 1e-9)
}

func TestAdvice_SaysNothingBeforeMeasuring(t *testing.T) {
	ac, _, _ := makeObserver(t)

	advice := ac.Advice()

	assert.Zero(t, advice.Epochs)
	assert.Empty(t, advice.Reports)
	assert.Equal(t, "no measurements yet", advice.String())
}

func TestAdvice_ReportsWhenTheActivePolicyIsAlreadyBest(t *testing.T) {
	ac, _, lfu := makeObserver(t)

	primeActiveStats(ac, 90, 10)
	primeStats(lfu, 10, 90)
	ac.runEpoch()

	advice := ac.Advice()

	assert.Equal(t, LRU, advice.Best)
	assert.Zero(t, advice.Improvement)
	assert.Contains(t, advice.String(), "LRU is the best")
}

func TestAdvice_FlagsSampledEstimates(t *testing.T) {
	lru := newMockPolicy[string, int](LRU, 100000)
	lfu := newMockPolicy[string, int](LFU, 100000)

	ac, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{lru, lfu}, nil,
		&Settings{EpochDuration: 24 * time.Hour, ObserveOnly: true, ShadowSampleRate: 0.05},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	for i := 0; i < 2000; i++ {
		ac.Add("key-"+strconv.Itoa(i), i)
	}
	ac.runEpoch()

	advice := ac.Advice()

	assert.True(t, advice.Sampled, "sampled measurements must be flagged as estimates")
	assert.InDelta(t, 0.05, advice.SampleRate, 1e-9)
	assert.Contains(t, advice.String(), "estimated from 5.0%")
}

// TestAdvice_SafeUnderConcurrentUse checks that reading advice while the cache
// is serving traffic neither races nor blocks writers out.
func TestAdvice_SafeUnderConcurrentUse(t *testing.T) {
	ac, _, _ := makeObserver(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; ; j++ {
				select {
				case <-stop:
					return
				default:
					key := "k" + strconv.Itoa((seed+j)%50)
					ac.Add(key, j)
					ac.Get(key)
				}
			}
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			advice := ac.Advice()
			_ = advice.String()
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestAdvice_StringIsReadable checks the summary a user actually reads.
func TestAdvice_StringIsReadable(t *testing.T) {
	ac, _, lfu := makeObserver(t)

	primeActiveStats(ac, 25, 75)
	primeStats(lfu, 60, 40)
	ac.runEpoch()

	summary := ac.Advice().String()
	t.Logf("\n%s", summary)

	for _, want := range []string{"LFU", "LRU", "hit rate", "currently active"} {
		assert.True(t, strings.Contains(summary, want),
			"summary should mention %q:\n%s", want, summary)
	}
}

// TestAdvice_DoesNotRecommendRevertingACorrectSwitch guards the defect that
// made advice actively harmful in adaptive mode. Accumulating a policy's
// measurements across a role change pooled its active tenure (full capacity,
// all traffic) with its shadow tenure (miniature capacity, a sample), and left
// the outgoing policy's long good history outweighing the incoming policy's
// short one - so right after a correct switch, Advice named the policy the
// cache had just moved away from as best, for as many epochs as the history
// was long.
func TestAdvice_DoesNotRecommendRevertingACorrectSwitch(t *testing.T) {
	lru := newMockPolicy[string, int](LRU, 100)
	lfu := newMockPolicy[string, int](LFU, 100)
	bandit := &mockBandit{next: LRU}

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lru, lfu}, bandit,
		&Settings{EpochDuration: 24 * time.Hour, EvictPartialCapacityFilling: true},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	// A long stretch where LRU is active and excellent.
	for i := 0; i < 30; i++ {
		primeActiveStats(ac, 90, 10)
		primeStats(lfu, 10, 90)
		ac.runEpoch()
	}
	require.Equal(t, LRU, ac.ActivePolicy())

	// The workload turns, the bandit switches to LFU, and LFU is now the
	// better policy while LRU shadows badly.
	bandit.next = LFU
	primeActiveStats(ac, 10, 90)
	primeStats(lfu, 95, 5)
	ac.runEpoch()
	require.Equal(t, LFU, ac.ActivePolicy(), "expected the switch to land")

	primeActiveStats(ac, 95, 5)
	primeStats(lru, 10, 90)
	ac.runEpoch()

	advice := ac.Advice()

	assert.Equal(t, LFU, advice.Best,
		"advice must reflect the policies in their current roles, not recommend reverting a correct switch")
	assert.Zero(t, advice.Improvement,
		"the active policy is the best one, so there is nothing being left on the table")
}

// TestAdvice_EpochsCountsOnlyMeasuredEpochs guards a counter that reported
// elapsed ticks as evidence. The capacity gate can skip measurement for
// thousands of ticks, and reporting those as epochs behind a recommendation
// overstated the evidence by exactly that much.
func TestAdvice_EpochsCountsOnlyMeasuredEpochs(t *testing.T) {
	lru := newMockPolicy[string, int](LRU, 100)
	lfu := newMockPolicy[string, int](LFU, 100)

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{lru, lfu}, &mockBandit{next: LRU},
		// The gate is on and the cache holds far less than its capacity, so
		// no epoch measures anything.
		&Settings{EpochDuration: 24 * time.Hour, EvictPartialCapacityFilling: false},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	ac.Add("a", 1)
	for i := 0; i < 50; i++ {
		ac.runEpoch()
	}

	advice := ac.Advice()

	assert.Zero(t, advice.Epochs,
		"50 gated ticks measured nothing, so they are not evidence")
	assert.Empty(t, advice.Reports)
	assert.Equal(t, "no measurements yet", advice.String())
}

// TestAdvice_BestIsDeterministicOnTies guards a Best that flapped between arms
// on an unchanged cache: Reports was built by ranging a map and sorted stably
// on hit rate alone, so tied policies kept their random order.
func TestAdvice_BestIsDeterministicOnTies(t *testing.T) {
	ac, _, lfu := makeObserver(t)

	// Both arms measure exactly the same, so every ordering is a valid sort.
	primeActiveStats(ac, 50, 50)
	primeStats(lfu, 50, 50)
	ac.runEpoch()

	first := ac.Advice().Best
	for i := 0; i < 200; i++ {
		assert.Equal(t, first, ac.Advice().Best,
			"Best must not change while the cache does not")
	}
}
