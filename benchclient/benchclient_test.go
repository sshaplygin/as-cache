package benchclient_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/benchclient"
	"github.com/sshaplygin/as-cache/policies"
)

// zipfish returns a deterministic, skewed key sequence: the same trace every
// run, with a working set larger than the capacities used below so that
// eviction actually happens and the policies can disagree.
func zipfish(n int) []uint64 {
	trace := make([]uint64, 0, n)
	state := uint64(1)
	for range n {
		// xorshift64: deterministic, no dependency, good enough to spread keys.
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		// Square the uniform draw to bias towards low keys, which is the shape
		// a cache benchmark cares about.
		u := float64(state%10_000) / 10_000
		trace = append(trace, uint64(u*u*4_000))
	}

	return trace
}

// replay drives the adapter exactly as a hit-ratio harness does: look up, and
// on a miss install. It returns the hit count and the policy the cache settled
// on.
func replay(t *testing.T, c *benchclient.Cache[uint64, uint64], capacity int, trace []uint64) (int, ascache.PolicyType) {
	t.Helper()

	c.Init(capacity)
	defer c.Close()

	hits := 0
	for _, key := range trace {
		if _, ok := c.Get(key); ok {
			hits++
			continue
		}
		c.Set(key, key)
	}

	return hits, c.ActivePolicy()
}

// TestReplayIsReproducible is the property the package exists for. Two replays
// of one trace must agree exactly - not approximately - or no number this
// adapter produces can be compared with another.
func TestReplayIsReproducible(t *testing.T) {
	t.Parallel()

	trace := zipfish(50_000)

	firstHits, firstPolicy := replay(t, &benchclient.Cache[uint64, uint64]{}, 500, trace)
	secondHits, secondPolicy := replay(t, &benchclient.Cache[uint64, uint64]{}, 500, trace)

	assert.Equal(t, firstHits, secondHits, "the same trace must produce the same hit count")
	assert.Equal(t, firstPolicy, secondPolicy, "the same trace must settle on the same policy")
	assert.Positive(t, firstHits, "the replay must actually have hit something")
}

// TestEpochsActuallyRun guards the setting that makes the replay deterministic.
// If EpochRequests were ever dropped, the cache would still serve traffic and
// this suite would still pass on hit counts alone - it would simply never adapt,
// and would be benchmarking its first policy under another name.
func TestEpochsActuallyRun(t *testing.T) {
	t.Parallel()

	trace := zipfish(60_000)
	c := &benchclient.Cache[uint64, uint64]{EpochRequests: 1_000}

	_, settled := replay(t, c, 300, trace)
	assert.NotEqual(t, ascache.Undefined, settled)
}

func TestDefaults(t *testing.T) {
	t.Parallel()

	c := &benchclient.Cache[uint64, uint64]{}
	assert.Equal(t, benchclient.DefaultName, c.Name())
	assert.Equal(t, ascache.Undefined, c.ActivePolicy(), "no policy is active before Init")

	// Close before Init must not panic: a harness that skips a cache still
	// closes it.
	assert.NotPanics(t, c.Close)
}

func TestLabelOverridesName(t *testing.T) {
	t.Parallel()

	c := &benchclient.Cache[uint64, uint64]{Label: "as-cache (2 arms)"}
	assert.Equal(t, "as-cache (2 arms)", c.Name())
}

func TestCustomArms(t *testing.T) {
	t.Parallel()

	built := 0
	c := &benchclient.Cache[uint64, uint64]{
		Arms: func(capacity int) ([]ascache.Policy[uint64, uint64], error) {
			built++
			lru, err := policies.NewLRU[uint64, uint64](capacity)
			if err != nil {
				return nil, err
			}

			return []ascache.Policy[uint64, uint64]{lru}, nil
		},
	}

	hits, _ := replay(t, c, 200, zipfish(5_000))
	assert.Equal(t, 1, built, "the harness builds arms once per Init")
	assert.Positive(t, hits)
}

func TestDefaultArmsAreDistinctAndPatentFree(t *testing.T) {
	t.Parallel()

	arms, err := benchclient.DefaultArms[uint64, uint64](256)
	require.NoError(t, err)
	require.Len(t, arms, 4)

	seen := make(map[ascache.PolicyType]bool, len(arms))
	for _, arm := range arms {
		require.False(t, seen[arm.GetType()], "duplicate arm %s would be rejected by the cache", arm.GetType())
		seen[arm.GetType()] = true
		assert.Equal(t, 256, arm.Cap())
	}

	assert.False(t, seen[ascache.ARC], "ARC is patented and must not be pulled in by default")
	assert.False(t, seen[ascache.TinyLFU],
		"W-TinyLFU is not reproducible and must be opted into, not defaulted to")
}

func TestArmsWithWindowTinyLFUAddsExactlyThatArm(t *testing.T) {
	t.Parallel()

	base, err := benchclient.DefaultArms[uint64, uint64](256)
	require.NoError(t, err)

	withTinyLFU, err := benchclient.ArmsWithWindowTinyLFU[uint64, uint64](256)
	require.NoError(t, err)

	require.Len(t, withTinyLFU, len(base)+1)
	assert.Equal(t, ascache.TinyLFU, withTinyLFU[len(withTinyLFU)-1].GetType())
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	c := &benchclient.Cache[uint64, uint64]{}
	c.Init(64)
	c.Close()
	assert.NotPanics(t, c.Close)
}
