package bandit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
)

// testClock is a clock a test drives by hand, so bucket boundaries are exact
// and nothing has to sleep.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func newTestClock() *testClock {
	// An arbitrary fixed instant. Buckets are derived from Unix milliseconds,
	// so any starting point works as long as it does not move on its own.
	return &testClock{at: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.at
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.at = c.at.Add(d)
}

const testEpoch = time.Second

func testSyncRequest(namespace, node string, counts ...ArmCounts) SyncRequest {
	return SyncRequest{
		Namespace:   namespace,
		NodeID:      node,
		Counts:      counts,
		EpochMillis: testEpoch.Milliseconds(),
		CounterTTL:  12 * testEpoch,
		LeaderTTL:   2 * testEpoch,
	}
}

func shadow(policy ascache.PolicyType, hits, misses int64) ArmCounts {
	return ArmCounts{Policy: policy, Role: RoleShadow, Hits: hits, Misses: misses}
}

func TestMemStore_BucketComesFromTheStoreClock(t *testing.T) {
	clock := newTestClock()
	store := NewMemStore()
	store.SetClock(clock.now)
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.Sync(t.Context(), testSyncRequest("ns", "a"))
	require.NoError(t, err)

	clock.advance(testEpoch)
	second, err := store.Sync(t.Context(), testSyncRequest("ns", "a"))
	require.NoError(t, err)

	assert.Equal(t, first.Bucket+1, second.Bucket)
}

func TestMemStore_SumsCountsAcrossReplicas(t *testing.T) {
	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	result, err := store.Sync(t.Context(), testSyncRequest("ns", "a", shadow(ascache.LRU, 10, 5)))
	require.NoError(t, err)
	_, err = store.Sync(t.Context(), testSyncRequest("ns", "b", shadow(ascache.LRU, 20, 15)))
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "ns", result.Bucket, result.Bucket)
	require.NoError(t, err)
	require.Len(t, window, 1)

	assert.Equal(t,
		ascache.PolicyStats{Hits: 30, Misses: 20},
		window[0].Arms[ArmKey{Policy: ascache.LRU, Role: RoleShadow}])
}

func TestMemStore_NamespacesDoNotPool(t *testing.T) {
	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	result, err := store.Sync(t.Context(), testSyncRequest("one", "a", shadow(ascache.LRU, 10, 0)))
	require.NoError(t, err)
	_, err = store.Sync(t.Context(), testSyncRequest("two", "b", shadow(ascache.LRU, 999, 0)))
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "one", result.Bucket, result.Bucket)
	require.NoError(t, err)
	require.Len(t, window, 1)

	assert.Equal(t, int64(10), window[0].Arms[ArmKey{Policy: ascache.LRU, Role: RoleShadow}].Hits)
}

func TestMemStore_OneLeaderPerBucket(t *testing.T) {
	clock := newTestClock()
	store := NewMemStore()
	store.SetClock(clock.now)
	t.Cleanup(func() { _ = store.Close() })

	leaders := 0
	for _, node := range []string{"a", "b", "c", "d", "e"} {
		req := testSyncRequest("ns", node)
		req.Lead = true
		result, err := store.Sync(t.Context(), req)
		require.NoError(t, err)
		if result.Leader {
			leaders++
		}
	}
	assert.Equal(t, 1, leaders, "leadership of a bucket is claimed once")

	clock.advance(testEpoch)
	req := testSyncRequest("ns", "f")
	req.Lead = true
	result, err := store.Sync(t.Context(), req)
	require.NoError(t, err)
	assert.True(t, result.Leader, "the next bucket is up for grabs again")
}

func TestMemStore_LeadershipIsNotClaimedUnlessAsked(t *testing.T) {
	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	result, err := store.Sync(t.Context(), testSyncRequest("ns", "a"))
	require.NoError(t, err)
	assert.False(t, result.Leader)
}

func TestMemStore_DecisionIsImmutable(t *testing.T) {
	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.Decide(t.Context(), "ns", 100, ascache.TinyLFU, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, ascache.TinyLFU, first)

	// Republishing is not an error, and reports what is actually in force -
	// which is how a leader that lost a race still ends up agreeing with the
	// fleet instead of running its own draw alone.
	second, err := store.Decide(t.Context(), "ns", 100, ascache.LRU, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, ascache.TinyLFU, second)

	clock := newTestClock()
	store.SetClock(clock.now)

	// Read it back through a sync landing in the same bucket.
	req := testSyncRequest("ns", "a")
	req.EpochMillis = 1
	result, err := store.Sync(t.Context(), req)
	require.NoError(t, err)

	_, err = store.Decide(t.Context(), "ns", result.Bucket, ascache.LFU, time.Minute)
	require.NoError(t, err)

	again, err := store.Sync(t.Context(), req)
	require.NoError(t, err)

	require.True(t, again.HasDecision)
	assert.Equal(t, ascache.LFU, again.Decision)
}

func TestMemStore_ExpiredBucketsLeaveHolesRatherThanZeros(t *testing.T) {
	clock := newTestClock()
	store := NewMemStore()
	store.SetClock(clock.now)
	t.Cleanup(func() { _ = store.Close() })

	req := testSyncRequest("ns", "a", shadow(ascache.LRU, 10, 0))
	req.CounterTTL = 2 * testEpoch
	first, err := store.Sync(t.Context(), req)
	require.NoError(t, err)

	clock.advance(5 * testEpoch)
	latest, err := store.Sync(t.Context(), req)
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "ns", first.Bucket, latest.Bucket)
	require.NoError(t, err)

	require.Len(t, window, 1, "the expired bucket is absent, not present and empty")
	assert.Equal(t, latest.Bucket, window[0].Bucket)
}

func TestMemStore_WindowSkipsBucketsNeverWritten(t *testing.T) {
	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	result, err := store.Sync(t.Context(), testSyncRequest("ns", "a", shadow(ascache.LRU, 1, 1)))
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "ns", result.Bucket-10, result.Bucket)
	require.NoError(t, err)
	assert.Len(t, window, 1)
}

func TestMemStore_WindowCopiesItsCounts(t *testing.T) {
	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	result, err := store.Sync(t.Context(), testSyncRequest("ns", "a", shadow(ascache.LRU, 10, 0)))
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "ns", result.Bucket, result.Bucket)
	require.NoError(t, err)
	require.Len(t, window, 1)

	// A caller mutating what it read must not corrupt the store's own state,
	// or a fleet simulation would poison itself.
	window[0].Arms[ArmKey{Policy: ascache.LRU, Role: RoleShadow}] = ascache.PolicyStats{Hits: 1 << 40}

	again, err := store.Window(t.Context(), "ns", result.Bucket, result.Bucket)
	require.NoError(t, err)
	assert.Equal(t, int64(10), again[0].Arms[ArmKey{Policy: ascache.LRU, Role: RoleShadow}].Hits)
}

func TestMemStore_FailAffectsEveryCall(t *testing.T) {
	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	boom := errors.New("store is down")
	store.Fail(boom)

	_, err := store.Sync(t.Context(), testSyncRequest("ns", "a"))
	assert.ErrorIs(t, err, boom)

	_, err = store.Window(t.Context(), "ns", 0, 10)
	assert.ErrorIs(t, err, boom)

	_, err = store.Decide(t.Context(), "ns", 1, ascache.LRU, time.Minute)
	assert.ErrorIs(t, err, boom)

	store.Fail(nil)
	_, err = store.Sync(t.Context(), testSyncRequest("ns", "a"))
	assert.NoError(t, err)
}

func TestMemStore_RespectsContextCancellation(t *testing.T) {
	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := store.Sync(ctx, testSyncRequest("ns", "a"))
	assert.ErrorIs(t, err, context.Canceled)

	_, err = store.Window(ctx, "ns", 0, 1)
	assert.ErrorIs(t, err, context.Canceled)

	_, err = store.Decide(ctx, "ns", 1, ascache.LRU, time.Minute)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMemStore_ConcurrentReplicas(t *testing.T) {
	store := NewMemStore()
	t.Cleanup(func() { _ = store.Close() })

	const replicas = 16
	const rounds = 50

	var wg sync.WaitGroup
	for node := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				req := testSyncRequest("ns", string(rune('a'+node)), shadow(ascache.LRU, 1, 1))
				req.Lead = true
				if _, err := store.Sync(t.Context(), req); err != nil {
					assert.NoError(t, err)
					return
				}
				if _, err := store.Window(t.Context(), "ns", 0, 32); err != nil {
					assert.NoError(t, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
