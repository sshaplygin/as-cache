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

// TestRistrettoSetIsLossy records why ristretto's hit rate in the table above
// is not a like-for-like eviction comparison, so nobody has to rediscover it
// from a surprising number.
//
// Its Set is asynchronous and admission-gated: it can return having queued
// nothing at all. Filling a cache well under its capacity and immediately
// reading the keys back should be a hit on any conventional cache; here it is
// not.
func TestRistrettoSetIsLossy(t *testing.T) {
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
		if _, ok := cache.Get(strconv.Itoa(i)); ok {
			found++
		}
	}

	t.Logf("ristretto retained %d/%d keys written into a cache of %d", found, written, size)
	assert.Less(t, found, written,
		"if this ever passes with every key present, ristretto's Set became synchronous "+
			"and the caveat documented on the adapter should be revisited")
}

// TestCompetitorCapacityHonesty guards the assumption every hit-rate number in
// this file rests on: a cache asked to hold N entries holds about N.
//
// It exists because otter did not. Admission runs on the caller's goroutine
// and eviction on a maintenance pass, so a replay that writes flat out leaves
// the cache far over its limit - 1916 entries resident against a MaximumSize
// of 500, measured here. Every otter number in the first version of this
// comparison was therefore a cache four times the size of its rivals, which
// read as a decisive win on uniform traffic (44% against everyone else's 10%)
// and was nothing but the extra capacity. The adapter calls CleanUp; this test
// fails if that stops working, or if another library develops the same habit.
func TestCompetitorCapacityHonesty(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	const (
		size    = 500
		written = 5000
		// Half over is slack, not indifference. Approximate accounting is
		// normal here and varies run to run: over five runs theine held
		// between 500 and 604 entries (up to 1.21x), ristretto 518 to 540,
		// sturdyc 476 every time, otter exactly 500 once CleanUp is called.
		// A threshold set at the top of that spread would flake; this one sits
		// clear of it and still fails the 3.8x that prompted the test.
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
