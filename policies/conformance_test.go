package policies_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/policies"
)

// newPolicy builds a policy of the given capacity for the conformance suite.
type newPolicy func(t *testing.T, size int) ascache.Policy[string, int]

// policiesUnderTest is every policy this module provides. ARC is absent by
// design: it lives in its own module and runs the same suite there.
var policiesUnderTest = map[string]newPolicy{
	"lru": func(t *testing.T, size int) ascache.Policy[string, int] {
		t.Helper()
		p, err := policies.NewLRU[string, int](size)
		require.NoError(t, err)

		return p
	},
	"lfu": func(t *testing.T, size int) ascache.Policy[string, int] {
		t.Helper()
		p, err := policies.NewLFU[string, int](size)
		require.NoError(t, err)

		return p
	},
	"2q": func(t *testing.T, size int) ascache.Policy[string, int] {
		t.Helper()
		p, err := policies.NewTwoQueue[string, int](size)
		require.NoError(t, err)

		return p
	},
	"ttl": func(t *testing.T, size int) ascache.Policy[string, int] {
		t.Helper()

		// A long TTL keeps expiry out of the way: this suite is about the
		// Cacher contract, not about expiry behaviour.
		return policies.NewTTL[string, int](size, time.Hour)
	},
	"random": func(t *testing.T, size int) ascache.Policy[string, int] {
		t.Helper()

		return policies.NewRandomPolicy[string, int](size)
	},
}

// TestPolicyConformance runs the Cacher/Policy contract against every policy.
// Anything an AdaptiveCache relies on belongs here, because a policy that
// breaks one of these is a policy the adaptive layer will mis-drive.
func TestPolicyConformance(t *testing.T) {
	for name, build := range policiesUnderTest {
		t.Run(name, func(t *testing.T) {
			runConformance(t, build)
		})
	}
}

func runConformance(t *testing.T, build newPolicy) {
	t.Helper()

	t.Run("stores and retrieves", func(t *testing.T) {
		p := build(t, 10)
		p.Add("a", 1)

		got, ok := p.Get("a")
		require.True(t, ok, "a stored key must be retrievable")
		assert.Equal(t, 1, got)
	})

	t.Run("reports a miss for an absent key", func(t *testing.T) {
		p := build(t, 10)

		got, ok := p.Get("nope")
		assert.False(t, ok)
		assert.Zero(t, got, "a miss must return the zero value")
	})

	t.Run("overwrites without growing", func(t *testing.T) {
		p := build(t, 10)
		p.Add("a", 1)
		p.Add("a", 2)

		got, ok := p.Get("a")
		require.True(t, ok)
		assert.Equal(t, 2, got, "re-adding a key must overwrite it")
		assert.Equal(t, 1, p.Len(), "re-adding a key must not add an entry")
	})

	t.Run("never exceeds capacity", func(t *testing.T) {
		const size = 10
		p := build(t, size)

		for i := 0; i < size*5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		assert.LessOrEqual(t, p.Len(), size, "a policy must not exceed its capacity")
		assert.Equal(t, size, p.Cap(), "Cap must report the configured capacity")
	})

	t.Run("Peek does not report a miss for a live key", func(t *testing.T) {
		p := build(t, 10)
		p.Add("a", 42)

		got, ok := p.Peek("a")
		require.True(t, ok)
		assert.Equal(t, 42, got)
	})

	t.Run("Contains agrees with Get", func(t *testing.T) {
		p := build(t, 10)
		p.Add("a", 1)

		assert.True(t, p.Contains("a"))
		assert.False(t, p.Contains("b"))
	})

	t.Run("Remove reports presence", func(t *testing.T) {
		p := build(t, 10)
		p.Add("a", 1)

		assert.True(t, p.Remove("a"), "removing a present key must report true")
		assert.False(t, p.Contains("a"))
		assert.False(t, p.Remove("a"), "removing an absent key must report false")
	})

	t.Run("Purge empties", func(t *testing.T) {
		p := build(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Purge()
		assert.Zero(t, p.Len())
		assert.Empty(t, p.Keys())
	})

	t.Run("Keys and Values agree with Len", func(t *testing.T) {
		p := build(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		assert.Len(t, p.Keys(), p.Len())
		assert.Len(t, p.Values(), p.Len())
	})

	t.Run("Resize shrinks to the new capacity", func(t *testing.T) {
		p := build(t, 20)
		for i := 0; i < 20; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}
		require.Positive(t, p.Len(), "expected entries before resizing")

		p.Resize(5)

		assert.LessOrEqual(t, p.Len(), 5, "Resize must enforce the new capacity")
		assert.Equal(t, 5, p.Cap(), "Cap must follow Resize - the adaptive layer relies on it")
	})

	t.Run("Resize grows without losing data", func(t *testing.T) {
		p := build(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}
		before := p.Len()

		p.Resize(100)

		assert.Equal(t, before, p.Len(), "growing must not evict")
		assert.Equal(t, 100, p.Cap())
	})

	t.Run("Resize to zero empties", func(t *testing.T) {
		p := build(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Resize(0)

		assert.Zero(t, p.Len(), "a zero-capacity policy must hold nothing")

		// Adding to a zero-capacity policy must not panic or retain anything.
		p.Add("x", 1)
		assert.Zero(t, p.Len())
	})

	t.Run("tracks hits and misses", func(t *testing.T) {
		p := build(t, 10)
		p.Add("a", 1)
		p.ResetStats()

		p.Get("a")
		p.Get("absent")

		stats := p.GetStats()
		assert.Equal(t, int64(1), stats.Hits)
		assert.Equal(t, int64(1), stats.Misses)

		p.ResetStats()
		assert.Equal(t, ascache.PolicyStats{}, p.GetStats())
	})

	t.Run("is safe under concurrent use", func(t *testing.T) {
		p := build(t, 100)

		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				for i := 0; i < 200; i++ {
					key := "key-" + strconv.Itoa((seed+i)%150)
					p.Add(key, i)
					p.Get(key)
					p.Contains(key)
					_ = p.Len()
					if i%20 == 0 {
						_ = p.Keys()
					}
					if i%50 == 0 {
						p.Remove(key)
					}
				}
			}(g)
		}
		wg.Wait()

		assert.LessOrEqual(t, p.Len(), 100)
	})
}

// TestPolicyTypesAreDistinct guards the wiring: two arms reporting the same
// PolicyType would collide in AdaptiveCache's policy map, and the constructor
// rejects that.
func TestPolicyTypesAreDistinct(t *testing.T) {
	seen := map[ascache.PolicyType]string{}
	for name, build := range policiesUnderTest {
		p := build(t, 10)
		policyType := p.GetType()

		if other, dup := seen[policyType]; dup {
			assert.Fail(t, "duplicate PolicyType",
				"%s and %s both report %s", name, other, policyType)
		}
		seen[policyType] = name

		assert.NotEqual(t, ascache.Undefined, policyType,
			"%s must report a defined PolicyType", name)
	}
}

// TestPoliciesDriveAnAdaptiveCache is the integration check: every policy this
// module provides must work as an arm of a real AdaptiveCache, including
// through a switch.
func TestPoliciesDriveAnAdaptiveCache(t *testing.T) {
	const size = 1000

	lruPolicy, err := policies.NewLRU[string, int](size)
	require.NoError(t, err)
	twoQ, err := policies.NewTwoQueue[string, int](size)
	require.NoError(t, err)

	cache, err := ascache.NewAdaptiveCache(
		[]ascache.Policy[string, int]{
			lruPolicy,
			twoQ,
			policies.NewRandomPolicy[string, int](size),
			policies.NewTTL[string, int](size, time.Hour),
		},
		&alternatingBandit{},
		&ascache.Settings{
			EpochDuration:               time.Millisecond,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           ascache.MigrationWarm,
			ShadowSampleRate:            0.05,
			MinShadowCapacity:           16,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	for i := 0; i < size; i++ {
		cache.Add("key-"+strconv.Itoa(i), i)
	}

	// Let several epochs elapse so the arms rotate through active duty.
	seenActive := map[ascache.PolicyType]struct{}{}
	served := 0
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		for i := 0; i < 100; i++ {
			seenActive[cache.ActivePolicy()] = struct{}{}
			key := "key-" + strconv.Itoa(i)
			if got, ok := cache.Get(key); ok {
				require.Equal(t, i, got,
					"key %q returned a value that was never stored - a shadow zero leaked", key)
				served++
			}
		}
	}

	// Without these the loop above proves nothing: an always-missing cache
	// satisfies every assertion in it vacuously, and a cache that never
	// switches never exercises migration between arms at all.
	assert.Positive(t, served, "the cache must actually serve hits for the value check to mean anything")
	assert.Greater(t, len(seenActive), 1,
		"the bandit must rotate the active policy so migration between arms is exercised, saw %v", seenActive)
}

// alternatingBandit cycles through the arms so every policy takes a turn as
// the active one.
type alternatingBandit struct {
	mu   sync.Mutex
	n    int
	arms []ascache.PolicyType
}

func (b *alternatingBandit) RecordStats(stats ascache.ShadowStats) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, arm := range b.arms {
		if arm == stats.Policy {
			return
		}
	}
	b.arms = append(b.arms, stats.Policy)
}

func (b *alternatingBandit) SelectPolicy() ascache.PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.arms) == 0 {
		return ascache.Undefined
	}
	b.n++

	return b.arms[b.n%len(b.arms)]
}
