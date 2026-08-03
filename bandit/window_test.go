package bandit

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
)

func TestAggregate_WeightsBucketsByAge(t *testing.T) {
	window := []WindowCounts{
		{Bucket: 8, Arms: map[ArmKey]ascache.PolicyStats{
			{Policy: ascache.LRU, Role: RoleShadow}: {Hits: 100, Misses: 0},
		}},
		{Bucket: 10, Arms: map[ArmKey]ascache.PolicyStats{
			{Policy: ascache.LRU, Role: RoleShadow}: {Hits: 100, Misses: 0},
		}},
	}

	pooled := aggregate(window, 10, 0.5, EvidenceAll)

	// Bucket 10 is the newest so it is unweighted; bucket 8 is two buckets old
	// and carries 0.5^2 of its counts.
	assert.InDelta(t, 100+25.0, pooled[ascache.LRU].Hits, 1e-9)
}

func TestAggregate_IgnoresBucketsNewerThanTheReference(t *testing.T) {
	// The current bucket is still being written by the rest of the fleet.
	// Counting it would weight whichever replicas happened to sync first.
	window := []WindowCounts{
		{Bucket: 11, Arms: map[ArmKey]ascache.PolicyStats{
			{Policy: ascache.LRU, Role: RoleShadow}: {Hits: 999, Misses: 0},
		}},
		{Bucket: 10, Arms: map[ArmKey]ascache.PolicyStats{
			{Policy: ascache.LRU, Role: RoleShadow}: {Hits: 5, Misses: 5},
		}},
	}

	pooled := aggregate(window, 10, 1, EvidenceAll)

	assert.InDelta(t, 5.0, pooled[ascache.LRU].Hits, 1e-9)
	assert.InDelta(t, 5.0, pooled[ascache.LRU].Misses, 1e-9)
}

func TestAggregate_ShadowOnlyDropsActiveRole(t *testing.T) {
	window := []WindowCounts{
		{Bucket: 1, Arms: map[ArmKey]ascache.PolicyStats{
			{Policy: ascache.LRU, Role: RoleActive}:     {Hits: 90, Misses: 10},
			{Policy: ascache.LRU, Role: RoleShadow}:     {Hits: 40, Misses: 60},
			{Policy: ascache.TinyLFU, Role: RoleShadow}: {Hits: 50, Misses: 50},
		}},
	}

	all := aggregate(window, 1, 1, EvidenceAll)
	assert.InDelta(t, 0.65, all[ascache.LRU].hitRate(), 1e-9,
		"pooling both roles mixes the flattering active measurement in")

	shadowOnly := aggregate(window, 1, 1, EvidenceShadowOnly)
	assert.InDelta(t, 0.40, shadowOnly[ascache.LRU].hitRate(), 1e-9)
	assert.InDelta(t, 0.50, shadowOnly[ascache.TinyLFU].hitRate(), 1e-9)
}

func TestAggregate_EmptyWindowYieldsNoArms(t *testing.T) {
	assert.Empty(t, aggregate(nil, 5, 0.8, EvidenceAll))
}

func TestCapEvidence_PreservesRateAndBoundsTotal(t *testing.T) {
	pooled := map[ascache.PolicyType]weighted{
		ascache.LRU:     {Hits: 9_000_000, Misses: 1_000_000},
		ascache.TinyLFU: {Hits: 50, Misses: 50},
	}

	capEvidence(pooled, 1000)

	assert.InDelta(t, 0.9, pooled[ascache.LRU].hitRate(), 1e-9, "the rate must survive the cap exactly")
	assert.InDelta(t, 1000.0, pooled[ascache.LRU].total(), 1e-9)

	assert.InDelta(t, 100.0, pooled[ascache.TinyLFU].total(), 1e-9,
		"an arm already under the cap must not be touched")
}

func TestCapEvidence_NegativeCapDisablesIt(t *testing.T) {
	pooled := map[ascache.PolicyType]weighted{
		ascache.LRU: {Hits: 1e9, Misses: 1e9},
	}

	capEvidence(pooled, -1)

	assert.InDelta(t, 2e9, pooled[ascache.LRU].total(), 1)
}

// TestCapEvidence_RestoresExploration is the reason the cap exists: pooling
// multiplies evidence by the size of the fleet, and a posterior built on
// millions of observations stops moving. Two arms a fifth of a point apart
// should still both get drawn.
func TestCapEvidence_RestoresExploration(t *testing.T) {
	fleetScale := func(cap float64) int {
		pooled := map[ascache.PolicyType]weighted{
			ascache.LRU:     {Hits: 5_000_000, Misses: 5_000_000},
			ascache.TinyLFU: {Hits: 5_020_000, Misses: 4_980_000},
		}
		capEvidence(pooled, cap)

		rng := rand.New(rand.NewPCG(1, 2))
		lruWins := 0
		for range 200 {
			if draw(rng, pooled) == ascache.LRU {
				lruWins++
			}
		}

		return lruWins
	}

	assert.Zero(t, fleetScale(-1),
		"uncapped, ten million observations make the marginally worse arm unreachable")
	assert.Greater(t, fleetScale(DefaultMaxEvidence), 10,
		"capped, the marginally worse arm is still explored")
}

func TestDraw_PrefersTheBetterArm(t *testing.T) {
	pooled := map[ascache.PolicyType]weighted{
		ascache.LRU:     {Hits: 100, Misses: 900},
		ascache.TinyLFU: {Hits: 900, Misses: 100},
	}

	rng := rand.New(rand.NewPCG(42, 42))
	wins := 0
	for range 500 {
		if draw(rng, pooled) == ascache.TinyLFU {
			wins++
		}
	}

	assert.Greater(t, wins, 495, "a nine-to-one better arm should win nearly always")
}

func TestDraw_EmptyEvidenceIsUndefined(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 1))
	assert.Equal(t, ascache.Undefined, draw(rng, nil))
}

func TestDraw_DoesNotDependOnMapIterationOrder(t *testing.T) {
	// Two arms with identical evidence must produce the same sequence of draws
	// from the same seed, however Go happens to order the map that run.
	build := func() map[ascache.PolicyType]weighted {
		return map[ascache.PolicyType]weighted{
			ascache.LRU:      {Hits: 10, Misses: 10},
			ascache.LFU:      {Hits: 10, Misses: 10},
			ascache.TwoQueue: {Hits: 10, Misses: 10},
			ascache.TinyLFU:  {Hits: 10, Misses: 10},
		}
	}

	sequence := func() []ascache.PolicyType {
		rng := rand.New(rand.NewPCG(7, 7))
		pooled := build()
		out := make([]ascache.PolicyType, 0, 50)
		for range 50 {
			out = append(out, draw(rng, pooled))
		}

		return out
	}

	first := sequence()
	for range 20 {
		require.Equal(t, first, sequence())
	}
}

func TestWeighted_HitRateOfNothingIsZero(t *testing.T) {
	var empty weighted
	assert.Zero(t, empty.hitRate())
	assert.False(t, math.IsNaN(empty.hitRate()))
}
