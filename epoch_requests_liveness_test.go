package ascache

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tickCountingBandit counts selections, one per reporting epoch, so a test
// can tell whether the epoch clock is still advancing.
type tickCountingBandit struct {
	selections atomic.Int64
}

func (b *tickCountingBandit) RecordStats(ShadowStats) {}

func (b *tickCountingBandit) SelectPolicy() PolicyType {
	b.selections.Add(1)

	return LRU
}

// TestEpochRequests_ClockSurvivesContention checks that the request-driven
// epoch clock is still running after a contended burst.
//
// countRequest triggers an epoch when its counter equals the limit, then
// subtracts the limit. If enough other callers increment between one caller's
// increment and its subtraction, the counter passes the limit without anyone
// observing it equal, and nothing ever subtracts again: every later Get sees
// a count above the limit and returns. The clock does not skip an epoch; it
// stops. With EpochRequests at 1 the window needs only one concurrent Get.
func TestEpochRequests_ClockSurvivesContention(t *testing.T) {
	t.Parallel()

	bandit := &tickCountingBandit{}
	cache, err := NewAdaptiveCache[string, int](
		[]Policy[string, int]{
			newEvictingPolicy[string, int](LRU, 4),
			newEvictingPolicy[string, int](LFU, 4),
		},
		bandit,
		&Settings{EpochRequests: 1, EvictPartialCapacityFilling: true},
	)
	require.NoError(t, err)
	defer cache.Close()

	const (
		goroutines = 16
		perRoutine = 2000
	)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perRoutine {
				cache.Get("k")
			}
		}()
	}
	wg.Wait()

	// The burst is over and nothing else is running. With EpochRequests at 1,
	// each of these Gets must end exactly one epoch.
	const serial = 10
	before := bandit.selections.Load()
	for range serial {
		cache.Get("k")
	}

	assert.Equal(t, before+serial, bandit.selections.Load(),
		"the request-driven epoch clock stopped after contention: %d selections before, "+
			"%d after %d uncontended Gets", before, bandit.selections.Load(), serial)
}
