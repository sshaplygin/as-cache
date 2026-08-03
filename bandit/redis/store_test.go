package redis

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
)

const testEpoch = time.Second

// runID separates one run's keys from the last one's on a real server, which
// unlike miniredis is not thrown away between runs.
var runID = strconv.FormatInt(time.Now().UnixNano(), 36)

// newStore returns a Store over miniredis, or over a real server when
// AS_CACHE_REDIS_ADDR is set.
//
// The fake covers the logic; the real server covers the assumption the logic
// rests on, which is that the Lua runs at all - TIME inside a script, SET NX
// with PX, and HINCRBY on a hash the script names itself. Those are exactly
// the things a fake can get wrong in the direction of being too permissive,
// so the same tests run against both.
func newStore(t *testing.T) (*Store, goredis.UniversalClient, *miniredis.Miniredis) {
	t.Helper()

	if addr := os.Getenv("AS_CACHE_REDIS_ADDR"); addr != "" {
		client := goredis.NewClient(&goredis.Options{Addr: addr})
		require.NoError(t, client.Ping(t.Context()).Err(), "AS_CACHE_REDIS_ADDR is set but unreachable")

		// A real server is shared and outlives the run, so each test gets its
		// own key prefix rather than flushing someone else's data. The run id
		// is part of it because buckets come from the clock: two runs within
		// the same second would otherwise land in the same bucket and the
		// second would find the first's leadership already claimed and its
		// decisions already published. Everything written carries a TTL, so
		// the extra prefixes expire on their own.
		store, err := New(Options{
			Client:    client,
			KeyPrefix: "asctest:" + runID + ":" + t.Name(),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = store.Close()
			_ = client.Close()
		})

		return store, client, nil
	}

	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store, err := New(Options{Client: client})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	return store, client, server
}

// expire advances past a TTL. A fake is fast-forwarded; a real server has to
// be waited out, which is why the TTLs in these tests are short.
func expire(t *testing.T, server *miniredis.Miniredis, d time.Duration) {
	t.Helper()

	if server != nil {
		server.FastForward(d)

		return
	}
	time.Sleep(d)
}

func syncRequest(namespace, node string, counts ...bandit.ArmCounts) bandit.SyncRequest {
	return bandit.SyncRequest{
		Namespace:   namespace,
		NodeID:      node,
		Counts:      counts,
		EpochMillis: testEpoch.Milliseconds(),
		CounterTTL:  12 * testEpoch,
		LeaderTTL:   2 * testEpoch,
	}
}

func shadow(policy ascache.PolicyType, hits, misses int64) bandit.ArmCounts {
	return bandit.ArmCounts{Policy: policy, Role: bandit.RoleShadow, Hits: hits, Misses: misses}
}

func TestNew_RejectsANilClient(t *testing.T) {
	_, err := New(Options{})
	assert.ErrorIs(t, err, ErrNilClient)
}

func TestStore_SyncReportsAServerDerivedBucket(t *testing.T) {
	store, _, _ := newStore(t)

	result, err := store.Sync(t.Context(), syncRequest("ns", "a"))
	require.NoError(t, err)

	// The bucket is the server's clock divided by the epoch. Checking it lands
	// near the client's clock proves the script read a real time rather than
	// returning a constant.
	expected := bandit.Bucket(time.Now().UnixMilli() / testEpoch.Milliseconds())
	assert.InDelta(t, int64(expected), int64(result.Bucket), 5)
}

func TestStore_SumsCountsAcrossReplicas(t *testing.T) {
	store, _, _ := newStore(t)

	result, err := store.Sync(t.Context(), syncRequest("ns", "a", shadow(ascache.LRU, 10, 5)))
	require.NoError(t, err)
	_, err = store.Sync(t.Context(), syncRequest("ns", "b", shadow(ascache.LRU, 20, 15)))
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "ns", result.Bucket, result.Bucket)
	require.NoError(t, err)
	require.Len(t, window, 1)

	assert.Equal(t,
		ascache.PolicyStats{Hits: 30, Misses: 20},
		window[0].Arms[bandit.ArmKey{Policy: ascache.LRU, Role: bandit.RoleShadow}])
}

func TestStore_KeepsRolesApart(t *testing.T) {
	store, _, _ := newStore(t)

	result, err := store.Sync(t.Context(), syncRequest("ns", "a",
		bandit.ArmCounts{Policy: ascache.LRU, Role: bandit.RoleActive, Hits: 90, Misses: 10},
		bandit.ArmCounts{Policy: ascache.LRU, Role: bandit.RoleShadow, Hits: 40, Misses: 60},
	))
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "ns", result.Bucket, result.Bucket)
	require.NoError(t, err)
	require.Len(t, window, 1)

	assert.Equal(t, ascache.PolicyStats{Hits: 90, Misses: 10},
		window[0].Arms[bandit.ArmKey{Policy: ascache.LRU, Role: bandit.RoleActive}])
	assert.Equal(t, ascache.PolicyStats{Hits: 40, Misses: 60},
		window[0].Arms[bandit.ArmKey{Policy: ascache.LRU, Role: bandit.RoleShadow}])
}

func TestStore_NamespacesDoNotPool(t *testing.T) {
	store, _, _ := newStore(t)

	result, err := store.Sync(t.Context(), syncRequest("one", "a", shadow(ascache.LRU, 10, 0)))
	require.NoError(t, err)
	_, err = store.Sync(t.Context(), syncRequest("two", "b", shadow(ascache.LRU, 999, 0)))
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "one", result.Bucket, result.Bucket)
	require.NoError(t, err)
	require.Len(t, window, 1)

	assert.Equal(t, int64(10),
		window[0].Arms[bandit.ArmKey{Policy: ascache.LRU, Role: bandit.RoleShadow}].Hits)
}

func TestStore_OneLeaderPerBucket(t *testing.T) {
	store, _, _ := newStore(t)

	leaders := 0
	for _, node := range []string{"a", "b", "c", "d", "e"} {
		req := syncRequest("ns", node)
		req.Lead = true
		result, err := store.Sync(t.Context(), req)
		require.NoError(t, err)
		if result.Leader {
			leaders++
		}
	}

	assert.Equal(t, 1, leaders)
}

func TestStore_LeadershipIsNotClaimedUnlessAsked(t *testing.T) {
	store, _, _ := newStore(t)

	result, err := store.Sync(t.Context(), syncRequest("ns", "a"))
	require.NoError(t, err)
	assert.False(t, result.Leader)

	// And a later replica that does ask still gets it.
	req := syncRequest("ns", "b")
	req.Lead = true
	result, err = store.Sync(t.Context(), req)
	require.NoError(t, err)
	assert.True(t, result.Leader)
}

func TestStore_DecisionIsImmutableAndReportsWhatIsInForce(t *testing.T) {
	store, _, _ := newStore(t)

	first, err := store.Decide(t.Context(), "ns", 42, ascache.TinyLFU, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, ascache.TinyLFU, first)

	second, err := store.Decide(t.Context(), "ns", 42, ascache.LRU, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, ascache.TinyLFU, second,
		"a leader that lost the race must follow the fleet, not its own draw")
}

func TestStore_SyncReadsBackAPublishedDecision(t *testing.T) {
	store, _, _ := newStore(t)

	result, err := store.Sync(t.Context(), syncRequest("ns", "a"))
	require.NoError(t, err)
	assert.False(t, result.HasDecision)

	_, err = store.Decide(t.Context(), "ns", result.Bucket, ascache.TwoQueue, time.Minute)
	require.NoError(t, err)

	again, err := store.Sync(t.Context(), syncRequest("ns", "b"))
	require.NoError(t, err)
	require.True(t, again.HasDecision)
	assert.Equal(t, ascache.TwoQueue, again.Decision)
}

func TestStore_WindowOmitsBucketsThatHoldNothing(t *testing.T) {
	store, _, _ := newStore(t)

	result, err := store.Sync(t.Context(), syncRequest("ns", "a", shadow(ascache.LRU, 1, 1)))
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "ns", result.Bucket-8, result.Bucket)
	require.NoError(t, err)

	require.Len(t, window, 1, "a bucket nobody wrote is absent, not present and zero")
	assert.Equal(t, result.Bucket, window[0].Bucket)
}

func TestStore_WindowOfAnInvertedRangeIsEmpty(t *testing.T) {
	store, _, _ := newStore(t)

	window, err := store.Window(t.Context(), "ns", 100, 10)
	require.NoError(t, err)
	assert.Empty(t, window)
}

func TestStore_ZeroCountsCreateNoCounters(t *testing.T) {
	store, _, _ := newStore(t)

	// An arm that measured nothing has produced no evidence. Writing a zero
	// would read back as evidence of a zero hit rate.
	result, err := store.Sync(t.Context(), syncRequest("ns", "a", shadow(ascache.LRU, 0, 0)))
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "ns", result.Bucket, result.Bucket)
	require.NoError(t, err)
	assert.Empty(t, window)
}

func TestStore_CountersExpire(t *testing.T) {
	store, _, server := newStore(t)

	// A fleet that stops running must leave nothing behind in a store it
	// shares with everything else, so this checks both halves of that: the TTL
	// is actually attached, and the data really does go.
	const ttl = 300 * time.Millisecond

	req := syncRequest("ns", "a", shadow(ascache.LRU, 10, 0))
	req.CounterTTL = ttl
	result, err := store.Sync(t.Context(), req)
	require.NoError(t, err)

	window, err := store.Window(t.Context(), "ns", result.Bucket, result.Bucket)
	require.NoError(t, err)
	require.Len(t, window, 1)

	expire(t, server, 2*ttl)

	window, err = store.Window(t.Context(), "ns", result.Bucket, result.Bucket)
	require.NoError(t, err)
	assert.Empty(t, window)
}

func TestStore_EveryKeyItWritesHasATTL(t *testing.T) {
	store, client, _ := newStore(t)

	req := syncRequest("ns", "a", shadow(ascache.LRU, 1, 1))
	req.Lead = true
	result, err := store.Sync(t.Context(), req)
	require.NoError(t, err)

	_, err = store.Decide(t.Context(), "ns", result.Bucket, ascache.LRU, time.Minute)
	require.NoError(t, err)

	keys, err := client.Keys(t.Context(), store.prefix+":*").Result()
	require.NoError(t, err)
	require.NotEmpty(t, keys)

	// A key without an expiry is a key that outlives the fleet that wrote it.
	for _, key := range keys {
		ttl, err := client.PTTL(t.Context(), key).Result()
		require.NoError(t, err)
		assert.Positive(t, ttl, "key %q was written without a TTL", key)
	}
}

func TestStore_EveryKeyForANamespaceSharesOneSlot(t *testing.T) {
	store, client, _ := newStore(t)

	req := syncRequest("ns", "a", shadow(ascache.LRU, 1, 1))
	req.Lead = true
	result, err := store.Sync(t.Context(), req)
	require.NoError(t, err)

	_, err = store.Decide(t.Context(), "ns", result.Bucket, ascache.LRU, time.Minute)
	require.NoError(t, err)

	keys, err := client.Keys(t.Context(), store.prefix+":*").Result()
	require.NoError(t, err)
	require.NotEmpty(t, keys)

	// The hash tag is what keeps a namespace in one Redis Cluster slot, which
	// is what lets the window pipeline and the scripts' computed key names
	// work on a cluster at all. Nothing here runs against a cluster, so this
	// checks the property the cluster behaviour rests on.
	for _, key := range keys {
		assert.Contains(t, key, "{ns}", "key %q is missing the hash tag", key)
	}
}

func TestStore_SurvivesAStrayFieldWrittenBySomethingElse(t *testing.T) {
	store, client, _ := newStore(t)

	result, err := store.Sync(t.Context(), syncRequest("ns", "a", shadow(ascache.LRU, 10, 5)))
	require.NoError(t, err)

	// The store may be shared. A field written by something else must not stop
	// a fleet reading its own counters.
	require.NoError(t, client.HSet(t.Context(),
		store.countsKey("ns", result.Bucket), "not-a-field", "nonsense").Err())

	window, err := store.Window(t.Context(), "ns", result.Bucket, result.Bucket)
	require.NoError(t, err)
	require.Len(t, window, 1)

	assert.Equal(t, ascache.PolicyStats{Hits: 10, Misses: 5},
		window[0].Arms[bandit.ArmKey{Policy: ascache.LRU, Role: bandit.RoleShadow}])
	assert.Len(t, window[0].Arms, 1)
}

func TestStore_ReportsAnUnreachableServer(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	store, err := New(Options{Client: client})
	require.NoError(t, err)

	_, err = store.Sync(t.Context(), syncRequest("ns", "a"))
	require.Error(t, err, "an unreachable store must report, so the bandit can fall back")

	_, err = store.Window(t.Context(), "ns", 0, 4)
	assert.Error(t, err)

	_, err = store.Decide(t.Context(), "ns", 1, ascache.LRU, time.Minute)
	assert.Error(t, err)
}

func TestStore_RespectsContextCancellation(t *testing.T) {
	store, _, _ := newStore(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := store.Sync(ctx, syncRequest("ns", "a"))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestParseCountField_RoundTrips(t *testing.T) {
	policies := []ascache.PolicyType{
		ascache.LRU, ascache.LFU, ascache.TwoQueue,
		ascache.ARC, ascache.Random, ascache.TTL, ascache.TinyLFU,
	}

	for _, policy := range policies {
		for _, role := range []bandit.Role{bandit.RoleActive, bandit.RoleShadow} {
			for _, hits := range []bool{true, false} {
				field := countField(policy, role, hits)

				gotPolicy, gotRole, gotHits, ok := parseCountField(field)
				require.True(t, ok, "field %q did not parse", field)
				assert.Equal(t, policy, gotPolicy)
				assert.Equal(t, role, gotRole)
				assert.Equal(t, hits, gotHits)
			}
		}
	}
}

func TestParseCountField_RejectsJunk(t *testing.T) {
	for _, field := range []string{"", "1", "1:s", "1:s:h:x", "x:s:h", "1:z:h", "1:s:z"} {
		_, _, _, ok := parseCountField(field)
		assert.False(t, ok, "field %q should not have parsed", field)
	}
}
