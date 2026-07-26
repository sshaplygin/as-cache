package arc_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/policies/arc"
)

func newARC(t *testing.T, size int) ascache.Policy[string, int] {
	t.Helper()

	p, err := arc.NewPolicy[string, int](size)
	require.NoError(t, err)

	return p
}

// TestARCConformance mirrors the conformance suite the other policies run,
// since ARC lives in its own module and cannot share that file.
func TestARCConformance(t *testing.T) {
	t.Run("stores and retrieves", func(t *testing.T) {
		p := newARC(t, 10)
		p.Add("a", 1)

		got, ok := p.Get("a")
		require.True(t, ok)
		assert.Equal(t, 1, got)
	})

	t.Run("reports a miss for an absent key", func(t *testing.T) {
		p := newARC(t, 10)

		got, ok := p.Get("nope")
		assert.False(t, ok)
		assert.Zero(t, got)
	})

	t.Run("overwrites without growing", func(t *testing.T) {
		p := newARC(t, 10)
		p.Add("a", 1)
		p.Add("a", 2)

		got, ok := p.Get("a")
		require.True(t, ok)
		assert.Equal(t, 2, got)
		assert.Equal(t, 1, p.Len())
	})

	t.Run("never exceeds capacity", func(t *testing.T) {
		const size = 10
		p := newARC(t, size)

		for i := 0; i < size*5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		assert.LessOrEqual(t, p.Len(), size)
		assert.Equal(t, size, p.Cap())
	})

	t.Run("Remove reports presence", func(t *testing.T) {
		p := newARC(t, 10)
		p.Add("a", 1)

		assert.True(t, p.Remove("a"), "removing a present key must report true")
		assert.False(t, p.Remove("a"), "removing an absent key must report false")
	})

	t.Run("Add reports eviction only when full", func(t *testing.T) {
		p := newARC(t, 2)

		assert.False(t, p.Add("a", 1), "adding into a cache with room must not report an eviction")
		assert.False(t, p.Add("b", 2))
		assert.False(t, p.Add("a", 3), "overwriting must not report an eviction")
		assert.True(t, p.Add("c", 4), "adding into a full cache must report an eviction")
	})

	t.Run("Purge empties", func(t *testing.T) {
		p := newARC(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Purge()
		assert.Zero(t, p.Len())
	})

	t.Run("Resize shrinks and Cap follows", func(t *testing.T) {
		p := newARC(t, 20)
		for i := 0; i < 20; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Resize(5)

		assert.LessOrEqual(t, p.Len(), 5, "Resize must enforce the new capacity")
		assert.Equal(t, 5, p.Cap(), "Cap must follow Resize - the adaptive layer relies on it")
	})

	t.Run("Resize retains entries and leaves survivors intact", func(t *testing.T) {
		p := newARC(t, 10)
		for i := 0; i < 10; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Resize(3)

		// Which entries survive is not guaranteed - ARC's Keys() is recent
		// then frequent, not one recency order, so the rebuilt cache chooses.
		// What must hold is that the shrink retains what it can and that no
		// survivor comes back corrupted.
		assert.Equal(t, 3, p.Len(), "a shrink from 10 to 3 must retain 3 entries, not fewer")

		survivors := 0
		for i := 0; i < 10; i++ {
			key := "key-" + strconv.Itoa(i)
			if got, ok := p.Peek(key); ok {
				assert.Equal(t, i, got, "%s survived the resize with a corrupted value", key)
				survivors++
			}
		}
		assert.Equal(t, 3, survivors, "Len must agree with what is actually retrievable")
	})

	t.Run("Resize grows without losing data", func(t *testing.T) {
		p := newARC(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Resize(100)

		assert.Equal(t, 5, p.Len(), "growing must not evict")
		assert.Equal(t, 100, p.Cap())
	})

	t.Run("Resize to zero empties and refuses new entries", func(t *testing.T) {
		p := newARC(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Resize(0)
		assert.Zero(t, p.Len(), "a zero-capacity policy must hold nothing")

		p.Add("x", 1)
		assert.Zero(t, p.Len(), "a zero-capacity policy must not accept entries")
	})

	t.Run("tracks hits and misses", func(t *testing.T) {
		p := newARC(t, 10)
		p.Add("a", 1)
		p.ResetStats()

		p.Get("a")
		p.Get("absent")

		stats := p.GetStats()
		assert.Equal(t, int64(1), stats.Hits)
		assert.Equal(t, int64(1), stats.Misses)
	})

	t.Run("is safe under concurrent use, including resizes", func(t *testing.T) {
		p := newARC(t, 100)

		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				for i := 0; i < 200; i++ {
					key := "key-" + strconv.Itoa((seed+i)%150)
					p.Add(key, i)
					p.Get(key)
					_ = p.Len()
					if i%50 == 0 {
						_ = p.Keys()
					}
				}
			}(g)
		}

		// Resize concurrently: it swaps the underlying cache wholesale, which
		// is exactly the operation most likely to race with the workers.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				p.Resize(50 + i%100)
			}
		}()

		wg.Wait()
	})

	t.Run("reports the ARC policy type", func(t *testing.T) {
		assert.Equal(t, ascache.ARC, newARC(t, 10).GetType())
		assert.Equal(t, "ARC", ascache.ARC.String())
	})
}

// TestARCDrivesAnAdaptiveCache checks ARC works as a real bandit arm, and in
// particular that a caller never sees a value that was never stored while the
// cache switches between arms.
func TestARCDrivesAnAdaptiveCache(t *testing.T) {
	const size = 500

	arcPolicy, err := arc.NewPolicy[string, int](size)
	require.NoError(t, err)

	// ARC is the only arm here: a second ARC instance would report the same
	// PolicyType and the constructor rejects that collision by design.
	cache, err := ascache.NewAdaptiveCache(
		[]ascache.Policy[string, int]{arcPolicy},
		&fixedBandit{pick: ascache.ARC},
		&ascache.Settings{
			EpochDuration:               time.Millisecond,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           ascache.MigrationWarm,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	for i := 0; i < size; i++ {
		cache.Add("key-"+strconv.Itoa(i), i)
	}

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		for i := 0; i < 100; i++ {
			key := "key-" + strconv.Itoa(i)
			if got, ok := cache.Get(key); ok {
				require.Equal(t, i, got, "key %q returned a value that was never stored", key)
			}
		}
	}

	assert.Equal(t, ascache.ARC, cache.ActivePolicy())
}

// TestARCPolicyTypeIsDistinctFromTheOthers guards the enum wiring: ARC must
// not collide with a policy from the sibling module, or the two could not be
// used as arms of the same cache.
func TestARCPolicyTypeIsDistinctFromTheOthers(t *testing.T) {
	for _, other := range []ascache.PolicyType{
		ascache.Undefined, ascache.LRU, ascache.LFU,
		ascache.TwoQueue, ascache.Random, ascache.TTL,
	} {
		assert.NotEqual(t, other, ascache.ARC, "ARC must have its own PolicyType")
	}
}

type fixedBandit struct{ pick ascache.PolicyType }

func (b *fixedBandit) RecordStats(_ ascache.ShadowStats) {}
func (b *fixedBandit) SelectPolicy() ascache.PolicyType  { return b.pick }
