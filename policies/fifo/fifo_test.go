package fifo_test

import (
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/policies"
	"github.com/sshaplygin/as-cache/policies/fifo"
)

// algorithms is every policy this module provides. Both run the whole suite:
// they share an adapter, so a defect in it shows up in either, but they are
// different algorithms underneath and upstream handles them differently - a
// SIEVE cache panics if built at size zero where an S3-FIFO one hangs, and
// only one of them locks in Len.
var algorithms = map[string]func(size int) (ascache.Policy[string, int], error){
	"s3fifo": fifo.NewS3FIFOPolicy[string, int],
	"sieve":  fifo.NewSievePolicy[string, int],
}

// build constructs the policy under test. Every test below is run once per
// algorithm through forEachAlgorithm.
type build func(t *testing.T, size int) ascache.Policy[string, int]

// forEachAlgorithm runs a test body against each algorithm in turn.
func forEachAlgorithm(t *testing.T, body func(t *testing.T, newPolicy build)) {
	t.Helper()

	for name, ctor := range algorithms {
		t.Run(name, func(t *testing.T) {
			body(t, func(t *testing.T, size int) ascache.Policy[string, int] {
				t.Helper()

				p, err := ctor(size)
				require.NoError(t, err)

				return p
			})
		})
	}
}

// TestS3FIFOConformance mirrors the conformance suite the policies in the
// parent module run, since this adapter lives in its own module and cannot
// share that file.
func TestConformance(t *testing.T) {
	forEachAlgorithm(t, conformance)
}

func conformance(t *testing.T, newS3FIFO build) {
	t.Helper()

	t.Run("stores and retrieves", func(t *testing.T) {
		p := newS3FIFO(t, 10)
		p.Add("a", 1)

		got, ok := p.Get("a")
		require.True(t, ok, "a stored key must be retrievable")
		assert.Equal(t, 1, got)
	})

	t.Run("reports a miss for an absent key", func(t *testing.T) {
		p := newS3FIFO(t, 10)

		got, ok := p.Get("nope")
		assert.False(t, ok)
		assert.Zero(t, got, "a miss must return the zero value")
	})

	t.Run("overwrites without growing", func(t *testing.T) {
		p := newS3FIFO(t, 10)
		p.Add("a", 1)
		p.Add("a", 2)

		got, ok := p.Get("a")
		require.True(t, ok)
		assert.Equal(t, 2, got, "re-adding a key must overwrite it")
		assert.Equal(t, 1, p.Len(), "re-adding a key must not add an entry")
	})

	t.Run("never exceeds capacity", func(t *testing.T) {
		const size = 10
		p := newS3FIFO(t, size)

		for i := 0; i < size*5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		assert.LessOrEqual(t, p.Len(), size, "a policy must not exceed its capacity")
		assert.Equal(t, size, p.Cap(), "Cap must report the configured capacity")
	})

	t.Run("Peek does not report a miss for a live key", func(t *testing.T) {
		p := newS3FIFO(t, 10)
		p.Add("a", 42)

		got, ok := p.Peek("a")
		require.True(t, ok)
		assert.Equal(t, 42, got)
	})

	t.Run("Contains agrees with Get", func(t *testing.T) {
		p := newS3FIFO(t, 10)
		p.Add("a", 1)

		assert.True(t, p.Contains("a"))
		assert.False(t, p.Contains("b"))
	})

	t.Run("Remove reports presence", func(t *testing.T) {
		p := newS3FIFO(t, 10)
		p.Add("a", 1)

		assert.True(t, p.Remove("a"), "removing a present key must report true")
		assert.False(t, p.Contains("a"))
		assert.False(t, p.Remove("a"), "removing an absent key must report false")
	})

	t.Run("Purge empties", func(t *testing.T) {
		p := newS3FIFO(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Purge()
		assert.Zero(t, p.Len())
		assert.Empty(t, p.Keys())
	})

	t.Run("Keys and Values agree with Len", func(t *testing.T) {
		p := newS3FIFO(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		assert.Len(t, p.Keys(), p.Len())
		assert.Len(t, p.Values(), p.Len())
	})

	t.Run("Resize shrinks to the new capacity", func(t *testing.T) {
		p := newS3FIFO(t, 20)
		for i := 0; i < 20; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}
		require.Positive(t, p.Len(), "expected entries before resizing")

		p.Resize(5)

		assert.LessOrEqual(t, p.Len(), 5, "Resize must enforce the new capacity")
		assert.Equal(t, 5, p.Cap(), "Cap must follow Resize - the adaptive layer relies on it")
	})

	t.Run("Resize grows without losing data", func(t *testing.T) {
		p := newS3FIFO(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}
		before := p.Len()

		p.Resize(100)

		assert.Equal(t, before, p.Len(), "growing must not evict")
		assert.Equal(t, 100, p.Cap())
	})

	t.Run("Resize to zero empties", func(t *testing.T) {
		p := newS3FIFO(t, 10)
		for i := 0; i < 5; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Resize(0)

		assert.Zero(t, p.Len(), "a zero-capacity policy must hold nothing")

		// Adding to a zero-capacity policy must not panic, hang or retain
		// anything. Upstream cannot be built at size zero at all - its Set
		// loops forever waiting to evict from an empty cache - so the adapter
		// holds no cache in this state and has to answer every method itself.
		p.Add("x", 1)
		assert.Zero(t, p.Len())
		assert.False(t, p.Contains("x"))
		_, ok := p.Get("x")
		assert.False(t, ok)
		assert.Empty(t, p.Keys())
		assert.Empty(t, p.Values())
	})

	t.Run("tracks hits and misses", func(t *testing.T) {
		p := newS3FIFO(t, 10)
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
		p := newS3FIFO(t, 100)

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

	t.Run("reports a defined type", func(t *testing.T) {
		p := newS3FIFO(t, 10)

		assert.NotEqual(t, ascache.Undefined, p.GetType())
	})
}

// TestIndexStaysInStepWithTheLibrary is the test this adapter exists to earn.
//
// golang-fifo cannot enumerate its own contents, so the adapter keeps a second
// copy of the key set and maintains it through the library's eviction
// callback. Every path that can remove an entry - an eviction during a write,
// an explicit Remove, a Purge, a rebuild during Resize - has to be reflected
// there, and a single missed path is invisible until Keys hands AdaptiveCache
// a key the cache no longer holds and warm migration copies a zero value onto
// it.
func TestIndexStaysInStepWithTheLibrary(t *testing.T) {
	forEachAlgorithm(t, indexStaysInStep)
}

func indexStaysInStep(t *testing.T, newS3FIFO build) {
	t.Helper()

	const size = 64

	p := newS3FIFO(t, size)
	rng := rand.New(rand.NewSource(7))

	for i := 0; i < 50_000; i++ {
		key := "k" + strconv.Itoa(rng.Intn(size*4))

		switch rng.Intn(16) {
		case 0:
			p.Remove(key)
		case 1:
			// Rare but reachable. Gating this on a second condition as well
			// left it firing zero times in fifty thousand iterations, so the
			// path was never covered at all.
			if rng.Intn(64) == 0 {
				p.Purge()
			}
		case 2:
			// Resize churn is what a policy changing role sees.
			p.Resize(size / 2)
			p.Resize(size)
		default:
			if _, ok := p.Get(key); !ok {
				p.Add(key, i)
			}
		}

		if i%1000 != 0 {
			continue
		}

		keys := p.Keys()
		require.LessOrEqual(t, len(keys), size, "step %d: the index outgrew the capacity", i)
		require.Equal(t, len(keys), p.Len(), "step %d: Len and Keys disagree", i)

		for _, k := range keys {
			require.True(t, p.Contains(k),
				"step %d: Keys reported %q, which the underlying cache does not hold", i, k)
		}

		require.Len(t, p.Values(), len(keys),
			"step %d: Values must line up with Keys", i)
	}
}

// TestAddReportsEvictionsExactly covers the one thing this adapter does better
// than the 2Q, ARC and W-TinyLFU ones: the library reports each eviction
// through a callback, so the evicted flag is counted rather than inferred from
// the length.
func TestAddReportsEvictionsExactly(t *testing.T) {
	forEachAlgorithm(t, addReportsEvictions)
}

func addReportsEvictions(t *testing.T, newS3FIFO build) {
	t.Helper()

	const size = 8

	p := newS3FIFO(t, size)

	for i := 0; i < size; i++ {
		assert.False(t, p.Add("fill-"+strconv.Itoa(i), i),
			"filling an empty cache evicts nothing")
	}

	evicted := 0
	for i := 0; i < size*4; i++ {
		if p.Add("more-"+strconv.Itoa(i), i) {
			evicted++
		}
	}

	assert.Positive(t, evicted, "writing into a full cache must report evictions")
	assert.LessOrEqual(t, p.Len(), size)
}

// TestResizeDiscardsTheAlgorithmsState records the cost this adapter pays for
// upstream having no Resize, so that it is a measured property rather than a
// claim in a comment.
//
// A key evicted from the small queue is remembered by the ghost queue, and
// coming back while it is remembered admits it straight to the main queue. A
// rebuild starts with an empty ghost queue, so that second chance is gone.
func TestResizeDiscardsTheAlgorithmsState(t *testing.T) {
	forEachAlgorithm(t, resizeDiscardsState)
}

func resizeDiscardsState(t *testing.T, newS3FIFO build) {
	t.Helper()

	const size = 16

	// survivesPressure reports whether a key made deliberately hot is still
	// resident after the cache is pushed a full capacity past its limit.
	survivesPressure := func(resize bool) bool {
		p := newS3FIFO(t, size)
		for i := range size {
			p.Add("k"+strconv.Itoa(i), value(i))
		}

		// Six reads is more than enough to saturate S3-FIFO's counter, which
		// caps at three, and to set SIEVE's visited bit.
		for range 6 {
			p.Get("k0")
		}

		if resize {
			// What a promotion or demotion does to a shadow policy.
			p.Resize(size * 2)
			p.Resize(size)
		}

		for i := range size {
			p.Add("fresh"+strconv.Itoa(i), value(i))
		}

		_, resident := p.Peek("k0")

		return resident
	}

	assert.True(t, survivesPressure(false),
		"a key read six times must outlive keys read none; if it does not, the "+
			"eviction state this test is about is not being built in the first place")

	// The claim this test exists for. Upstream cannot resize, so the adapter
	// rebuilds and replays every entry, and the replay carries values only -
	// S3-FIFO's frequency counters and ghost queue and SIEVE's visited bits and
	// hand position do not survive it. The hot key is therefore indistinguishable
	// from the cold ones afterwards and is evicted with them.
	//
	// This costs a policy that changes role often, since AdaptiveCache resizes
	// on every promotion and demotion: such an arm is permanently re-learning
	// and under-reports itself. Asserting merely that a resize does not panic
	// would pass just as happily if the state were preserved, which is the
	// thing being denied.
	assert.False(t, survivesPressure(true),
		"after a rebuild-to-resize the hot key must be as evictable as any other; "+
			"if it survived, the adapter kept state the rebuild is documented to discard")
}

// TestReplayIsDeterministic is a requirement of this arm being in
// benchclient.DefaultArms, not a nicety. That set exists so a replay produces
// the same number twice, and one unstable arm is enough to move which policy
// the bandit selects and therefore the whole replay.
//
// It is asserted rather than assumed because determinism is a property of the
// whole stack: the library's queues, the adapter's index, and the order the
// eviction callback fires in all have to be reproducible.
func TestReplayIsDeterministic(t *testing.T) {
	forEachAlgorithm(t, replayIsDeterministic)
}

func replayIsDeterministic(t *testing.T, newS3FIFO build) {
	t.Helper()

	const size = 128

	replay := func() (int, []string) {
		p := newS3FIFO(t, size)
		rng := rand.New(rand.NewSource(99))

		hits := 0
		for i := 0; i < 20_000; i++ {
			key := "k" + strconv.Itoa(rng.Intn(size*3))
			if _, ok := p.Get(key); ok {
				hits++

				continue
			}
			p.Add(key, i)
		}

		return hits, p.Keys()
	}

	firstHits, firstKeys := replay()
	secondHits, secondKeys := replay()

	assert.Equal(t, firstHits, secondHits, "two replays of one sequence must serve the same hits")
	assert.Equal(t, firstKeys, secondKeys,
		"and must leave the cache holding the same keys in the same order")
}

// TestPolicyTypesAreDistinct guards the wiring: two arms reporting the same
// PolicyType collide in AdaptiveCache's policy map and the constructor rejects
// the pair, so a copy-paste in NewSievePolicy would make the two unusable
// together rather than merely mislabelled.
func TestPolicyTypesAreDistinct(t *testing.T) {
	s3, err := fifo.NewS3FIFOPolicy[string, int](10)
	require.NoError(t, err)
	sv, err := fifo.NewSievePolicy[string, int](10)
	require.NoError(t, err)

	assert.Equal(t, ascache.S3FIFO, s3.GetType())
	assert.Equal(t, ascache.SIEVE, sv.GetType())
	assert.NotEqual(t, s3.GetType(), sv.GetType())
	assert.Equal(t, "SIEVE", ascache.SIEVE.String(),
		"the stringer output must be regenerated when a PolicyType is added")
}

// TestSieveKeepsTheHotSetThroughASweep is the property SIEVE is carried for,
// checked against LRU on the access pattern that separates them: a hot set
// small enough to fit, swept over by keys nobody requests twice.
//
// SIEVE keeps the hot set because its hand clears visited bits as it passes
// and evicts the entries that never set one; LRU loses the hot set on every
// sweep because the sweep is more recent.
func TestSieveKeepsTheHotSetThroughASweep(t *testing.T) {
	const (
		size   = 200
		hot    = 100
		sweep  = 500
		rounds = 20
	)

	keys := make([]string, 0, rounds*(hot*2+sweep))
	for round := 0; round < rounds; round++ {
		for pass := 0; pass < 2; pass++ {
			for i := 0; i < hot; i++ {
				keys = append(keys, "hot"+strconv.Itoa(i))
			}
		}
		for i := 0; i < sweep; i++ {
			keys = append(keys, "scan"+strconv.Itoa(round*sweep+i))
		}
	}

	readThrough := func(p ascache.Policy[string, int]) int {
		hits := 0
		for i, key := range keys {
			if _, ok := p.Get(key); ok {
				hits++

				continue
			}
			p.Add(key, i)
		}

		return hits
	}

	sv, err := fifo.NewSievePolicy[string, int](size)
	require.NoError(t, err)
	sieveHits := readThrough(sv)

	lru, err := policies.NewLRU[string, int](size)
	require.NoError(t, err)
	lruHits := readThrough(lru)

	t.Logf("over %d requests: SIEVE %d hits (%.1f%%), LRU %d hits (%.1f%%)",
		len(keys), sieveHits, float64(sieveHits)/float64(len(keys))*100,
		lruHits, float64(lruHits)/float64(len(keys))*100)

	assert.Greater(t, sieveHits, lruHits,
		"SIEVE must beat LRU on a sweep of keys with no reuse; if it does not, the hand is not filtering them")
}

// alternatingBandit hands out each arm in turn, so a replay drives every part
// of a switch rather than waiting for a real bandit to choose to make one:
// promotion back to full capacity, migration, and demotion onto shadow duty.
type alternatingBandit struct {
	mu    sync.Mutex
	picks []ascache.PolicyType
	next  int
}

func (b *alternatingBandit) RecordStats(_ ascache.ShadowStats) {}

func (b *alternatingBandit) SelectPolicy() ascache.PolicyType {
	b.mu.Lock()
	defer b.mu.Unlock()

	pick := b.picks[b.next%len(b.picks)]
	b.next++

	return pick
}

// value is what the cache stores for a key. It is never zero, so a zero read
// back is unambiguously a shadow's placeholder leaking to a caller rather than
// a value somebody stored.
func value(i int) int { return i + 1 }

// TestArmsDriveAnAdaptiveCacheThroughSwitches is the integration this module
// owes the adaptive layer, and it is the test that matters most for these two
// arms specifically.
//
// The conformance suite drives the adapter directly. AdaptiveCache drives it
// differently: it demotes a policy by walking Keys() and rewriting every
// sampled key to the zero value, removing the unsampled ones, then resizing to
// a miniature; it promotes by resizing back to full capacity; and warm
// migration copies entries out through Keys() and Peek(). All of those lean on
// the key index this adapter maintains for itself and on the rebuild it does
// to resize, so a defect in either shows up here and nowhere else.
//
// The invariant being defended is the one this whole repository rests on: a
// caller must never read a shadow policy's zero value as though it were data.
func TestArmsDriveAnAdaptiveCacheThroughSwitches(t *testing.T) {
	strategies := map[string]ascache.MigrationStrategy{
		"cold":    ascache.MigrationCold,
		"warm":    ascache.MigrationWarm,
		"gradual": ascache.MigrationGradual,
		"unset":   0,
	}

	for name, strategy := range strategies {
		t.Run(name, func(t *testing.T) {
			const (
				size   = 500
				rounds = 40
			)

			s3, err := fifo.NewS3FIFOPolicy[string, int](size)
			require.NoError(t, err)
			sv, err := fifo.NewSievePolicy[string, int](size)
			require.NoError(t, err)

			cache, err := ascache.NewAdaptiveCache(
				[]ascache.Policy[string, int]{s3, sv},
				&alternatingBandit{picks: []ascache.PolicyType{ascache.S3FIFO, ascache.SIEVE}},
				&ascache.Settings{
					// Request-counted rather than wall-clock epochs, so the
					// number of switches a run performs is the same on any
					// machine - and, in particular, the same under -race,
					// which slows the epoch goroutine enough that a
					// time-driven version of this test switches too few times
					// to reach both arms.
					EpochRequests:     size / 2,
					MigrationStrategy: strategy,
					// Sampled, so demotion takes both of its paths: rewriting
					// the sampled keys and removing the unsampled ones.
					ShadowSampleRate:  0.25,
					MinShadowCapacity: 64,
				},
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = cache.Close() })

			seen := map[ascache.PolicyType]bool{}
			for round := 0; round < rounds; round++ {
				for i := 0; i < size; i++ {
					key := "key-" + strconv.Itoa(i)
					got, ok := cache.Get(key)
					if !ok {
						cache.Add(key, value(i))

						continue
					}

					require.Equal(t, value(i), got,
						"round %d: key %q returned %d, which nobody ever stored for it",
						round, key, got)

					seen[cache.ActivePolicy()] = true
				}
			}

			assert.True(t, seen[ascache.S3FIFO], "S3-FIFO must have served traffic as the active policy")
			assert.True(t, seen[ascache.SIEVE], "SIEVE must have served traffic as the active policy")
			assert.LessOrEqual(t, cache.Len(), size, "the active policy must not exceed the cache's capacity")
		})
	}
}

// TestArmsSurviveRoleChangesUnderConcurrency runs the same switching against
// concurrent readers and writers, which is where the eviction callback and the
// adapter's index are most likely to disagree: the callback runs inside the
// library with its own lock held, on a goroutine that entered through this
// adapter's lock.
func TestArmsSurviveRoleChangesUnderConcurrency(t *testing.T) {
	const size = 400

	s3, err := fifo.NewS3FIFOPolicy[string, int](size)
	require.NoError(t, err)
	sv, err := fifo.NewSievePolicy[string, int](size)
	require.NoError(t, err)

	cache, err := ascache.NewAdaptiveCache(
		[]ascache.Policy[string, int]{s3, sv},
		&alternatingBandit{picks: []ascache.PolicyType{ascache.S3FIFO, ascache.SIEVE}},
		&ascache.Settings{
			// Request-counted for the same reason as above: it is what makes
			// the switch actually happen under -race.
			EpochRequests:     200,
			MigrationStrategy: ascache.MigrationWarm,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	// Failures are collected rather than asserted in place: require.* calls
	// FailNow, which is only legal on the goroutine running the test, and from
	// a spawned one it kills that goroutine instead - leaving the WaitGroup
	// waiting forever or the failure misreported.
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		faults []string
	)

	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()

			for i := 0; i < 3000; i++ {
				id := (seed*7 + i) % (size * 2)
				key := "key-" + strconv.Itoa(id)

				got, ok := cache.Get(key)
				if !ok {
					cache.Add(key, value(id))

					continue
				}
				if got != value(id) {
					mu.Lock()
					faults = append(faults,
						fmt.Sprintf("Get(%q) returned %d, but only %d was ever stored for it",
							key, got, value(id)))
					mu.Unlock()

					return
				}
			}
		}(g)
	}
	wg.Wait()

	assert.Empty(t, faults, "a caller read a value nobody stored")
	assert.LessOrEqual(t, cache.Len(), size)
}

// TestConstructorsRejectANonPositiveSize pins the behaviour every sibling
// adapter already has. A cache built at zero accepts nothing and reports no
// hits for as long as it exists, so as a bandit arm it is a silent no-op that
// drags the whole comparison down without ever looking broken. The constructor
// is the last place that can be caught.
func TestConstructorsRejectANonPositiveSize(t *testing.T) {
	for _, size := range []int{0, -1, -100} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			s3, err := fifo.NewS3FIFO[string, int](size)
			require.Error(t, err, "NewS3FIFO(%d) must fail", size)
			assert.Contains(t, err.Error(), "positive size")
			assert.Nil(t, s3, "a failed constructor must not hand back a cache to dereference")

			sv, err := fifo.NewSieve[string, int](size)
			require.Error(t, err, "NewSieve(%d) must fail", size)
			assert.Contains(t, err.Error(), "positive size")
			assert.Nil(t, sv)

			s3Policy, err := fifo.NewS3FIFOPolicy[string, int](size)
			require.Error(t, err, "NewS3FIFOPolicy(%d) must fail", size)
			assert.Contains(t, err.Error(), "positive size")
			assert.Nil(t, s3Policy, "a nil interface, not a typed nil wrapped in one")

			svPolicy, err := fifo.NewSievePolicy[string, int](size)
			require.Error(t, err, "NewSievePolicy(%d) must fail", size)
			assert.Contains(t, err.Error(), "positive size")
			assert.Nil(t, svPolicy)
		})
	}

	// The matching sibling behaviour, so this test fails if the analogy it
	// rests on ever stops holding.
	_, lruErr := policies.NewLRU[string, int](0)
	require.Error(t, lruErr, "the LRU adapter this mirrors must also reject zero")
}

// TestResizeToZeroIsStillLegal separates the two cases. Refusing to build a
// cache at zero must not stop an existing one being resized to zero, which is
// reachable through AdaptiveCache.Resize: it passes its own new capacity
// through to every arm, so a caller resizing the whole cache to zero resizes
// each of them to zero. (A shadow's miniature capacity never reaches zero on
// its own; scaledCapacity and shadowCapacity both floor it at one.)
func TestResizeToZeroIsStillLegal(t *testing.T) {
	forEachAlgorithm(t, func(t *testing.T, newPolicy build) {
		t.Helper()

		p := newPolicy(t, 8)
		for i := 0; i < 8; i++ {
			p.Add("key-"+strconv.Itoa(i), i)
		}

		p.Resize(0)

		assert.Zero(t, p.Len(), "a policy resized to zero must hold nothing")
		assert.Empty(t, p.Keys())
		assert.Empty(t, p.Values())

		p.Add("x", 1)
		assert.Zero(t, p.Len(), "and must accept nothing afterwards")

		// And back up again, which is the promotion path.
		p.Resize(8)
		p.Add("y", 2)
		got, ok := p.Get("y")
		require.True(t, ok, "a policy resized back up must accept entries again")
		assert.Equal(t, 2, got)
	})
}

// TestKeysAndValuesCorrespond checks the contract that matters rather than the
// one that is easy to check. Equal lengths are not enough: if Values ever
// misplaced an entry Keys had reported, every value after the gap would be
// attributed to the wrong key, and the lengths alone would not reveal it. The
// non-zero assertion covers the other way of getting the length right and the
// contents wrong - padding a gap with a zero value, which is the shape of the
// defect this repository documents against expirable.LRU.
func TestKeysAndValuesCorrespond(t *testing.T) {
	forEachAlgorithm(t, func(t *testing.T, newPolicy build) {
		t.Helper()

		const size = 32
		p := newPolicy(t, size)

		// Churn well past capacity so evictions, overwrites and removals have
		// all happened before the snapshot is taken.
		for i := 0; i < size*10; i++ {
			key := "key-" + strconv.Itoa(i%(size*2))
			p.Add(key, value(i%(size*2)))
			if i%7 == 0 {
				p.Get(key)
			}
			if i%11 == 0 {
				p.Remove("key-" + strconv.Itoa((i*3)%(size*2)))
			}
		}

		keys, values := p.Keys(), p.Values()
		require.Len(t, values, len(keys),
			"Keys and Values must describe the same set of entries")

		for i, key := range keys {
			want, ok := p.Peek(key)
			require.True(t, ok, "Keys returned %q, which the cache does not hold", key)
			assert.Equal(t, want, values[i],
				"Values[%d] belongs to key %q but holds another entry's value", i, key)
			assert.NotZero(t, values[i],
				"no stored value is zero here, so a zero at index %d is a padded placeholder", i)
		}
	})
}
