package bench_test

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
	"github.com/sshaplygin/as-cache/bench"
)

// timelineCache wraps an AdaptiveCache and records which policy was active as
// the workload is replayed, so switching behaviour can be plotted against the
// phase it was reacting to.
type timelineCache struct {
	inner *ascache.AdaptiveCache[string, int]

	mu       sync.Mutex
	samples  []ascache.PolicyType
	frames   []timelineFrame
	interval int
	seen     int
}

// timelineFrame is one sample's worth of what the cache knew: which arm was
// serving, and what every arm had measured at that moment.
//
// The plot alone shows that the active policy changes; it cannot show why. The
// evidence behind each switch is the interesting part, and it is only
// observable while the run is happening.
type timelineFrame struct {
	// Request is how many requests had been served when the frame was taken.
	Request int `json:"request"`
	// Active is the arm serving at that moment.
	Active string `json:"active"`
	// Epochs is how many reporting epochs had fed the advice so far.
	Epochs int64 `json:"epochs"`
	// Arms is each policy's measured hit rate, as the bandit could see it.
	Arms []timelineArm `json:"arms"`
}

type timelineArm struct {
	Policy  string  `json:"policy"`
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	HitRate float64 `json:"hit_rate"`
	Active  bool    `json:"active"`
}

func (c *timelineCache) sample() {
	c.mu.Lock()
	c.seen++
	due := c.seen%c.interval == 0
	seen := c.seen
	c.mu.Unlock()

	if !due {
		return
	}

	// Read outside the lock: Advice takes the cache's own read lock, and
	// holding this one across it would order two locks for no reason.
	active := c.inner.ActivePolicy()
	advice := c.inner.Advice()

	frame := timelineFrame{Request: seen, Active: active.String(), Epochs: advice.Epochs}
	for _, report := range advice.Reports {
		frame.Arms = append(frame.Arms, timelineArm{
			Policy:  report.Policy.String(),
			Hits:    report.Hits,
			Misses:  report.Misses,
			HitRate: report.HitRate(),
			Active:  report.Active,
		})
	}

	c.mu.Lock()
	c.samples = append(c.samples, active)
	c.frames = append(c.frames, frame)
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
		bandit.NewThompson(0.6, 9),
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
	frames := append([]timelineFrame(nil), cache.frames...)
	cache.mu.Unlock()

	require.NotEmpty(t, samples, "expected timeline samples")

	writeTimelineJSON(t, w, size, phases, result.HitRate(), frames)

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

// writeTimelineJSON dumps the run to the path in AS_CACHE_TIMELINE_JSON, for
// building an interactive view of it. It does nothing when the variable is
// unset, so the evidence run is unchanged by default.
func writeTimelineJSON(
	t *testing.T,
	w bench.Workload,
	size, phases int,
	hitRate float64,
	frames []timelineFrame,
) {
	t.Helper()

	path := os.Getenv("AS_CACHE_TIMELINE_JSON")
	if path == "" {
		return
	}

	payload := struct {
		Workload string          `json:"workload"`
		Requests int             `json:"requests"`
		Size     int             `json:"size"`
		Phases   int             `json:"phases"`
		HitRate  float64         `json:"hit_rate"`
		Frames   []timelineFrame `json:"frames"`
	}{
		Workload: w.Name,
		Requests: w.Len(),
		Size:     size,
		Phases:   phases,
		HitRate:  hitRate,
		Frames:   frames,
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, encoded, 0o600))

	t.Logf("wrote timeline trace to %s (%d frames)", path, len(frames))
}
