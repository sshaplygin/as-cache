package ascache

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parkedSwitchBandit parks inside its first SelectPolicy until released and
// then asks for a switch to next; every later selection asks for no change.
// It keeps every report it is given.
type parkedSwitchBandit struct {
	entered chan struct{}
	release chan struct{}
	next    PolicyType

	mu      sync.Mutex
	calls   int
	reports []ShadowStats
}

func (b *parkedSwitchBandit) RecordStats(s ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.reports = append(b.reports, s)
}

func (b *parkedSwitchBandit) SelectPolicy() PolicyType {
	b.mu.Lock()
	b.calls++
	first := b.calls == 1
	b.mu.Unlock()

	if !first {
		return Undefined
	}
	close(b.entered)
	<-b.release

	return b.next
}

func (b *parkedSwitchBandit) takeReports() []ShadowStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := b.reports
	b.reports = nil

	return out
}

// TestEpoch_OutgoingSamplesAreNotCreditedToIncomingPolicy pins who a sample
// belongs to across a switch.
//
// The active arm's evidence is counted on the cache, not on the policy, and
// read-and-reset when an epoch collects. The epoch then releases the lock while
// the bandit decides, and Gets keep arriving, served by the policy that is
// still active and counted into those same cache-level counters. If the bandit
// then switches, the next epoch reports whatever accumulated as the evidence
// of the policy that has just become active -- requests it never served.
// The window is as long as the bandit takes to decide.
func TestEpoch_OutgoingSamplesAreNotCreditedToIncomingPolicy(t *testing.T) {
	lru := newMockPolicy[string, int](LRU, 10)
	lfu := newMockPolicy[string, int](LFU, 10)
	bandit := &parkedSwitchBandit{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		next:    LFU,
	}

	ac, err := NewAdaptiveCache([]Policy[string, int]{lru, lfu}, bandit, &Settings{
		EpochDuration:               24 * time.Hour,
		EvictPartialCapacityFilling: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	ac.Add("a", 1)

	epochOne := make(chan struct{})
	go func() {
		ac.runEpoch()
		close(epochOne)
	}()

	select {
	case <-bandit.entered:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "epoch one never reached the bandit")
	}

	// Epoch one has collected and released the lock. LRU is still active and
	// serves these; each is recorded as a sample of the active arm.
	const servedByLRU = 5
	for range servedByLRU {
		_, ok := ac.Get("a")
		require.True(t, ok)
	}

	close(bandit.release)
	select {
	case <-epochOne:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "epoch one never finished")
	}
	require.Equal(t, LFU, ac.ActivePolicy(), "epoch one's selection switches to LFU")

	bandit.takeReports() // epoch one's, delivered before the bandit parked

	// Epoch two. Not one request has reached LFU since it became active.
	ac.runEpoch()

	reports := bandit.takeReports()
	var incoming *ShadowStats
	for i := range reports {
		if reports[i].Policy == LFU {
			incoming = &reports[i]
		}
	}
	require.NotNil(t, incoming, "epoch two must report LFU")

	assert.Zero(t, incoming.Hits+incoming.Misses,
		"LFU served nothing since becoming active, yet epoch two reported %d hits and %d misses "+
			"for it: requests LRU served while the bandit was deciding were credited to LFU",
		incoming.Hits, incoming.Misses)
}
