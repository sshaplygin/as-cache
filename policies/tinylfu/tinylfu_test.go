package tinylfu_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/policies/tinylfu"
)

func newTinyLFU(t *testing.T, size int) ascache.Policy[string, int] {
	t.Helper()

	p, err := tinylfu.NewPolicy[string, int](size)
	require.NoError(t, err)

	return p
}

func TestTinyLFUConformance(t *testing.T) {
	t.Run("stores and retrieves", func(t *testing.T) {
		p := newTinyLFU(t, 100)
		p.Add("a", 1)

		got, ok := p.Get("a")
		require.True(t, ok)
		assert.Equal(t, 1, got)
	})

	t.Run("reports a miss for an absent key", func(t *testing.T) {
		p := newTinyLFU(t, 100)

		got, ok := p.Get("nope")
		assert.False(t, ok)
		assert.Zero(t, got)
	})

	t.Run("overwrites without growing", func(t *testing.T) {
		p := newTinyLFU(t, 100)
		p.Add("a", 1)
		p.Add("a", 2)

		got, ok := p.Get("a")
		require.True(t, ok)
		assert.Equal(t, 2, got)
		assert.Equal(t, 1, p.Len())
	})

	t.Run("Peek does not report a miss for a live key", func(t *testing.T) {
		p := newTinyLFU(t, 100)
		p.Add("a", 42)

		got, ok := p.Peek("a")
		require.True(t, ok)
		assert.Equal(t, 42, got)
	})

	t.Run("Contains agrees with Peek", func(t *testing.T) {
		p := newTinyLFU(t, 100)
		p.Add("a", 1)

		assert.True(t, p.Contains("a"))
		assert.False(t, p.Contains("b"))
	})

	t.Run("Remove reports presence", func(t *testing.T) {
		p := newTinyLFU(t, 100)
		p.Add("a", 1)

		assert.True(t, p.Remove("a"), "removing a present key must report true")
		assert.False(t, p.Remove("a"), "removing an absent key must report false")
	})

	t.Run("Purge empties", func(t *testing.T) {
		p := newTinyLFU(t, 100)
		for i := 0; i < 50; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Purge()
		assert.Zero(t, p.Len())
	})

	t.Run("stays within capacity", func(t *testing.T) {
		const size = 100
		p := newTinyLFU(t, size)

		for i := 0; i < size*10; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		// otter evicts asynchronously and reports an approximate size, so this
		// asserts the bound is respected rather than an exact count.
		assert.LessOrEqual(t, p.Len(), size*2,
			"a cache of %d must not grow without bound", size)
		assert.Equal(t, size, p.Cap())
	})

	t.Run("Keys and Values are materialised", func(t *testing.T) {
		p := newTinyLFU(t, 100)
		for i := 0; i < 10; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		assert.NotEmpty(t, p.Keys(), "Keys must materialise otter's iterator")
		assert.NotEmpty(t, p.Values(), "Values must materialise otter's iterator")
	})

	t.Run("never serves a value that was never stored", func(t *testing.T) {
		p := newTinyLFU(t, 200)
		for i := 0; i < 500; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		for i := 0; i < 500; i++ {
			key := "key-" + strconv.Itoa(i)
			if got, ok := p.Peek(key); ok {
				require.Equal(t, i, got, "%s carries a value that was never stored", key)
			}
		}
	})

	t.Run("Resize shrinks and Cap follows", func(t *testing.T) {
		p := newTinyLFU(t, 500)
		for i := 0; i < 500; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Resize(50)

		assert.Equal(t, 50, p.Cap(), "Cap must follow Resize - the adaptive layer relies on it")
		assert.LessOrEqual(t, p.Len(), 100, "Resize must bring the cache down toward the new capacity")
	})

	t.Run("Resize grows", func(t *testing.T) {
		p := newTinyLFU(t, 100)
		for i := 0; i < 50; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Resize(1000)
		assert.Equal(t, 1000, p.Cap())
	})

	t.Run("tracks hits and misses", func(t *testing.T) {
		p := newTinyLFU(t, 100)
		p.Add("a", 1)
		p.ResetStats()

		p.Get("a")
		p.Get("absent")

		stats := p.GetStats()
		assert.Equal(t, int64(1), stats.Hits)
		assert.Equal(t, int64(1), stats.Misses)
	})

	t.Run("reports the TinyLFU policy type", func(t *testing.T) {
		assert.Equal(t, ascache.TinyLFU, newTinyLFU(t, 10).GetType())
		assert.Equal(t, "TinyLFU", ascache.TinyLFU.String())
	})

	t.Run("is safe under concurrent use, including resizes", func(t *testing.T) {
		p := newTinyLFU(t, 200)

		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				for i := 0; i < 300; i++ {
					key := "key-" + strconv.Itoa((seed+i)%400)
					p.Add(key, i)
					p.Get(key)
					p.Contains(key)
					_ = p.Len()
					if i%50 == 0 {
						_ = p.Keys()
					}
				}
			}(g)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				p.Resize(100 + i*10)
			}
		}()

		wg.Wait()
	})
}

// TestTinyLFUKeepsHotKeysUnderScan is the property the arm is carried for: a
// long scan of one-off keys must not flush a small set of repeatedly requested
// ones, which is exactly what defeats plain LRU.
func TestTinyLFUKeepsHotKeysUnderScan(t *testing.T) {
	const size = 500
	p := newTinyLFU(t, size)

	hot := make([]string, 50)
	for i := range hot {
		hot[i] = "hot-" + strconv.Itoa(i)
	}

	// Establish the hot set firmly in the frequency sketch.
	for round := 0; round < 40; round++ {
		for _, key := range hot {
			p.Add(key, 1)
			p.Get(key)
		}
	}

	// Now scan far more one-off keys than the cache can hold.
	for i := 0; i < size*20; i++ {
		p.Add("scan-"+strconv.Itoa(i), i)
	}

	retained := 0
	for _, key := range hot {
		if p.Contains(key) {
			retained++
		}
	}

	assert.Greater(t, retained, len(hot)/2,
		"W-TinyLFU should keep most of the hot set through a scan, kept %d of %d", retained, len(hot))
}

// TestTinyLFUDrivesAnAdaptiveCache checks the arm works inside a real cache,
// including the invariant that a caller never sees a value never stored.
func TestTinyLFUDrivesAnAdaptiveCache(t *testing.T) {
	const size = 1000

	arm, err := tinylfu.NewPolicy[string, int](size)
	require.NoError(t, err)

	cache, err := ascache.NewAdaptiveCache(
		[]ascache.Policy[string, int]{arm},
		&fixedBandit{pick: ascache.TinyLFU},
		&ascache.Settings{
			EpochDuration: time.Millisecond,
			// Len is approximate for this policy, so the capacity gate cannot
			// be relied on to fire - see Cache.Len.
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
		for i := 0; i < 200; i++ {
			key := "key-" + strconv.Itoa(i)
			if got, ok := cache.Get(key); ok {
				require.Equal(t, i, got, "key %q returned a value that was never stored", key)
			}
		}
	}

	assert.Equal(t, ascache.TinyLFU, cache.ActivePolicy())
}

type fixedBandit struct{ pick ascache.PolicyType }

func (b *fixedBandit) RecordStats(_ ascache.ShadowStats) {}
func (b *fixedBandit) SelectPolicy() ascache.PolicyType  { return b.pick }
