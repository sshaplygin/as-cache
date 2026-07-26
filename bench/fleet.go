package bench

import (
	"fmt"
	"strings"
	"sync"
	"time"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
)

// Shard splits a workload across n replicas by key, the way a consistent-hash
// router in front of a fleet would.
//
// Sharding by key rather than round-robin is what makes the simulation mean
// anything: a replica behind a hash router sees a stable slice of the keyspace
// and can build a working set in it. Round-robin would give every replica the
// full keyspace at 1/n the density, which is a different workload with
// different best policies, and would flatter pooling by making every replica's
// traffic identical.
func Shard(w Workload, n int) []Workload {
	if n <= 1 {
		return []Workload{w}
	}

	shards := make([]Workload, n)
	for i := range shards {
		shards[i] = Workload{
			Name:        fmt.Sprintf("%s/shard-%d", w.Name, i),
			Description: w.Description,
		}
	}

	for _, k := range w.Keys {
		// n is a positive int, so the remainder is below it and the conversion
		// back is exact.
		shard := int(fnv(k) % uint64(n)) //nolint:gosec // bounded by n on the line above
		shards[shard].Keys = append(shards[shard].Keys, k)
	}

	return shards
}

// Split divides a workload into n consecutive slices, so every replica sees
// the whole keyspace but only a fraction of the requests.
//
// This is the homogeneous case: every replica's traffic has the same shape, so
// the best policy is the same everywhere and pooling has nothing to reconcile.
// It is the case pooling should help most, which makes it the fair test of
// whether it helps at all.
func Split(w Workload, n int) []Workload {
	if n <= 1 {
		return []Workload{w}
	}

	shards := make([]Workload, n)
	for i := range shards {
		shards[i] = Workload{
			Name:        fmt.Sprintf("%s/slice-%d", w.Name, i),
			Description: w.Description,
		}
	}

	for i, k := range w.Keys {
		shards[i%n].Keys = append(shards[i%n].Keys, k)
	}

	return shards
}

// fnv hashes a key for sharding. Any stable hash does; this one avoids
// pulling in a dependency and avoids Go's map seed, which changes per process
// and would make a run unreproducible.
func fnv(s string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)

	h := uint64(offset)
	for i := range len(s) {
		h ^= uint64(s[i])
		h *= prime
	}

	return h
}

// ReplayPaced replays a workload at a target request rate rather than as fast
// as the machine allows.
//
// Every other measurement here runs flat out, which is right for comparing
// policies: it maximises requests per unit of wall clock and keeps runs short.
// It is wrong for the one question a distributed bandit exists to answer.
// "Too little traffic to tell the arms apart" means few requests per epoch,
// and an unpaced replay delivers thousands per epoch however small the
// workload is - the run just finishes sooner. Pacing is the only way to
// reproduce the regime the fleet is supposed to help with, and it costs
// wall-clock time to do it.
func ReplayPaced(name string, c Cache, w Workload, perSecond float64) Result {
	if perSecond <= 0 {
		return Replay(name, c, w)
	}

	result := Result{Policy: name}
	interval := time.Duration(float64(time.Second) / perSecond)

	start := time.Now()
	for i, key := range w.Keys {
		// Scheduled against the start rather than by sleeping a fixed interval
		// each time, so the accumulated cost of the cache operations does not
		// drift the rate downwards over a long run.
		if due := start.Add(time.Duration(i) * interval); time.Now().Before(due) {
			time.Sleep(time.Until(due))
		}

		if _, ok := c.Get(key); ok {
			result.Hits++

			continue
		}
		result.Misses++
		c.Add(key, i)
	}
	result.Duration = time.Since(start)

	return result
}

// FleetSetup describes one way of running a fleet, so the ways can be compared
// on identical traffic.
type FleetSetup struct {
	// Name identifies the setup in reports.
	Name string
	// Bandit builds the bandit for replica i. A setup that pools returns
	// bandits sharing one store; one that does not returns independent ones.
	Bandit func(replica int) (ascache.Bandit, func() error, error)
}

// LocalFleet gives every replica its own Thompson bandit and no shared state.
// It is the control: what a fleet does today, each replica deciding alone on
// the fraction of the traffic it happens to see.
func LocalFleet(discount float64, seed uint64) FleetSetup {
	return FleetSetup{
		Name: "local",
		Bandit: func(replica int) (ascache.Bandit, func() error, error) {
			// replica indexes a slice, so it is non-negative and small.
			return bandit.NewThompson(discount, seed+uint64(replica)), noClose, nil //nolint:gosec // see above
		},
	}
}

// PooledFleet gives every replica a distributed bandit over one shared
// in-memory store.
func PooledFleet(name string, mode bandit.Mode, epoch time.Duration, seed uint64) FleetSetup {
	store := bandit.NewMemStore()

	return FleetSetup{
		Name: name,
		Bandit: func(replica int) (ascache.Bandit, func() error, error) {
			b, err := bandit.NewDistributed(bandit.Config{
				Store:             store,
				Namespace:         "bench",
				NodeID:            fmt.Sprintf("r%d", replica),
				CoordinationEpoch: epoch,
				Mode:              mode,
				Window:            8,
				// replica indexes a slice, so it is non-negative and small.
				Seed: seed + uint64(replica), //nolint:gosec // see above
			})
			if err != nil {
				return nil, noClose, err
			}

			return b, b.Close, nil
		},
	}
}

func noClose() error { return nil }

// FleetResult is what a whole fleet served, plus how its replicas behaved.
type FleetResult struct {
	Result
	// Replicas is how many caches served the workload.
	Replicas int
	// Policies counts how many distinct policies the fleet ended on. Under
	// leader election this should be one; a fleet that ends split has either
	// lost its store or is running shared-posterior selection.
	Policies int
	// Ending lists each replica's final active policy.
	Ending []string
}

// ReplayFleet runs one workload across a fleet of AdaptiveCaches and reports
// what the fleet as a whole served.
//
// Replicas run concurrently, because the coordination this measures only
// happens in wall-clock time: a serial replay would let one replica finish
// before another had published anything, and the fleet would never actually
// overlap.
func ReplayFleet(
	setup FleetSetup,
	shards []Workload,
	size int,
	settings ascache.Settings,
) (FleetResult, error) {
	return replayFleet(setup, shards, size, settings, 0)
}

// ReplayFleetPaced is ReplayFleet with each replica held to perSecond
// requests, so the fleet actually runs in the thin-traffic regime it exists
// for rather than merely being given a small workload.
func ReplayFleetPaced(
	setup FleetSetup,
	shards []Workload,
	size int,
	settings ascache.Settings,
	perSecond float64,
) (FleetResult, error) {
	return replayFleet(setup, shards, size, settings, perSecond)
}

func replayFleet(
	setup FleetSetup,
	shards []Workload,
	size int,
	settings ascache.Settings,
	perSecond float64,
) (FleetResult, error) {
	type outcome struct {
		result Result
		ending string
	}

	outcomes := make([]outcome, len(shards))
	closers := make([]func() error, 0, len(shards)*2)

	caches := make([]*ascache.AdaptiveCache[string, int], len(shards))
	for i := range shards {
		arms, err := AdaptiveArms(size)
		if err != nil {
			return FleetResult{}, err
		}

		policyBandit, closeBandit, err := setup.Bandit(i)
		if err != nil {
			return FleetResult{}, err
		}
		closers = append(closers, closeBandit)

		perReplica := settings
		cache, err := ascache.NewAdaptiveCache(arms, policyBandit, &perReplica)
		if err != nil {
			return FleetResult{}, err
		}
		closers = append(closers, cache.Close)
		caches[i] = cache
	}

	defer func() {
		for _, close := range closers {
			_ = close()
		}
	}()

	start := time.Now()

	var wg sync.WaitGroup
	for i := range shards {
		wg.Add(1)
		go func() {
			defer wg.Done()

			outcomes[i].result = ReplayPaced(setup.Name, caches[i], shards[i], perSecond)
			outcomes[i].ending = caches[i].ActivePolicy().String()
		}()
	}
	wg.Wait()

	fleet := FleetResult{
		Result:   Result{Policy: setup.Name, Duration: time.Since(start)},
		Replicas: len(shards),
		Ending:   make([]string, 0, len(shards)),
	}

	distinct := make(map[string]struct{}, len(shards))
	for _, o := range outcomes {
		fleet.Hits += o.result.Hits
		fleet.Misses += o.result.Misses
		fleet.Ending = append(fleet.Ending, o.ending)
		distinct[o.ending] = struct{}{}
	}
	fleet.Policies = len(distinct)

	return fleet, nil
}

// ReplayFixedFleet runs the same shards against a single fixed policy per
// replica, which is the baseline every fleet setup has to be compared with:
// the hit rate a team would get by picking one policy and deploying it
// everywhere.
func ReplayFixedFleet(builder PolicyBuilder, shards []Workload, size int) (FleetResult, error) {
	fleet := FleetResult{
		Result:   Result{Policy: builder.Name},
		Replicas: len(shards),
		Ending:   make([]string, 0, len(shards)),
	}

	start := time.Now()
	for _, shard := range shards {
		policy, err := builder.Build(size)
		if err != nil {
			return FleetResult{}, fmt.Errorf("build %s: %w", builder.Name, err)
		}

		result := Replay(builder.Name, policyCache{policy}, shard)
		fleet.Hits += result.Hits
		fleet.Misses += result.Misses
		fleet.Ending = append(fleet.Ending, builder.Name)
	}
	fleet.Duration = time.Since(start)
	fleet.Policies = 1

	return fleet, nil
}

// policyCache adapts a bare Policy to the Cache the harness replays against.
type policyCache struct {
	policy ascache.Policy[string, int]
}

func (c policyCache) Get(key string) (int, bool)     { return c.policy.Get(key) }
func (c policyCache) Add(key string, value int) bool { return c.policy.Add(key, value) }

// FleetTable renders fleet results as a markdown table, best hit rate first.
func FleetTable(results []FleetResult) string {
	sorted := make([]FleetResult, len(results))
	copy(sorted, results)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].HitRate() > sorted[j-1].HitRate(); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	var b strings.Builder
	b.WriteString("| Setup | Hit rate | Policies in use at the end |\n| --- | --- | --- |\n")
	for _, r := range sorted {
		fmt.Fprintf(&b, "| %s | %.2f%% | %d |\n", r.Policy, r.HitRate()*100, r.Policies)
	}

	return b.String()
}
