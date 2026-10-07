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
// Shadow policies hold keys and eviction bookkeeping while omitting payload
// values. Each additional arm still requires storage and work. Sampling reduces
// the measured keyspace and shadow capacity; it does not remove the dependence
// on the number of policies. Costs depend on the workload, payload and host.
//
// # What to expect
//
// Adaptive selection is experimental. Its hit rate can fall below fixed-policy
// baselines, and the observed differences depend on the workload and settings.
// Sampled shadows and asynchronous policies introduce variation across replays;
// an observed range does not bound future outcomes. Evaluate fixed alternatives
// on representative traffic before enabling automatic switching.
//
// Request-counted replays compare epoch settings reproducibly. Production uses
// wall-clock epochs, where switching and migration costs require evaluation on
// the service itself. See the README for current evidence and configuration.
package ascache
