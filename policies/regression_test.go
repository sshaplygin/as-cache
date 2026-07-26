package policies_test

import (
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/policies"
)

// TestAddKeepsTheKeyItJustStored is the property every cache owes its caller:
// a successful Add leaves the key retrievable. RandomCache used to draw its
// eviction victim from a pool that already included the incoming key, so a
// write into a full cache was discarded with probability 1/(size+1) - accepted
// and then lost before the very next read.
func TestAddKeepsTheKeyItJustStored(t *testing.T) {
	for name, build := range policiesUnderTest {
		t.Run(name, func(t *testing.T) {
			const size = 4
			const trials = 4000

			lost := 0
			for i := 0; i < trials; i++ {
				p := build(t, size)
				for j := 0; j < size; j++ {
					p.Add("fill-"+strconv.Itoa(j), j)
				}

				p.Add("fresh", 42)
				if got, ok := p.Peek("fresh"); !ok || got != 42 {
					lost++
				}
			}

			assert.Zero(t, lost,
				"a successful Add must leave the key present: lost %d of %d writes into a full cache", lost, trials)
		})
	}
}

// TestRandomPurgeReleasesKeys guards a Purge that truncated the key slice
// instead of replacing it, leaving every key reachable through the retained
// backing array and pinning memory the caller purged specifically to free.
func TestRandomPurgeReleasesKeys(t *testing.T) {
	const size = 20000

	cache := policies.NewRandom[string, int](size)
	for i := 0; i < size; i++ {
		cache.Add(strconv.Itoa(i)+"-------------------------------------------------", i)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	cache.Purge()

	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)

	require.Zero(t, cache.Len(), "Purge must empty the cache")

	// The keys were the bulk of what was allocated, so Purge must release most
	// of it. A truncating Purge released essentially nothing.
	assert.Less(t, after.HeapAlloc, before.HeapAlloc,
		"Purge must release the key payload, not pin it in a retained backing array")
}

// TestTTLExpiryNeverServesAZeroValue guards the invariant this library is
// built around. hashicorp's expirable LRU returns (zeroValue, true) from Get
// and Peek for an entry that has expired but not been reaped, which would hand
// a caller a zero value and call it a hit.
func TestTTLExpiryNeverServesAZeroValue(t *testing.T) {
	const ttl = 30 * time.Millisecond
	p := policies.NewTTL[string, int](100, ttl)

	for i := 1; i <= 20; i++ {
		p.Add("key-"+strconv.Itoa(i), i)
	}

	// Wait past the deadline but keep reading throughout, so the check covers
	// the window in which an entry is expired but still resident.
	deadline := time.Now().Add(4 * ttl)
	for time.Now().Before(deadline) {
		for i := 1; i <= 20; i++ {
			key := "key-" + strconv.Itoa(i)

			if got, ok := p.Get(key); ok {
				require.Equal(t, i, got, "Get served a value that was never stored for %q", key)
			}
			if got, ok := p.Peek(key); ok {
				require.Equal(t, i, got, "Peek served a value that was never stored for %q", key)
			}
		}
	}

	assert.Zero(t, p.Len(), "every entry should have expired by now")
}

// TestTTLValuesAlignWithKeys guards a Values that returned a full-length slice
// padded with zeros once entries expired, so Values no longer corresponded to
// Keys.
func TestTTLValuesAlignWithKeys(t *testing.T) {
	const ttl = 30 * time.Millisecond
	p := policies.NewTTL[string, int](100, ttl)

	for i := 1; i <= 10; i++ {
		p.Add("fresh-"+strconv.Itoa(i), i)
	}
	time.Sleep(2 * ttl)
	for i := 1; i <= 5; i++ {
		p.Add("live-"+strconv.Itoa(i), 100+i)
	}

	keys, values := p.Keys(), p.Values()

	require.Len(t, values, len(keys), "Values must correspond one-to-one with Keys")
	for _, v := range values {
		assert.NotZero(t, v, "Values must not report a zero value for an expired entry")
	}
	assert.Len(t, keys, 5, "only the unexpired entries should be listed")
}

// TestTTLDoesNotLeakGoroutines guards against the reaper goroutine that
// hashicorp's expirable LRU starts per cache and offers no way to stop, which
// leaked a goroutine and the whole cache for every policy ever constructed.
func TestTTLDoesNotLeakGoroutines(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		p := policies.NewTTL[string, int](100, time.Millisecond)
		p.Add("a", 1)
	}

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()

	assert.LessOrEqual(t, after, before+2,
		"constructing 50 TTL policies must not leave goroutines behind (before %d, after %d)", before, after)
}

// TestAdaptedResizeSurvivorsAreIntact pins down what a shrinking Resize does
// guarantee. It cannot guarantee WHICH entries survive: Keys() means different
// things per implementation - 2Q returns frequent-then-recent, ARC returns
// recent-then-frequent - so no selection rule is right for both, and the
// rebuilt cache picks its own victims. What must hold is that the cache ends
// up within capacity and every survivor carries the value it was stored with,
// never a zero or another key's value.
func TestAdaptedResizeSurvivorsAreIntact(t *testing.T) {
	p, err := policies.NewTwoQueue[string, int](200)
	require.NoError(t, err)

	// Promote a small set into 2Q's frequent queue by touching it repeatedly.
	hot := make([]string, 10)
	for i := range hot {
		hot[i] = "hot-" + strconv.Itoa(i)
		p.Add(hot[i], i)
	}
	for round := 0; round < 5; round++ {
		for _, key := range hot {
			p.Get(key)
		}
	}

	// Fill the rest with one-off keys that stay in the recent queue.
	for i := 0; i < 150; i++ {
		p.Add("cold-"+strconv.Itoa(i), i)
	}

	p.Resize(20)

	assert.LessOrEqual(t, p.Len(), 20, "Resize must enforce the new capacity")
	assert.Equal(t, 20, p.Cap())

	for i, key := range hot {
		if got, ok := p.Peek(key); ok {
			assert.Equal(t, i, got, "%s survived the resize with a corrupted value", key)
		}
	}
	for i := 0; i < 150; i++ {
		key := "cold-" + strconv.Itoa(i)
		if got, ok := p.Peek(key); ok {
			assert.Equal(t, i, got, "%s survived the resize with a corrupted value", key)
		}
	}

	assert.Positive(t, p.Len(), "a shrink to 20 should retain entries, not empty the cache")
}

// TestAdaptedResizeEnforcesCapacityWhenRebuildFails guards a Resize that
// swallowed a build error, leaving the old capacity in force while Cap()
// reported the new one. hashicorp's 2Q rejects a size of 1, so this is
// reachable rather than hypothetical.
func TestAdaptedResizeEnforcesCapacityWhenRebuildFails(t *testing.T) {
	p, err := policies.NewTwoQueue[string, int](50)
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		p.Add("key-"+strconv.Itoa(i), i)
	}

	// A 2Q of size 1 cannot be built: its ghost queues round down to zero.
	p.Resize(1)

	assert.Equal(t, 1, p.Cap(), "Cap must report the requested capacity")
	assert.LessOrEqual(t, p.Len(), 1,
		"the requested capacity must actually be enforced, not just reported")

	// And it must keep honouring it.
	for i := 0; i < 20; i++ {
		p.Add("more-"+strconv.Itoa(i), i)
	}
	assert.LessOrEqual(t, p.Len(), 1, "capacity must stay enforced after further Adds")
}

// TestPolicyTypeNamesRoundTrip checks the regenerated stringer output covers
// every policy this repository ships.
func TestPolicyTypeNamesRoundTrip(t *testing.T) {
	for policyType, want := range map[ascache.PolicyType]string{
		ascache.LRU:      "LRU",
		ascache.LFU:      "LFU",
		ascache.TwoQueue: "TwoQueue",
		ascache.ARC:      "ARC",
		ascache.Random:   "Random",
		ascache.TTL:      "TTL",
		ascache.TinyLFU:  "TinyLFU",
	} {
		assert.Equal(t, want, policyType.String())
	}
}
