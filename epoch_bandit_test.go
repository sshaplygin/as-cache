package ascache

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// epochRecordingBandit implements the optional EpochBandit extension and
// records both reporting shapes, so a test can assert that only one of them is
// ever used.
type epochRecordingBandit struct {
	mu       sync.Mutex
	next     PolicyType
	reports  []EpochReport
	perArm   []ShadowStats
	selected int
}

func (b *epochRecordingBandit) RecordStats(stats ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.perArm = append(b.perArm, stats)
}

func (b *epochRecordingBandit) RecordEpoch(report EpochReport) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reports = append(b.reports, report)
}

func (b *epochRecordingBandit) SelectPolicy() PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selected++
	return b.next
}

func (b *epochRecordingBandit) snapshot() ([]EpochReport, []ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.reports), slices.Clone(b.perArm)
}

func (b *epochRecordingBandit) setNext(policy PolicyType) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next = policy
}

// makeEpochCache builds a cache driven by an EpochBandit with the epoch ticker
// far enough out that every reporting epoch in the test is one the test
// triggered itself.
func makeEpochCache(t *testing.T, settings *Settings) (
	*AdaptiveCache[string, int],
	*epochRecordingBandit,
) {
	t.Helper()

	bandit := &epochRecordingBandit{next: LRU}
	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{
			newMockPolicy[string, int](LRU, 10),
			newMockPolicy[string, int](TinyLFU, 10),
			newMockPolicy[string, int](LFU, 10),
		},
		bandit,
		settings,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	return ac, bandit
}

func defaultEpochSettings() *Settings {
	return &Settings{
		EpochDuration:               24 * time.Hour,
		EvictPartialCapacityFilling: true,
	}
}

func TestEpochBandit_ReplacesPerArmReporting(t *testing.T) {
	ac, bandit := makeEpochCache(t, defaultEpochSettings())

	ac.Add("a", 1)
	ac.Get("a")
	ac.Get("missing")

	ac.tryChangePolicy()

	reports, perArm := bandit.snapshot()
	require.Len(t, reports, 1, "expected exactly one RecordEpoch per reporting epoch")
	assert.Empty(t, perArm, "a bandit implementing EpochBandit must not also receive RecordStats")
}

func TestEpochBandit_ReportCoversEveryArmInPolicyOrder(t *testing.T) {
	ac, bandit := makeEpochCache(t, defaultEpochSettings())

	ac.Add("a", 1)
	ac.Get("a")

	ac.tryChangePolicy()

	reports, _ := bandit.snapshot()
	require.Len(t, reports, 1)

	got := make([]PolicyType, 0, len(reports[0].Stats))
	for _, stats := range reports[0].Stats {
		got = append(got, stats.Policy)
	}

	// Sorted by PolicyType, not by the map's iteration order: LFU(2) before
	// TinyLFU(7) even though TinyLFU was passed to the constructor first.
	assert.Equal(t, []PolicyType{LRU, LFU, TinyLFU}, got)
}

func TestEpochBandit_ReportOrderIsStableAcrossEpochs(t *testing.T) {
	ac, bandit := makeEpochCache(t, defaultEpochSettings())

	const epochs = 20
	for i := range epochs {
		ac.Add("a", i)
		ac.Get("a")
		ac.tryChangePolicy()
	}

	reports, _ := bandit.snapshot()
	require.Len(t, reports, epochs)

	first := make([]PolicyType, 0, len(reports[0].Stats))
	for _, stats := range reports[0].Stats {
		first = append(first, stats.Policy)
	}

	// Ranging a map would pass this by chance roughly (1/6)^19 of the time.
	for i, report := range reports {
		order := make([]PolicyType, 0, len(report.Stats))
		for _, stats := range report.Stats {
			order = append(order, stats.Policy)
		}
		assert.Equal(t, first, order, "epoch %d reported a different arm order", i)
	}
}

func TestEpochBandit_ReportCarriesActivePolicyAndEpochID(t *testing.T) {
	ac, bandit := makeEpochCache(t, defaultEpochSettings())
	ac.Add("a", 1)

	// tryChangePolicy reports without advancing epochID or switching, so drive
	// runEpoch instead: the active policy and the ID both have to move.
	bandit.setNext(TinyLFU)
	ac.runEpoch()
	ac.runEpoch()

	reports, _ := bandit.snapshot()
	require.Len(t, reports, 2)

	assert.Equal(t, LRU, reports[0].Active, "first epoch was served by the initial policy")
	assert.Equal(t, int64(0), reports[0].EpochID)

	assert.Equal(t, TinyLFU, reports[1].Active, "second epoch was served by the policy the first switched to")
	assert.Equal(t, int64(1), reports[1].EpochID)
}

func TestEpochBandit_ReportCarriesCacheShape(t *testing.T) {
	settings := defaultEpochSettings()
	settings.ShadowSampleRate = 0.25
	settings.MinShadowCapacity = 1

	ac, bandit := makeEpochCache(t, settings)
	ac.Add("a", 1)
	ac.tryChangePolicy()

	reports, _ := bandit.snapshot()
	require.Len(t, reports, 1)

	assert.Equal(t, 10, reports[0].Capacity, "the capacity the cache actually serves at")
	assert.InDelta(t, 0.25, reports[0].SampleRate, 1e-9)
}

func TestEpochBandit_ReportedCapacityFollowsResize(t *testing.T) {
	ac, bandit := makeEpochCache(t, defaultEpochSettings())

	ac.Resize(40)
	ac.Add("a", 1)
	ac.tryChangePolicy()

	reports, _ := bandit.snapshot()
	require.Len(t, reports, 1)
	assert.Equal(t, 40, reports[0].Capacity)
}

func TestEpochBandit_ReportCountsMatchPerArmReporting(t *testing.T) {
	// The same traffic against the same arms must produce the same numbers
	// whichever reporting shape the bandit asks for.
	traffic := func(ac *AdaptiveCache[string, int]) {
		ac.Add("a", 1)
		ac.Add("b", 2)
		ac.Get("a")
		ac.Get("a")
		ac.Get("b")
		ac.Get("nope")
	}

	epochCache, epochBandit := makeEpochCache(t, defaultEpochSettings())
	traffic(epochCache)
	epochCache.tryChangePolicy()

	plainBandit := &recordingBandit{next: LRU}
	plainCache, err := NewAdaptiveCache(
		[]Policy[string, int]{
			newMockPolicy[string, int](LRU, 10),
			newMockPolicy[string, int](TinyLFU, 10),
			newMockPolicy[string, int](LFU, 10),
		},
		plainBandit,
		defaultEpochSettings(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plainCache.Close() })

	traffic(plainCache)
	plainCache.tryChangePolicy()

	reports, _ := epochBandit.snapshot()
	require.Len(t, reports, 1)

	viaEpoch := make(map[PolicyType]ShadowStats, len(reports[0].Stats))
	for _, stats := range reports[0].Stats {
		viaEpoch[stats.Policy] = stats
	}

	viaPerArm := make(map[PolicyType]ShadowStats)
	for _, stats := range plainBandit.getRecords() {
		viaPerArm[stats.Policy] = stats
	}

	assert.Equal(t, viaPerArm, viaEpoch)
}

func TestEpochBandit_ReportIsNotReusedAcrossEpochs(t *testing.T) {
	// RecordEpoch documents that an implementation may retain the report, so a
	// later epoch must not write through the slice an earlier one handed over.
	ac, bandit := makeEpochCache(t, defaultEpochSettings())

	ac.Add("a", 1)
	ac.Get("a")
	ac.tryChangePolicy()

	for range 5 {
		ac.Get("a")
		ac.Get("miss")
	}
	ac.tryChangePolicy()

	reports, _ := bandit.snapshot()
	require.Len(t, reports, 2)

	assert.NotSame(t, &reports[0].Stats[0], &reports[1].Stats[0],
		"the second epoch reused the first epoch's backing array")

	var firstTotal int64
	for _, stats := range reports[0].Stats {
		firstTotal += stats.Hits + stats.Misses
	}
	assert.Equal(t, int64(3), firstTotal,
		"the first report changed after it was delivered: expected the one Get it covered, per arm")
}

func TestEpochBandit_GatedEpochReportsNothing(t *testing.T) {
	settings := defaultEpochSettings()
	settings.EvictPartialCapacityFilling = false

	ac, bandit := makeEpochCache(t, settings)

	// One entry in a cache of ten: the capacity gate skips the whole report.
	ac.Add("a", 1)
	ac.Get("a")
	ac.tryChangePolicy()

	reports, perArm := bandit.snapshot()
	assert.Empty(t, reports, "a gated epoch measured nothing and must report nothing")
	assert.Empty(t, perArm)
}

func TestEpochBandit_ObserveOnlyStillReports(t *testing.T) {
	settings := defaultEpochSettings()
	settings.ObserveOnly = true

	ac, bandit := makeEpochCache(t, settings)
	bandit.setNext(TinyLFU)

	ac.Add("a", 1)
	ac.Get("a")
	ac.runEpoch()

	reports, _ := bandit.snapshot()
	require.Len(t, reports, 1, "observe-only measures and reports; it only declines to act")
	assert.Equal(t, LRU, reports[0].Active)
	assert.Equal(t, LRU, ac.ActivePolicy(), "observe-only must not apply the selection")
}

func TestRunEpoch_UnrecognisedSelectionMeansNoChange(t *testing.T) {
	// A bandit that has not formed an opinion yet returns Undefined, and a
	// distributed one does so for as long as it takes to reach its store.
	// Looking that up in the policy map yields a nil interface, so switching
	// to it used to panic the epoch goroutine and take the process down.
	for _, selection := range []PolicyType{Undefined, ARC} {
		t.Run(selection.String(), func(t *testing.T) {
			ac, err := NewAdaptiveCache(
				[]Policy[string, int]{
					newMockPolicy[string, int](LRU, 10),
					newMockPolicy[string, int](LFU, 10),
				},
				&mockBandit{next: selection},
				defaultEpochSettings(),
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = ac.Close() })

			ac.Add("a", 1)
			require.NotPanics(t, ac.runEpoch)

			assert.Equal(t, LRU, ac.ActivePolicy(), "an unrecognised selection must leave the active policy alone")

			// The cache must still be serving, not left in a torn state.
			value, ok := ac.Get("a")
			assert.True(t, ok)
			assert.Equal(t, 1, value)
		})
	}
}

func TestMigration_DefaultStrategyNeverServesAShadowZero(t *testing.T) {
	// Settings.MigrationStrategy is documented as defaulting to MigrationCold,
	// and every strategy has to purge the incoming policy's zero-value shadow
	// entries before it starts serving. A strategy value the switch does not
	// recognise - the zero value among them - must not skip that and hand
	// callers a shadow zero as if it were cached data.
	for _, strategy := range []MigrationStrategy{
		0, MigrationCold, MigrationWarm, MigrationGradual,
	} {
		t.Run(fmt.Sprint(uint(strategy)), func(t *testing.T) {
			ac, err := NewAdaptiveCache(
				[]Policy[string, int]{
					newMockPolicy[string, int](LRU, 10),
					newMockPolicy[string, int](LFU, 10),
				},
				&mockBandit{next: LFU},
				&Settings{
					EpochDuration:               time.Hour,
					EvictPartialCapacityFilling: true,
					MigrationStrategy:           strategy,
				},
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = ac.Close() })

			ac.Add("real", 42)
			// LFU is shadowing, so it now holds "real" mapped to a zero value.

			ac.runEpoch()
			require.Equal(t, LFU, ac.ActivePolicy())

			value, ok := ac.Get("real")
			if ok {
				assert.Equal(t, 42, value, "served a shadow zero as real data")
			}
		})
	}
}

func TestBandit_WithoutExtensionStillReceivesPerArmStats(t *testing.T) {
	// The extension is optional: a plain Bandit must be unaffected by its
	// existence.
	ac, _, _, _ := makeCache(t, MigrationCold)
	require.Nil(t, ac.epochBandit)

	bandit := &recordingBandit{next: LRU}
	plain, err := NewAdaptiveCache(
		[]Policy[string, int]{
			newMockPolicy[string, int](LRU, 10),
			newMockPolicy[string, int](LFU, 10),
		},
		bandit,
		defaultEpochSettings(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plain.Close() })

	plain.Add("a", 1)
	plain.Get("a")
	plain.tryChangePolicy()

	assert.Len(t, bandit.getRecords(), 2, "expected one RecordStats per arm")
}
