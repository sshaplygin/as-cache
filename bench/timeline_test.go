package bench_test

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bench"
)

// timelineCache wraps an AdaptiveCache and records which policy was active as
// the workload is replayed, so switching behaviour can be plotted against the
// phase it was reacting to.
type timelineCache struct {
	inner *ascache.AdaptiveCache[string, int]

	mu       sync.Mutex
	samples  []ascache.PolicyType
	interval int
	seen     int
}

func (c *timelineCache) sample() {
	c.mu.Lock()
	c.seen++
	due := c.seen%c.interval == 0
	c.mu.Unlock()

	if !due {
		return
	}

	active := c.inner.ActivePolicy()

	c.mu.Lock()
	c.samples = append(c.samples, active)
	c.mu.Unlock()
}

func (c *timelineCache) Get(key string) (int, bool) {
	c.sample()

	return c.inner.Get(key)
}

func (c *timelineCache) Add(key string, value int) bool {
	return c.inner.Add(key, value)
}

// TestActivePolicyTimeline replays a phase-shifting workload and plots which
// policy was active through it.
//
// This is the artifact that shows whether switching does anything real: on a
// workload that alternates between regimes, a cache that adapts should be seen
// changing arms, and changing them at the phase boundaries rather than at
// random.
func TestActivePolicyTimeline(t *testing.T) {
	if testing.Short() {
		// These replay millions of requests through timing-driven epochs.
		// They are evidence, not correctness checks, and under -race the
		// epoch pacing changes enough to make them meaningless.
		t.Skip("evidence run; use make evidence")
	}

	const (
		size     = 500
		phases   = 12
		perPhase = 20000
	)

	w := bench.PhaseShift(phases, perPhase, 20000, size+50, 5)

	arms, err := bench.AdaptiveArms(size)
	require.NoError(t, err)

	inner, err := ascache.NewAdaptiveCache(arms,
		bench.NewThompsonBandit(0.6, 9),
		&ascache.Settings{
			EpochDuration:               2 * time.Millisecond,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           ascache.MigrationWarm,
			ShadowSampleRate:            0.05,
			MinShadowCapacity:           64,
		})
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	// One sample per 1/40th of a phase, so a phase is legible in the plot.
	cache := &timelineCache{inner: inner, interval: perPhase / 40}

	result := bench.Replay("adaptive", cache, w)

	cache.mu.Lock()
	samples := append([]ascache.PolicyType(nil), cache.samples...)
	cache.mu.Unlock()

	require.NotEmpty(t, samples, "expected timeline samples")

	t.Logf("\nphase-shift timeline (%d requests, cache %d, %d phases)\n%s\nhit rate %.2f%%",
		w.Len(), size, phases, plotTimeline(samples, phases), result.HitRate()*100)

	distinct := map[ascache.PolicyType]int{}
	for _, p := range samples {
		distinct[p]++
	}

	assert.Greater(t, len(distinct), 1,
		"the cache should change arms on a workload that changes regime, saw only %v", distinct)
}

// plotTimeline renders the active policy over time as one row per policy, with
// phase boundaries marked, so the reader can see whether switches line up with
// the workload changing regime.
func plotTimeline(samples []ascache.PolicyType, phases int) string {
	present := map[ascache.PolicyType]bool{}
	for _, p := range samples {
		present[p] = true
	}

	names := make([]ascache.PolicyType, 0, len(present))
	for p := range present {
		names = append(names, p)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	width := len(samples)
	perPhase := width / phases

	var b strings.Builder

	// Phase ruler: alternating regime labels above the plot.
	b.WriteString(fmt.Sprintf("%-10s ", "phase"))
	for i := 0; i < width; i++ {
		if perPhase > 0 && i%perPhase == 0 {
			if (i/perPhase)%2 == 0 {
				b.WriteString("Z")

				continue
			}
			b.WriteString("L")

			continue
		}
		b.WriteString("-")
	}
	b.WriteString("   (Z = zipf phase, L = loop phase)\n")

	for _, policy := range names {
		fmt.Fprintf(&b, "%-10s ", policy.String())
		for _, s := range samples {
			if s == policy {
				b.WriteString("#")

				continue
			}
			b.WriteString(" ")
		}
		b.WriteString("\n")
	}

	// Share of time each policy held the active slot.
	counts := map[ascache.PolicyType]int{}
	for _, s := range samples {
		counts[s]++
	}
	b.WriteString("\nshare of time active: ")
	parts := make([]string, 0, len(names))
	for _, policy := range names {
		parts = append(parts, fmt.Sprintf("%s %.0f%%",
			policy, float64(counts[policy])/float64(len(samples))*100))
	}
	b.WriteString(strings.Join(parts, ", "))

	return b.String()
}
