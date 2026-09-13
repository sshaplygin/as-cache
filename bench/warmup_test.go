package bench_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bench"
)

// switchOnceTo asks for a switch to target on its first selection and for no
// change on every later one, so the switch lands at a known request. Bandit
// calls are serialised by the cache, so done needs no lock.
type switchOnceTo struct {
	target ascache.PolicyType
	done   bool
}

func (b *switchOnceTo) RecordStats(ascache.ShadowStats) {}

func (b *switchOnceTo) SelectPolicy() ascache.PolicyType {
	if b.done {
		return ascache.Undefined
	}
	b.done = true

	return b.target
}

// warmupWindows are the reported request ranges, as offsets from the switch.
var warmupWindows = [][2]int{{0, 1000}, {1000, 5000}, {5000, 20000}, {20000, 100000}}

type warmupRow struct {
	name string
	hits []int
}

// TestSwitchWarmupCost measures what a switch costs in the requests right
// after it, per migration strategy.
//
// A switch is only worth making if the policy switched to earns back what the
// switch loses. The loss is concentrated in the requests immediately after it,
// where a cold policy starts empty, a warm one starts with the outgoing
// policy's contents, and a gradual one fills as keys are asked for. An average
// over a whole run hides that shape entirely, so hits are reported in windows
// measured from the switch.
//
// The switch is scripted rather than chosen, so every strategy switches at the
// same request, from LRU to LFU, on the evidence suite's zipf workload and
// cache size. Two references bracket the result: LFU replayed on its own over
// the whole trace, which is an LFU that never had to warm up, and LRU replayed
// on its own, which is not switching at all.
func TestSwitchWarmupCost(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	const (
		capacity = 500
		switchAt = 100000
	)
	w := bench.Zipf(2*switchAt, 20000, 1.1, 1)

	lruBuilder, lfuBuilder := fixedPolicy(t, "LRU"), fixedPolicy(t, "LFU")

	rows := make([]warmupRow, 0, 8)
	for _, ref := range []struct {
		name    string
		builder bench.PolicyBuilder
	}{
		{"LFU all along (no warm-up)", lfuBuilder},
		{"LRU, never switched", lruBuilder},
	} {
		policy, err := ref.builder.Build(capacity)
		require.NoError(t, err)
		rows = append(rows, warmupRow{ref.name, windowHits(policy, w, switchAt)})
	}

	for _, cfg := range []struct {
		name        string
		strategy    ascache.MigrationStrategy
		maxRequests int64
	}{
		{"cold", ascache.MigrationCold, 0},
		{"warm", ascache.MigrationWarm, 0},
		{"gradual", ascache.MigrationGradual, 0},
		{"gradual, capped at 10 Gets", ascache.MigrationGradual, 10},
		{"gradual, capped at 100 Gets", ascache.MigrationGradual, 100},
		{"gradual, capped at 1000 Gets", ascache.MigrationGradual, 1000},
	} {
		lru, err := lruBuilder.Build(capacity)
		require.NoError(t, err)
		lfu, err := lfuBuilder.Build(capacity)
		require.NoError(t, err)

		cache, err := ascache.NewAdaptiveCache(
			[]ascache.Policy[string, int]{lru, lfu},
			&switchOnceTo{target: ascache.LFU},
			&ascache.Settings{
				// The Get completing request switchAt ends the first epoch, so
				// request index switchAt is the first one the new policy serves.
				EpochRequests:               switchAt,
				EvictPartialCapacityFilling: true,
				MigrationStrategy:           cfg.strategy,
				MigrationMaxRequests:        cfg.maxRequests,
			},
		)
		require.NoError(t, err)

		hits := windowHits(cache, w, switchAt)
		require.Equal(t, ascache.LFU, cache.ActivePolicy(), "%s: the scripted switch must have happened", cfg.name)
		require.NoError(t, cache.Close())

		rows = append(rows, warmupRow{cfg.name, hits})
	}

	t.Logf("\nhit rate after a switch from LRU to LFU at request %d (%s, %d requests, cache %d)\n%s",
		switchAt, w.Name, len(w.Keys), capacity, warmupTable(rows))
}

func fixedPolicy(t *testing.T, name string) bench.PolicyBuilder {
	t.Helper()

	for _, builder := range bench.FixedPolicies() {
		if builder.Name == name {
			return builder
		}
	}
	require.FailNow(t, "no fixed policy named "+name)

	return bench.PolicyBuilder{}
}

// windowHits replays w through c as a read-through cache, filling on every miss
// as bench.Replay does, and counts the hits falling in each of warmupWindows.
func windowHits(c bench.Cache, w bench.Workload, switchAt int) []int {
	hits := make([]int, len(warmupWindows))

	for i, key := range w.Keys {
		_, ok := c.Get(key)
		if !ok {
			c.Add(key, i)

			continue
		}
		if i < switchAt {
			continue
		}

		offset := i - switchAt
		for j, window := range warmupWindows {
			if offset >= window[0] && offset < window[1] {
				hits[j]++
			}
		}
	}

	return hits
}

func warmupTable(rows []warmupRow) string {
	var b strings.Builder

	b.WriteString("| Configuration |")
	for _, window := range warmupWindows {
		fmt.Fprintf(&b, " %d-%d |", window[0], window[1])
	}
	b.WriteString("\n| --- |" + strings.Repeat(" --- |", len(warmupWindows)) + "\n")

	for _, row := range rows {
		fmt.Fprintf(&b, "| %s |", row.name)
		for j, window := range warmupWindows {
			fmt.Fprintf(&b, " %.2f%% |", 100*float64(row.hits[j])/float64(window[1]-window[0]))
		}
		b.WriteString("\n")
	}

	return b.String()
}
