package bench_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
	"github.com/sshaplygin/as-cache/bench"
)

// runCompetitors measures every rival cache library on a workload.
func runCompetitors(t *testing.T, w bench.Workload, size int) []bench.Result {
	t.Helper()

	results := make([]bench.Result, 0, len(bench.Competitors()))
	for _, builder := range bench.Competitors() {
		cache, err := builder.Build(size)
		require.NoError(t, err, "build %s", builder.Name)
		results = append(results, bench.Replay(builder.Name, cache, w))
	}

	return results
}

// runAdaptive replays this library, configured the way the README recommends
// rather than the way that flatters it.
func runAdaptive(t *testing.T, w bench.Workload, size int) bench.Result {
	t.Helper()

	arms, err := bench.AdaptiveArms(size)
	require.NoError(t, err)

	cache, err := ascache.NewAdaptiveCache(arms,
		bandit.NewThompson(0.7, 7),
		&ascache.Settings{
			// Request-counted rather than wall-clock. On a 2ms epoch this
			// same comparison moved 12 points between two runs, because how
			// often the cache re-evaluated depended on how loaded the machine
			// was. A competitor table built on that cannot be read.
			EpochRequests:               2_000,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           ascache.MigrationWarm,
		})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	return bench.Replay("as-cache (adaptive)", cache, w)
}

// TestAgainstOtherLibraries is the comparison a reader actually wants: not
// which of this repository's policies wins, but whether reaching for this
// library beats reaching for one of the well-known Go caches.
//
// The printed tables are the artifact. The assertions are deliberately weak,
// because the honest answer is not known in advance and a test that demanded a
// particular ordering would be asserting the conclusion rather than measuring
// it.
func TestAgainstOtherLibraries(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	const size = 500

	for _, w := range workloads() {
		t.Run(w.Name, func(t *testing.T) {
			results := runCompetitors(t, w, size)
			results = append(results, runAdaptive(t, w, size))

			t.Logf("\n%s (%d requests, cache %d)\n%s\n%s",
				w.Name, w.Len(), size, w.Description, bench.Table(results))

			for _, r := range results {
				assert.Positive(t, r.Hits+r.Misses, "%s served nothing", r.Policy)
			}
		})
	}
}

// TestRistrettoImmediateVisibility records the read-after-write behavior of
// asynchronous, admission-gated Set. Depending on scheduling, any number of the
// queued writes may be visible by the time they are read, including all of them.
// The count is a diagnostic; every value that is returned must still be correct.
func TestRistrettoImmediateVisibility(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	const size = 500

	var ristretto bench.CompetitorBuilder
	for _, builder := range bench.Competitors() {
		if builder.Name == "ristretto" {
			ristretto = builder
		}
	}
	require.NotNil(t, ristretto.Build, "ristretto competitor must exist")

	cache, err := ristretto.Build(size)
	require.NoError(t, err)

	// A tenth of capacity: nothing here should ever need evicting.
	const written = size / 10
	for i := range written {
		cache.Add(strconv.Itoa(i), i)
	}

	found := 0
	for i := range written {
		if value, ok := cache.Get(strconv.Itoa(i)); ok {
			assert.Equal(t, i, value, "value associated with key %d", i)
			found++
		}
	}

	t.Logf("ristretto retained %d/%d keys written into a cache of %d", found, written, size)
}

// TestCompetitorCapacityHonesty guards the assumption every hit-rate number in
// this file rests on: a cache asked to hold N entries holds about N.
//
// Asynchronous admission/eviction can let a write flood exceed nominal capacity.
// This test checks the configured adapters after that distinct workload; it does
// not measure resident entries during the zipf/loop/uniform hit-rate replays.
// The otter adapter calls CleanUp to finish pending maintenance.
func TestCompetitorCapacityHonesty(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	const (
		size    = 500
		written = 5000
		// Approximate accounting permits slack, but a sustained excess above
		// 1.5 times the requested size invalidates this comparison. Current
		// observed counts are retained in each evidence log, not copied here.
		tolerance = 1.5
	)

	for _, builder := range bench.Competitors() {
		t.Run(builder.Name, func(t *testing.T) {
			cache, err := builder.Build(size)
			require.NoError(t, err)

			for i := range written {
				cache.Add(strconv.Itoa(i), i)
			}

			resident := 0
			for i := range written {
				if _, ok := cache.Get(strconv.Itoa(i)); ok {
					resident++
				}
			}

			t.Logf("%s asked for %d, holds %d (%.1fx)",
				builder.Name, size, resident, float64(resident)/float64(size))
			assert.LessOrEqual(t, float64(resident), float64(size)*tolerance,
				"%s holds far more than the capacity it was given, so its hit rate "+
					"is not comparable with the others", builder.Name)
		})
	}
}
