package bench_test

import (
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bench"
	"github.com/sshaplygin/as-cache/policies"
	"github.com/sshaplygin/as-cache/policies/arc"
	"github.com/sshaplygin/as-cache/policies/tinylfu"
)

// valueBytes is the payload size per entry. Real caches hold objects, not
// ints, and the whole point of dropping values from demoted policies is only
// visible when a value costs more than a pointer.
const valueBytes = 256

// retainedBytes measures the heap still held after build runs, by settling the
// heap, building, settling again, and keeping the result alive across the
// second measurement so it cannot be collected early.
func retainedBytes(build func() any) uint64 {
	var before, after runtime.MemStats

	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)

	held := build()

	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)

	runtime.KeepAlive(held)

	if after.HeapAlloc < before.HeapAlloc {
		return 0
	}

	return after.HeapAlloc - before.HeapAlloc
}

// fillKeys returns the key set used by every memory configuration, so they are
// all measured holding exactly the same data.
func fillKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "memory-key-" + strconv.Itoa(i)
	}

	return keys
}

// TestMemoryMultiplier measures what the adaptive layer costs in memory
// relative to a single plain LRU holding the same entries.
//
// This is the number the README's disclaimer used to assert without measuring:
// running N policies in parallel was said to multiply memory by N. Shadow
// policies hold no real values, and with sampling they hold only a fraction of
// the keys, so the true multiplier should be far below N.
func TestMemoryMultiplier(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	const entries = 50000

	keys := fillKeys(entries)
	payload := func() []byte { return make([]byte, valueBytes) }

	baseline := retainedBytes(func() any {
		cache, err := policies.NewLRU[string, []byte](entries)
		require.NoError(t, err)
		for _, key := range keys {
			cache.Add(key, payload())
		}

		return cache
	})

	adaptive := func(rate float64) uint64 {
		return retainedBytes(func() any {
			arms := buildArms(t, entries)
			cache, err := ascache.NewAdaptiveCache(arms, NewNoSwitchBandit(), &ascache.Settings{
				EpochDuration:               time.Hour,
				EvictPartialCapacityFilling: true,
				ShadowSampleRate:            rate,
				MinShadowCapacity:           256,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = cache.Close() })

			for _, key := range keys {
				cache.Add(key, payload())
			}

			return cache
		})
	}

	full := adaptive(0)
	sampled := adaptive(0.05)

	mib := func(b uint64) float64 { return float64(b) / (1 << 20) }

	t.Logf("\nmemory holding %d entries of %d-byte values, %d policies\n"+
		"  single LRU            %7.1f MiB   (1.00x)\n"+
		"  adaptive, no sampling %7.1f MiB   (%.2fx)\n"+
		"  adaptive, sample 0.05 %7.1f MiB   (%.2fx)",
		entries, valueBytes, len(bench.FixedPolicies()),
		mib(baseline),
		mib(full), float64(full)/float64(baseline),
		mib(sampled), float64(sampled)/float64(baseline))

	require.Positive(t, baseline, "baseline measurement failed")

	assert.Less(t, sampled, full,
		"sampling should reduce what the shadows retain")

	// Shadows hold keys and bookkeeping but never real values, so even with no
	// sampling the multiplier must be far below the number of policies.
	assert.Less(t, float64(full)/float64(baseline), 3.0,
		"shadow policies hold no values, so six policies must not cost six times one")
}

// TestAllocationsPerOperation reports allocations on the hot path, which is
// the other half of the overhead question: bytes retained is what the cache
// costs at rest, allocations per op is what it costs to run.
func TestAllocationsPerOperation(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	const size = 10000

	keys := fillKeys(size)

	measure := func(name string, c interface {
		Get(string) ([]byte, bool)
		Add(string, []byte) bool
	},
	) {
		for _, key := range keys {
			c.Add(key, make([]byte, valueBytes))
		}

		result := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				c.Get(keys[i%size])
			}
		})

		t.Logf("  %-24s %8.1f ns/op  %6d B/op  %4d allocs/op",
			name, float64(result.NsPerOp()),
			result.AllocedBytesPerOp(), result.AllocsPerOp())
	}

	t.Log("\nGet on a warm cache:")

	lru, err := policies.NewLRU[string, []byte](size)
	require.NoError(t, err)
	measure("single LRU", lru)

	for _, rate := range []float64{0, 0.05} {
		arms := buildArms(t, size)
		cache, err := ascache.NewAdaptiveCache(arms, NewNoSwitchBandit(), &ascache.Settings{
			EpochDuration:               time.Hour,
			EvictPartialCapacityFilling: true,
			ShadowSampleRate:            rate,
			MinShadowCapacity:           256,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = cache.Close() })

		name := "adaptive, no sampling"
		if rate > 0 {
			name = "adaptive, sample 0.05"
		}
		measure(name, cache)
	}
}

// buildArms builds one arm per shipped policy over []byte values.
func buildArms(t *testing.T, size int) []ascache.Policy[string, []byte] {
	t.Helper()

	lru, err := policies.NewLRU[string, []byte](size)
	require.NoError(t, err)
	twoQ, err := policies.NewTwoQueue[string, []byte](size)
	require.NoError(t, err)
	arcPolicy, err := arc.NewPolicy[string, []byte](size)
	require.NoError(t, err)
	tiny, err := tinylfu.NewPolicy[string, []byte](size)
	require.NoError(t, err)

	return []ascache.Policy[string, []byte]{
		lru,
		twoQ,
		arcPolicy,
		tiny,
		policies.NewRandomPolicy[string, []byte](size),
		policies.NewTTL[string, []byte](size, time.Hour),
	}
}

// NewNoSwitchBandit returns a bandit that never switches, so a memory
// measurement is not perturbed by a migration mid-fill.
func NewNoSwitchBandit() ascache.Bandit { return noSwitchBandit{} }

type noSwitchBandit struct{}

func (noSwitchBandit) RecordStats(_ ascache.ShadowStats) {}
func (noSwitchBandit) SelectPolicy() ascache.PolicyType  { return ascache.Undefined }
