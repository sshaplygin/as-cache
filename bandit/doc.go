// Package bandit provides ready-made [ascache.Bandit] implementations.
//
// The core as-cache module deliberately ships none: which arm to pull is the
// interesting decision, and it depends on how fast the traffic moves. This
// module makes the common choices available without putting them, or their
// dependencies, in the core.
//
// # Local
//
// [Thompson] samples each arm's hit rate from a Beta posterior and picks the
// best draw, so an arm is chosen roughly as often as it is likely to be the
// best one. Evidence is discounted as it ages, which is what lets it change
// its mind when the workload does. [Greedy] always takes the best-measured arm
// and exists as a control: it shows what the adaptive layer achieves with no
// exploration at all.
//
// # Distributed
//
// [Distributed] pools evidence across a fleet of replicas through a shared
// store, so a cache seeing too little traffic to tell its arms apart can still
// benefit from selection. Each replica publishes its per-epoch counts; one
// replica per coordination epoch reads the fleet's aggregate and publishes the
// decision the others apply.
//
//	b, err := bandit.NewDistributed(bandit.Config{
//	    Store:             store, // from .../bandit/redis
//	    Namespace:         "sessions",
//	    CoordinationEpoch: time.Second,
//	})
//	defer b.Close()
//
// The store is an interface, not a client: [MemStore] runs a whole fleet in
// one process for tests, and github.com/sshaplygin/as-cache/bandit/redis backs
// it with Valkey or Redis.
//
// # What crosses the wire
//
// Per-policy hit and miss integers, and a policy name. No cache keys and no
// cache values ever leave the process.
//
// # Cost
//
// One round trip per replica per coordination epoch, plus two more for
// whichever replica is leading. Nothing touches the network on the cache's hot
// path, or while the cache holds a lock: [Distributed.RecordEpoch] folds
// numbers into a buffer and [Distributed.SelectPolicy] is an atomic load.
package bandit
