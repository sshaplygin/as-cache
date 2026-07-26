package simplelfu

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sshaplygin/as-cache/lfu/internal"
)

func TestNewLFU_PositiveSize(t *testing.T) {
	c, err := NewLFU[string, int](10, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewLFU_ZeroSize(t *testing.T) {
	_, err := NewLFU[string, int](0, nil)
	require.Error(t, err)
}

func TestNewLFU_NegativeSize(t *testing.T) {
	_, err := NewLFU[string, int](-1, nil)
	require.Error(t, err)
}

func TestAdd_Basic(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	evicted := c.Add("a", 1)
	assert.False(t, evicted, "expected no eviction on first add")
	assert.Equal(t, 1, c.Len())
}

func TestAdd_UpdateExistingKey(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	evicted := c.Add("a", 2)
	assert.False(t, evicted, "expected no eviction when updating existing key")
	assert.Equal(t, 1, c.Len())

	val, ok := c.Peek("a")
	require.True(t, ok, "expected key 'a' to exist")
	assert.Equal(t, 2, val)
}

func TestAdd_Eviction(t *testing.T) {
	c, err := NewLFU[string, int](2, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	evicted := c.Add("c", 3)

	assert.True(t, evicted, "expected eviction when cache is full")
	assert.Equal(t, 2, c.Len())
}

func TestAdd_EvictsLeastFrequentlyUsed(t *testing.T) {
	c, err := NewLFU[string, int](2, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)

	// Access "a" to increase its frequency
	c.Get("a")

	// Adding "c" should evict "b" (lower frequency)
	c.Add("c", 3)

	assert.False(t, c.Contains("b"), "expected 'b' to be evicted (lower frequency)")
	assert.True(t, c.Contains("a"), "expected 'a' to still be in cache (higher frequency)")
	assert.True(t, c.Contains("c"), "expected 'c' to be in cache")
}

func TestAdd_EvictsOldestAmongSameFrequency(t *testing.T) {
	c, err := NewLFU[string, int](2, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)

	// Both have frequency 1. "a" was added first, so it should be evicted.
	c.Add("c", 3)

	assert.False(t, c.Contains("a"), "expected 'a' to be evicted (oldest with same frequency)")
	assert.True(t, c.Contains("b"), "expected 'b' to still be in cache")
	assert.True(t, c.Contains("c"), "expected 'c' to be in cache")
}

func TestAdd_EvictionCallback(t *testing.T) {
	var evictedKey string
	var evictedVal int
	onEvict := func(k string, v int) {
		evictedKey = k
		evictedVal = v
	}

	c, err := NewLFU[string, int](1, onEvict)
	require.NoError(t, err)

	c.Add("a", 10)
	c.Add("b", 20) // should evict "a"

	assert.Equal(t, "a", evictedKey)
	assert.Equal(t, 10, evictedVal)
}

func TestGet_Existing(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	c.Add("a", 42)
	val, ok := c.Get("a")
	require.True(t, ok, "expected key to be found")
	assert.Equal(t, 42, val)
}

func TestGet_NonExistent(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	val, ok := c.Get("missing")
	assert.False(t, ok, "expected key not to be found")
	assert.Equal(t, 0, val)
}

func TestGet_IncreasesFrequency(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	// Access "a" twice, "b" once
	c.Get("a")
	c.Get("a")
	c.Get("b")

	// "c" has lowest frequency (1), should be evicted
	c.Add("d", 4)

	assert.False(t, c.Contains("c"), "expected 'c' to be evicted (lowest frequency)")
	assert.True(t, c.Contains("a"), "expected 'a' to remain (highest frequency)")
	assert.True(t, c.Contains("b"), "expected 'b' to remain")
}

func TestContains(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	assert.False(t, c.Contains("a"), "expected Contains to return false for empty cache")

	c.Add("a", 1)
	assert.True(t, c.Contains("a"), "expected Contains to return true for existing key")
	assert.False(t, c.Contains("b"), "expected Contains to return false for non-existing key")
}

func TestPeek(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	// Peek on empty cache
	val, ok := c.Peek("a")
	assert.False(t, ok, "expected Peek to return false for empty cache")
	assert.Equal(t, 0, val)

	c.Add("a", 42)
	val, ok = c.Peek("a")
	require.True(t, ok, "expected Peek to find key")
	assert.Equal(t, 42, val)
}

func TestPeek_DoesNotChangeFrequency(t *testing.T) {
	c, err := NewLFU[string, int](2, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)

	// Peek at "a" multiple times -- should not increase frequency
	c.Peek("a")
	c.Peek("a")
	c.Peek("a")

	// Access "b" via Get to actually increase its frequency
	c.Get("b")

	// "a" should still have frequency 1, "b" has frequency 2
	// So "a" should be evicted
	c.Add("c", 3)

	assert.False(t, c.Contains("a"), "expected 'a' to be evicted (Peek should not affect frequency)")
	assert.True(t, c.Contains("b"), "expected 'b' to remain")
}

func TestRemove(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	// Remove from empty cache
	assert.False(t, c.Remove("a"), "expected Remove to return false for empty cache")

	c.Add("a", 1)
	c.Add("b", 2)

	present := c.Remove("a")
	assert.True(t, present, "expected Remove to return true for existing key")
	assert.False(t, c.Contains("a"), "expected key to be removed")
	assert.Equal(t, 1, c.Len())

	// Remove non-existing key
	assert.False(t, c.Remove("z"), "expected Remove to return false for non-existing key")
}

func TestRemove_EvictionCallback(t *testing.T) {
	var evictedKey string
	var evictedVal int
	onEvict := func(k string, v int) {
		evictedKey = k
		evictedVal = v
	}

	c, err := NewLFU[string, int](3, onEvict)
	require.NoError(t, err)

	c.Add("a", 10)
	c.Remove("a")

	assert.Equal(t, "a", evictedKey)
	assert.Equal(t, 10, evictedVal)
}

func TestLen(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	assert.Equal(t, 0, c.Len(), "expected len 0 for empty cache")

	c.Add("a", 1)
	assert.Equal(t, 1, c.Len())

	c.Add("b", 2)
	c.Add("c", 3)
	assert.Equal(t, 3, c.Len())

	// Should not grow beyond capacity
	c.Add("d", 4)
	assert.Equal(t, 3, c.Len(), "expected len 3 after eviction")
}

func TestPurge(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	c.Purge()

	assert.Equal(t, 0, c.Len(), "expected len 0 after purge")
	assert.False(t, c.Contains("a"))
	assert.False(t, c.Contains("b"))
	assert.False(t, c.Contains("c"))
}

func TestPurge_EvictionCallback(t *testing.T) {
	evicted := make(map[string]int)
	onEvict := func(k string, v int) {
		evicted[k] = v
	}

	c, err := NewLFU[string, int](3, onEvict)
	require.NoError(t, err)
	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	c.Purge()

	assert.Len(t, evicted, 3)
	for _, key := range []string{"a", "b", "c"} {
		assert.Contains(t, evicted, key)
	}
}

func TestPurge_ThenReuse(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Purge()

	// Cache should be reusable after purge
	c.Add("x", 10)
	val, ok := c.Get("x")
	require.True(t, ok, "expected cache to be reusable after purge")
	assert.Equal(t, 10, val)
}

func TestKeys(t *testing.T) {
	c, err := NewLFU[string, int](5, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	keys := c.Keys()
	assert.Len(t, keys, 3)
	assert.ElementsMatch(t, []string{"a", "b", "c"}, keys)
}

func TestKeys_EmptyCache(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)
	keys := c.Keys()
	assert.Len(t, keys, 0)
}

func TestValues(t *testing.T) {
	c, err := NewLFU[string, int](5, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	values := c.Values()
	assert.Len(t, values, 3)
	assert.ElementsMatch(t, []int{1, 2, 3}, values)
}

func TestValues_EmptyCache(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)
	values := c.Values()
	assert.Len(t, values, 0)
}

func TestCapacityOne(t *testing.T) {
	evicted := make(map[string]int)
	onEvict := func(k string, v int) {
		evicted[k] = v
	}

	c, err := NewLFU[string, int](1, onEvict)
	require.NoError(t, err)

	c.Add("a", 1)
	assert.Equal(t, 1, c.Len())

	c.Add("b", 2)
	assert.Equal(t, 1, c.Len(), "expected len 1 after eviction")
	assert.True(t, c.Contains("b"), "expected 'b' to be in cache")
	assert.False(t, c.Contains("a"), "expected 'a' to be evicted")
	assert.Equal(t, 1, evicted["a"])
}

func TestAdd_NoEvictCallbackWhenNil(t *testing.T) {
	// Verify no panic when onEvict is nil and eviction happens
	c, err := NewLFU[string, int](1, nil)
	require.NoError(t, err)
	c.Add("a", 1)
	c.Add("b", 2) // triggers eviction with nil callback -- should not panic
	assert.Equal(t, 1, c.Len())
}

// Table-driven tests for comprehensive Get/Add scenarios
func TestAddGet_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		adds     []struct {
			key string
			val int
		}
		gets       []string
		finalCheck struct {
			key    string
			exists bool
		}
	}{
		{
			name:     "single item cache hit",
			capacity: 5,
			adds: []struct {
				key string
				val int
			}{{"x", 100}},
			gets: nil,
			finalCheck: struct {
				key    string
				exists bool
			}{"x", true},
		},
		{
			name:     "non-existent key",
			capacity: 5,
			adds: []struct {
				key string
				val int
			}{{"x", 100}},
			gets: nil,
			finalCheck: struct {
				key    string
				exists bool
			}{"y", false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewLFU[string, int](tt.capacity, nil)
			require.NoError(t, err)
			for _, a := range tt.adds {
				c.Add(a.key, a.val)
			}
			for _, g := range tt.gets {
				c.Get(g)
			}
			_, ok := c.Get(tt.finalCheck.key)
			assert.Equal(t, tt.finalCheck.exists, ok)
		})
	}
}

func TestMultipleEvictions(t *testing.T) {
	var evictions []string
	onEvict := func(k string, v int) {
		evictions = append(evictions, k)
	}

	c, err := NewLFU[string, int](2, onEvict)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3) // evicts "a"
	c.Add("d", 4) // evicts "b"

	assert.Len(t, evictions, 2)
}

func TestFrequencyPromotionAcrossMultipleGets(t *testing.T) {
	c, err := NewLFU[string, int](3, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	// Give "c" the highest frequency
	c.Get("c")
	c.Get("c")
	c.Get("c")

	// Give "b" medium frequency
	c.Get("b")

	// "a" still has frequency 1, should be evicted first
	c.Add("d", 4)
	assert.False(t, c.Contains("a"), "expected 'a' to be evicted")

	// "d" now has frequency 1 (lowest), should be evicted next
	c.Add("e", 5)
	assert.False(t, c.Contains("d"), "expected 'd' to be evicted")

	// "b" and "c" should remain
	assert.True(t, c.Contains("b"), "expected 'b' to remain")
	assert.True(t, c.Contains("c"), "expected 'c' to remain")
}

// ---------------------------------------------------------------------------
// Bucket-invariant regression tests
//
// Every bucket in evictList must hold at least one entry, and minFreq must
// address a live bucket whenever the cache is non-empty. Each test below
// panicked with a nil-pointer dereference before that invariant was enforced.
// ---------------------------------------------------------------------------

// assertBucketInvariant fails if any frequency bucket is empty or if minFreq
// does not address a live bucket while entries remain.
func assertBucketInvariant[K comparable, V any](t *testing.T, c *LFU[K, V]) {
	t.Helper()

	for freq, bucket := range c.evictList {
		assert.NotZero(t, bucket.Length(), "bucket freq=%d must not be empty", freq)
	}

	if len(c.items) > 0 {
		bucket, found := c.evictList[c.minFreq]
		require.True(t, found, "minFreq=%d must address a live bucket", c.minFreq)
		assert.NotZero(t, bucket.Length(), "minFreq bucket must not be empty")
	}
}

// Add's eviction path must drop the minFreq bucket once it empties, otherwise a
// later minFreq recompute selects the stale empty bucket and dereferences nil.
func TestAdd_EvictDropsEmptiedBucket(t *testing.T) {
	c, err := NewLFU[string, int](2, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Get("a")
	c.Get("a")
	c.Get("b")

	c.Add("c", 3) // evicts "b" and empties its bucket
	assertBucketInvariant(t, c)

	c.Add("d", 4) // evicts "c" and empties its bucket
	assertBucketInvariant(t, c)

	k1, _, ok := c.RemoveOldest()
	require.True(t, ok, "first RemoveOldest must succeed")
	assert.Equal(t, "d", k1)
	assertBucketInvariant(t, c)

	k2, _, ok := c.RemoveOldest()
	require.True(t, ok, "second RemoveOldest must succeed (previously panicked)")
	assert.Equal(t, "a", k2)
	assertBucketInvariant(t, c)

	assert.Zero(t, c.Len(), "cache must be drained")
}

// Resize(0) drains the cache and leaves size==0; the next Add must not take the
// eviction branch and dereference a bucket that no longer exists.
func TestResize_ZeroThenAddDoesNotPanic(t *testing.T) {
	c, err := NewLFU[string, int](2, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	evicted := c.Resize(0)
	assert.Equal(t, 1, evicted, "Resize(0) must evict the single entry")
	assert.Zero(t, c.Len())

	c.Add("b", 2) // previously panicked: evictList[minFreq=0] was nil
	assertBucketInvariant(t, c)

	// A zero-capacity cache holds nothing. This assertion used to require the
	// opposite - that "b" was retrievable - which made LFU the only policy in
	// the repository that kept an entry it had no room for. Since the adaptive
	// layer resizes policies on its own, that inconsistency was a trap rather
	// than a feature.
	_, ok := c.Get("b")
	assert.False(t, ok, "a zero-capacity cache must not serve an entry it had no room for")
	assert.Zero(t, c.Len())

	// The cache stays bounded rather than growing without limit.
	c.Add("c", 3)
	assertBucketInvariant(t, c)
	assert.Zero(t, c.Len(), "degenerate size must hold nothing")
}

// Remove must drop an emptied bucket and repair minFreq, otherwise GetOldest
// reads the back of an empty bucket and dereferences nil.
func TestRemove_RepairsMinFreqWhenBucketEmpties(t *testing.T) {
	c, err := NewLFU[string, int](2, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Get("a") // "a" -> freq 2, leaving "b" alone in the freq-1 bucket

	require.True(t, c.Remove("b"), "removing 'b' empties the freq-1 bucket")
	assertBucketInvariant(t, c)

	k, v, ok := c.GetOldest() // previously panicked: minFreq still pointed at freq 1
	require.True(t, ok, "GetOldest must find 'a'")
	assert.Equal(t, "a", k)
	assert.Equal(t, 1, v)
}

// The bucket index is an internal invariant that the code above is responsible
// for upholding. These white-box cases corrupt it deliberately to prove the
// lookup helpers degrade to a miss rather than dereferencing a nil bucket,
// keeping the package free of panics (CLAUDE.md rule 5).
func TestCorruptedIndex_DegradesToMissInsteadOfPanic(t *testing.T) {
	t.Run("minFreq addresses a missing bucket", func(t *testing.T) {
		c, err := NewLFU[string, int](2, nil)
		require.NoError(t, err)
		c.Add("a", 1)
		c.minFreq = 99 // no such bucket exists

		_, _, ok := c.GetOldest()
		assert.False(t, ok, "GetOldest must report a miss")

		_, _, ok = c.RemoveOldest()
		assert.False(t, ok, "RemoveOldest must report a miss")
	})

	t.Run("minFreq addresses an empty bucket", func(t *testing.T) {
		c, err := NewLFU[string, int](2, nil)
		require.NoError(t, err)
		c.Add("a", 1)
		c.evictList[7] = internal.NewList[string, int]()
		c.minFreq = 7 // bucket exists but holds nothing

		_, _, ok := c.GetOldest()
		assert.False(t, ok, "GetOldest must report a miss")

		_, _, ok = c.RemoveOldest()
		assert.False(t, ok, "RemoveOldest must report a miss")
	})

	t.Run("Resize stops when eviction cannot progress", func(t *testing.T) {
		c, err := NewLFU[string, int](4, nil)
		require.NoError(t, err)
		c.Add("a", 1)
		c.Add("b", 2)
		c.minFreq = 99 // eviction can no longer find an entry to drop

		assert.Zero(t, c.Resize(1), "Resize must not report phantom evictions")
	})

	t.Run("detach ignores an entry whose bucket is absent", func(t *testing.T) {
		c, err := NewLFU[string, int](2, nil)
		require.NoError(t, err)
		c.Add("a", 1)

		c.detach(&internal.Entry[string, int]{Key: "ghost", Freq: 42})
		assert.Equal(t, 1, c.Len(), "cache must be unchanged")
	})
}

// TestLFU_ZeroCapacityHoldsNothing guards a cache resized to zero that still
// accepted entries: Add skipped its eviction step because there was nothing to
// evict, then stored the entry regardless. Every other policy in this
// repository holds nothing at zero capacity, and the adaptive layer resizes
// policies on its own, so the odd one out is a trap.
func TestLFU_ZeroCapacityHoldsNothing(t *testing.T) {
	cache, err := NewLFU[string, int](4, nil)
	require.NoError(t, err)

	for i := 0; i < 4; i++ {
		cache.Add("key-"+strconv.Itoa(i), i)
	}
	require.Equal(t, 4, cache.Len())

	cache.Resize(0)
	assert.Zero(t, cache.Len(), "resizing to zero must empty the cache")

	assert.False(t, cache.Add("fresh", 1), "a zero-capacity cache stores nothing")
	assert.Zero(t, cache.Len(), "a zero-capacity cache must stay empty")

	_, ok := cache.Get("fresh")
	assert.False(t, ok)
}
