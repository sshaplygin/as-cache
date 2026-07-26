package metrics_test

import (
	"encoding/json"
	"expvar"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/metrics"
	"github.com/sshaplygin/as-cache/policies"
)

// newCache builds an observe-only cache with two arms, which is the
// configuration an operator would run first.
func newCache(t *testing.T) *ascache.AdaptiveCache[string, int] {
	t.Helper()

	lru, err := policies.NewLRU[string, int](1000)
	require.NoError(t, err)
	twoQ, err := policies.NewTwoQueue[string, int](1000)
	require.NoError(t, err)

	cache, err := ascache.NewAdaptiveCache[string, int](
		[]ascache.Policy[string, int]{lru, twoQ}, nil,
		&ascache.Settings{EpochDuration: 5 * time.Millisecond, ObserveOnly: true},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	return cache
}

// drive puts enough traffic through the cache that at least one epoch has been
// measured, so a snapshot has something in it.
func drive(t *testing.T, cache *ascache.AdaptiveCache[string, int]) {
	t.Helper()

	deadline := time.Now().Add(200 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		key := "key-" + strconv.Itoa(i%500)
		if _, ok := cache.Get(key); !ok {
			cache.Add(key, i)
		}
		if cache.Advice().Epochs > 2 {
			return
		}
	}

	require.Positive(t, cache.Advice().Epochs, "expected at least one epoch to elapse")
}

func TestTake_ReportsTheCacheState(t *testing.T) {
	cache := newCache(t)
	drive(t, cache)

	snapshot := metrics.Take(cache)

	assert.Equal(t, "LRU", snapshot.ActivePolicy, "observe-only keeps the first policy active")
	assert.Positive(t, snapshot.Epochs)
	assert.Positive(t, snapshot.Entries)
	assert.Positive(t, snapshot.Hits+snapshot.Misses, "the cache served traffic")
	assert.InDelta(t, float64(snapshot.Hits)/float64(snapshot.Hits+snapshot.Misses),
		snapshot.HitRate, 1e-9)
	assert.Len(t, snapshot.Policies, 2, "every arm should be reported")
}

func TestTake_UnsampledTotalsAreRealTraffic(t *testing.T) {
	cache := newCache(t)
	drive(t, cache)

	snapshot := metrics.Take(cache)
	stats := cache.Stats()

	assert.Equal(t, stats.Hits, snapshot.Hits,
		"the headline counters must be the traffic actually served, not a sample")
	assert.Equal(t, stats.Misses, snapshot.Misses)
}

func TestTake_PoliciesAreOrderedBestFirst(t *testing.T) {
	cache := newCache(t)
	drive(t, cache)

	snapshot := metrics.Take(cache)
	require.NotEmpty(t, snapshot.Policies)

	for i := 1; i < len(snapshot.Policies); i++ {
		assert.GreaterOrEqual(t, snapshot.Policies[i-1].HitRate, snapshot.Policies[i].HitRate,
			"policies should be ordered best hit rate first")
	}

	assert.Equal(t, snapshot.Policies[0].Policy, snapshot.BestPolicy)
}

func TestSnapshot_IsValidJSON(t *testing.T) {
	cache := newCache(t)
	drive(t, cache)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(metrics.Take(cache).String()), &decoded))

	for _, field := range []string{
		"active_policy", "epochs", "entries", "hits", "misses",
		"hit_rate", "best_policy", "improvement", "policies",
	} {
		assert.Contains(t, decoded, field)
	}
}

func TestPublish_ExposesThroughExpvar(t *testing.T) {
	cache := newCache(t)
	drive(t, cache)

	name := "as_cache_test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	require.NoError(t, metrics.Publish(name, cache))

	published := expvar.Get(name)
	require.NotNil(t, published, "the snapshot should appear in expvar")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(published.String()), &decoded))
	assert.Equal(t, "LRU", decoded["active_policy"])
}

// TestPublish_RejectsDuplicateNames guards against expvar's own behaviour:
// publishing the same name twice panics, which would take a process down over
// a metrics registration mistake.
func TestPublish_RejectsDuplicateNames(t *testing.T) {
	cache := newCache(t)

	name := "as_cache_dup_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	require.NoError(t, metrics.Publish(name, cache))

	err := metrics.Publish(name, cache)
	require.Error(t, err, "a duplicate publish must be an error, not a panic")
	assert.Contains(t, err.Error(), "already published")
}

// TestPublish_IsEvaluatedLazily checks that the published value reflects the
// cache when scraped rather than when registered.
func TestPublish_IsEvaluatedLazily(t *testing.T) {
	cache := newCache(t)

	name := "as_cache_lazy_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	require.NoError(t, metrics.Publish(name, cache))

	before := expvar.Get(name).String()

	drive(t, cache)

	after := expvar.Get(name).String()

	assert.NotEqual(t, before, after,
		"the published value must be computed on scrape, so a dashboard sees live data")
}
