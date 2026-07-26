// Package ascache is a cache that chooses its own eviction policy.
//
// Choosing a replacement policy normally means guessing which one suits your
// traffic, and the cost of guessing wrong is large: on a cyclic access pattern
// just larger than the cache, LRU serves a 0% hit rate where W-TinyLFU serves
// 92%. This library removes the guess. It runs candidate policies side by side,
// measures them against your real traffic, and either tells you which one wins
// or switches to it for you.
//
// # How it works
//
// One policy is active and serves real data. The others are shadows: they
// receive the same key stream with zero values, purely so their hit rates can
// be compared. Every epoch each policy reports what it measured to a [Bandit],
// which picks the policy for the next epoch.
//
//	cache, err := ascache.NewAdaptiveCache(
//	    []ascache.Policy[string, int]{lru, twoQ, tinyLFU},
//	    myBandit,
//	    &ascache.Settings{EpochDuration: time.Minute},
//	)
//	defer cache.Close()
//
// The API is a superset of hashicorp/golang-lru/v2, so an existing cache can be
// swapped for one of these without changing call sites. [AdaptiveCache.Stats],
// [AdaptiveCache.Advice], [AdaptiveCache.ActivePolicy] and
// [AdaptiveCache.Close] are the additions.
//
// Ready-made policies live in companion modules, so the core has no
// dependencies: github.com/sshaplygin/as-cache/policies for LRU, 2Q, Random
// and TTL, .../policies/arc for ARC, .../policies/tinylfu for W-TinyLFU.
//
// # Start by observing
//
// The lowest-risk way to adopt this is not to let it switch anything. With
// [Settings.ObserveOnly] the cache behaves exactly like the first policy it was
// given, while every other policy is measured in the background, and
// [AdaptiveCache.Advice] reports what it found. No bandit is needed in this
// mode.
//
//	cache, _ := ascache.NewAdaptiveCache(policies, nil, &ascache.Settings{
//	    EpochDuration: time.Minute,
//	    ObserveOnly:   true,
//	})
//	// ... later ...
//	fmt.Println(cache.Advice())
//
// # Cost
//
// Shadow policies hold keys and eviction bookkeeping but never values, so they
// cost far less than a full copy: six policies measure at 2.65x the memory of
// one, and 1.32x with [Settings.ShadowSampleRate] set. Sampling has shadows
// track a deterministic fraction of the keyspace, which stops per-operation
// cost scaling with the number of policies.
//
// # What to expect
//
// Adaptive selection reliably beats the worst policy you might have picked and
// lands close to the best. On published traces it comes within about a point
// of the best fixed policy and occasionally beats it, without being told in
// advance which that is. It will not dramatically outperform a policy you have
// already measured and know suits your traffic.
//
// Epoch duration is the setting that matters most: too short and the cache
// spends its time migrating between policies rather than serving. See the
// README for measurements and configuration guidance.
package ascache
