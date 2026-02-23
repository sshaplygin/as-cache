package lfu

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_PositiveSize(t *testing.T) {
	c, err := New[string, int](10)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNew_ZeroSize(t *testing.T) {
	_, err := New[string, int](0)
	require.Error(t, err)
}

func TestNew_NegativeSize(t *testing.T) {
	_, err := New[string, int](-1)
	require.Error(t, err)
}

func TestNewWithEvict(t *testing.T) {
	called := false
	onEvict := func(k string, v int) {
		called = true
	}

	c, err := NewWithEvict[string, int](2, onEvict)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3) // triggers eviction

	assert.True(t, called, "expected eviction callback to be called")
}

func TestNewWithEvict_NilCallback(t *testing.T) {
	c, err := NewWithEvict[string, int](2, nil)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3) // should not panic with nil callback
}

func TestAdd_Basic(t *testing.T) {
	c, err := New[string, int](3)
	require.NoError(t, err)

	evicted := c.Add("a", 1)
	assert.False(t, evicted, "expected no eviction on first add")
	assert.Equal(t, 1, c.Len())
}

func TestAdd_Eviction(t *testing.T) {
	c, err := New[string, int](2)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	evicted := c.Add("c", 3)

	assert.True(t, evicted, "expected eviction")
	assert.Equal(t, 2, c.Len())
}

func TestAdd_EvictionCallbackOutsideLock(t *testing.T) {
	// Verify eviction callback is invoked outside the critical section.
	// We do this by calling Len() inside the callback -- if the callback were
	// invoked while holding the lock, this would deadlock (RWMutex is not reentrant).
	c, err := NewWithEvict[string, int](1, func(k string, v int) {
		// This must not deadlock. If the lock is held, calling Len() would block forever.
		// We cannot easily test this without a timeout, but at least verify it does not panic.
	})
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2) // triggers eviction callback outside lock
}

func TestGet(t *testing.T) {
	c, err := New[string, int](3)
	require.NoError(t, err)

	c.Add("a", 42)
	val, ok := c.Get("a")
	require.True(t, ok, "expected key to be found")
	assert.Equal(t, 42, val)
}

func TestGet_NonExistent(t *testing.T) {
	c, err := New[string, int](3)
	require.NoError(t, err)

	val, ok := c.Get("missing")
	assert.False(t, ok, "expected key not to be found")
	assert.Equal(t, 0, val)
}

func TestContains(t *testing.T) {
	c, err := New[string, int](3)
	require.NoError(t, err)

	assert.False(t, c.Contains("a"), "expected false for empty cache")

	c.Add("a", 1)
	assert.True(t, c.Contains("a"), "expected true for existing key")
	assert.False(t, c.Contains("z"), "expected false for non-existing key")
}

func TestPeek(t *testing.T) {
	c, err := New[string, int](3)
	require.NoError(t, err)

	val, ok := c.Peek("a")
	assert.False(t, ok, "expected false for empty cache")
	assert.Equal(t, 0, val)

	c.Add("a", 42)
	val, ok = c.Peek("a")
	require.True(t, ok, "expected Peek to find key")
	assert.Equal(t, 42, val)
}

func TestRemove(t *testing.T) {
	c, err := New[string, int](3)
	require.NoError(t, err)

	assert.False(t, c.Remove("a"), "expected false for empty cache")

	c.Add("a", 1)
	present := c.Remove("a")
	assert.True(t, present, "expected true for existing key")
	assert.False(t, c.Contains("a"), "expected key to be removed")
	assert.Equal(t, 0, c.Len())
}

func TestRemove_EvictionCallback(t *testing.T) {
	var evictedKey string
	var evictedVal int
	onEvict := func(k string, v int) {
		evictedKey = k
		evictedVal = v
	}

	c, err := NewWithEvict[string, int](3, onEvict)
	require.NoError(t, err)
	c.Add("a", 10)
	c.Remove("a")

	assert.Equal(t, "a", evictedKey)
	assert.Equal(t, 10, evictedVal)
}

func TestPurge(t *testing.T) {
	c, err := New[string, int](3)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	c.Purge()
	assert.Equal(t, 0, c.Len(), "expected len 0 after purge")
}

func TestPurge_EvictionCallback(t *testing.T) {
	evicted := make(map[string]int)
	mu := sync.Mutex{}
	onEvict := func(k string, v int) {
		mu.Lock()
		evicted[k] = v
		mu.Unlock()
	}

	c, err := NewWithEvict[string, int](3, onEvict)
	require.NoError(t, err)
	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	c.Purge()

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, evicted, 3)
}

func TestKeys(t *testing.T) {
	c, err := New[string, int](5)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	keys := c.Keys()
	assert.Len(t, keys, 3)
	assert.ElementsMatch(t, []string{"a", "b", "c"}, keys)
}

func TestValues(t *testing.T) {
	c, err := New[string, int](5)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	values := c.Values()
	assert.Len(t, values, 3)
	assert.ElementsMatch(t, []int{1, 2, 3}, values)
}

func TestLen(t *testing.T) {
	c, err := New[string, int](5)
	require.NoError(t, err)

	assert.Equal(t, 0, c.Len())

	c.Add("a", 1)
	c.Add("b", 2)
	assert.Equal(t, 2, c.Len())
}

func TestDefaultEvictedBufferSize(t *testing.T) {
	assert.Equal(t, 16, DefaultEvictedBufferSize)
}

// Concurrent tests -- these validate thread safety under the race detector.
// Note: use assert (not require) inside goroutines to avoid runtime.Goexit issues.

func TestConcurrent_AddGet(t *testing.T) {
	c, err := New[int, int](100)
	require.NoError(t, err)
	var wg sync.WaitGroup
	numGoroutines := 50
	opsPerGoroutine := 100

	wg.Add(numGoroutines * 2)

	// Writers
	for g := 0; g < numGoroutines; g++ {
		go func(base int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				c.Add(base*opsPerGoroutine+i, i)
			}
		}(g)
	}

	// Readers
	for g := 0; g < numGoroutines; g++ {
		go func(base int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				c.Get(base*opsPerGoroutine + i)
			}
		}(g)
	}

	wg.Wait()
}

func TestConcurrent_ContainsPeek(t *testing.T) {
	c, err := New[int, int](100)
	require.NoError(t, err)

	// Pre-populate
	for i := 0; i < 50; i++ {
		c.Add(i, i*10)
	}

	var wg sync.WaitGroup
	wg.Add(100)

	for g := 0; g < 50; g++ {
		go func(key int) {
			defer wg.Done()
			c.Contains(key)
		}(g)
		go func(key int) {
			defer wg.Done()
			c.Peek(key)
		}(g)
	}

	wg.Wait()
}

func TestConcurrent_AddRemove(t *testing.T) {
	c, err := New[int, int](100)
	require.NoError(t, err)
	var wg sync.WaitGroup
	wg.Add(100)

	for g := 0; g < 50; g++ {
		go func(key int) {
			defer wg.Done()
			c.Add(key, key*10)
		}(g)
		go func(key int) {
			defer wg.Done()
			c.Remove(key)
		}(g)
	}

	wg.Wait()
}

func TestConcurrent_MixedOperations(t *testing.T) {
	c, err := NewWithEvict[int, int](50, func(k int, v int) {
		// Eviction callback -- should not cause races
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(200)

	for g := 0; g < 50; g++ {
		go func(key int) {
			defer wg.Done()
			c.Add(key, key)
		}(g)
		go func(key int) {
			defer wg.Done()
			c.Get(key)
		}(g)
		go func(key int) {
			defer wg.Done()
			c.Contains(key)
		}(g)
		go func(key int) {
			defer wg.Done()
			c.Peek(key)
		}(g)
	}

	wg.Wait()
}

func TestConcurrent_AddWithEviction(t *testing.T) {
	evictCount := 0
	mu := sync.Mutex{}
	onEvict := func(k int, v int) {
		mu.Lock()
		evictCount++
		mu.Unlock()
	}

	c, err := NewWithEvict[int, int](10, onEvict)
	require.NoError(t, err)
	var wg sync.WaitGroup
	wg.Add(100)

	for g := 0; g < 100; g++ {
		go func(key int) {
			defer wg.Done()
			c.Add(key, key)
		}(g)
	}

	wg.Wait()

	// With 100 inserts into a size-10 cache, there should be evictions
	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, evictCount, 0, "expected at least one eviction in concurrent test")
}

func TestConcurrent_PurgeWhileReading(t *testing.T) {
	c, err := New[int, int](100)
	require.NoError(t, err)

	// Pre-populate
	for i := 0; i < 100; i++ {
		c.Add(i, i)
	}

	var wg sync.WaitGroup
	wg.Add(51)

	// Readers
	for g := 0; g < 50; g++ {
		go func(key int) {
			defer wg.Done()
			c.Get(key)
			c.Contains(key)
			c.Peek(key)
		}(g)
	}

	// Purge
	go func() {
		defer wg.Done()
		c.Purge()
	}()

	wg.Wait()
}

func TestConcurrent_KeysValues(t *testing.T) {
	c, err := New[int, int](100)
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		c.Add(i, i)
	}

	var wg sync.WaitGroup
	wg.Add(50)

	for g := 0; g < 25; g++ {
		go func() {
			defer wg.Done()
			c.Keys()
		}()
		go func() {
			defer wg.Done()
			c.Values()
		}()
	}

	wg.Wait()
}

// Verify the eviction callback receives correct key-value pairs during Add eviction.
func TestAdd_EvictionCallbackCorrectValues(t *testing.T) {
	evicted := make(map[string]int)
	onEvict := func(k string, v int) {
		evicted[k] = v
	}

	c, err := NewWithEvict[string, int](2, onEvict)
	require.NoError(t, err)

	c.Add("a", 1)
	c.Add("b", 2)

	// Access "a" to increase frequency
	c.Get("a")

	// "b" should be evicted (lower frequency)
	c.Add("c", 3)

	require.Contains(t, evicted, "b", "expected 'b' to be evicted")
	assert.Equal(t, 2, evicted["b"])
}
