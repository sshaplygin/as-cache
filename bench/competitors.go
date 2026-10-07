package bench

import (
	"fmt"
	"time"

	theine "github.com/Yiling-J/theine-go"
	ristretto "github.com/dgraph-io/ristretto/v2"
	otter "github.com/maypok86/otter/v2"
	sturdyc "github.com/viccon/sturdyc"
)

// This file measures other people's caches, not this one's policies.
//
// Every claim in the README about where adaptive selection lands is relative to
// the policies in this repository. That is the wrong comparison for anyone
// choosing a cache library: the question is not "which eviction algorithm" but
// "which package". These adapters answer it by replaying the same traces
// through the libraries a Go user would actually pick.
//
// Each is configured to match github.com/maypok86/benchmarks where that
// repository also benchmarks it, so the numbers here can be read alongside the
// ones published there rather than as a private scale. Where it does not, the
// configuration is the library's documented default shape and is stated on the
// builder.
//
// These wrappers implement only Cache - Get and Add - which is all Replay
// needs. They are deliberately not ascache.Policy implementations: several of
// these libraries cannot satisfy that contract at all (ristretto never exposes
// its keys, sturdyc is keyed by string only), and pretending otherwise here
// would be the first step towards shipping an adapter that cannot work.

// CompetitorBuilder constructs a rival cache of the given capacity.
type CompetitorBuilder struct {
	Name  string
	Build func(size int) (Cache, error)
}

// Competitors returns the Go cache libraries worth measuring against.
func Competitors() []CompetitorBuilder {
	return []CompetitorBuilder{
		{"otter v2", newOtterCompetitor},
		{"theine", newTheineCompetitor},
		{"ristretto", newRistrettoCompetitor},
		{"sturdyc", newSturdycCompetitor},
	}
}

// otterCompetitor wraps otter v2, the W-TinyLFU implementation this repository
// also ships as a policy arm.
//
// # Why this calls CleanUp
//
// Admission and eviction are asynchronous, so a tight write replay can outrun
// maintenance. CleanUp drains pending work before a result is compared. The
// separate capacity-honesty test records retained entries after a write flood;
// it does not establish occupancy or a causal hit-rate advantage on other replays.
// This forced maintenance also affects timings, which are raw diagnostics for
// this harness rather than estimates of production throughput.
type otterCompetitor struct {
	cache *otter.Cache[string, int]
}

func newOtterCompetitor(size int) (Cache, error) {
	cache, err := otter.New(&otter.Options[string, int]{MaximumSize: size})
	if err != nil {
		return nil, fmt.Errorf("build otter cache: %w", err)
	}

	return &otterCompetitor{cache: cache}, nil
}

func (c *otterCompetitor) Get(key string) (int, bool) { return c.cache.GetIfPresent(key) }

func (c *otterCompetitor) Add(key string, value int) bool {
	c.cache.Set(key, value)
	c.cache.CleanUp()

	return false
}

// theineCompetitor wraps theine, whose admission policy is also W-TinyLFU
// derived but whose eviction is its own.
type theineCompetitor struct {
	cache *theine.Cache[string, int]
}

func newTheineCompetitor(size int) (Cache, error) {
	cache, err := theine.NewBuilder[string, int](int64(size)).Build()
	if err != nil {
		return nil, fmt.Errorf("build theine cache: %w", err)
	}

	return &theineCompetitor{cache: cache}, nil
}

func (c *theineCompetitor) Get(key string) (int, bool) { return c.cache.Get(key) }

func (c *theineCompetitor) Add(key string, value int) bool {
	// Cost 1 per entry and a TTL longer than any replay, matching
	// maypok86/benchmarks: this measures eviction, not expiry.
	c.cache.SetWithTTL(key, value, 1, time.Hour)

	return false
}

// ristrettoCompetitor wraps ristretto.
//
// Two things about ristretto make its number here worth reading carefully, and
// both are properties of the library rather than of this harness.
//
// New writes are asynchronous: Set can return after enqueueing, so an immediate
// Get can miss. A false return means the write was not queued. Even after a true
// return, the background admission policy can reject a new key. A read-through
// replay therefore measures the caller-visible admission and scheduling effects
// as well as eviction; its hit rate is not just an eviction-policy comparison.
//
// Calling Wait after every Set would drain the buffers and remove the first
// effect, at a cost that would dominate the timing column and measure something
// nobody runs. maypok86/benchmarks does not call it either, so this matches.
type ristrettoCompetitor struct {
	cache *ristretto.Cache[string, int]
}

func newRistrettoCompetitor(size int) (Cache, error) {
	cache, err := ristretto.NewCache(&ristretto.Config[string, int]{
		// Ten counters per admitted entry is ristretto's documented guidance
		// and is what maypok86/benchmarks uses.
		NumCounters: int64(size) * 10,
		MaxCost:     int64(size),
		BufferItems: 64,
		// Without this, cost accounting includes ristretto's own per-entry
		// overhead and the cache holds noticeably fewer than size entries,
		// which would compare it at the wrong capacity.
		IgnoreInternalCost: true,
	})
	if err != nil {
		return nil, fmt.Errorf("build ristretto cache: %w", err)
	}

	return &ristrettoCompetitor{cache: cache}, nil
}

func (c *ristrettoCompetitor) Get(key string) (int, bool) { return c.cache.Get(key) }

func (c *ristrettoCompetitor) Add(key string, value int) bool {
	c.cache.SetWithTTL(key, value, 1, time.Hour)

	return false
}

// sturdycCompetitor wraps sturdyc.
//
// sturdyc is a different kind of library: its subject is stampede protection
// and batching around a data source, and its eviction is sharded with a
// percentage-based sweep rather than a replacement policy. It is measured here
// because people choose it as a cache, not because it is trying to win a
// hit-rate comparison.
type sturdycCompetitor struct {
	cache *sturdyc.Client[int]
}

// sturdycShards is how many shards a competitor cache is built with.
//
// Capacity is divided across shards, so a shard's share of a small cache is
// small, and sturdyc evicts a percentage of a shard when that shard fills.
// Eight keeps the per-shard capacity meaningful at the capacities replayed
// here while still exercising the sharding it is built around.
const sturdycShards = 8

// sturdycEvictionPercent is the share of a full shard sturdyc drops when it
// needs room. Ten is the value its own documentation uses.
const sturdycEvictionPercent = 10

func newSturdycCompetitor(size int) (Cache, error) {
	if size < sturdycShards {
		return nil, fmt.Errorf("sturdyc needs at least %d entries to shard, got %d", sturdycShards, size)
	}

	// A TTL longer than any replay, so this measures eviction rather than
	// expiry - the workloads carry no notion of staleness.
	return &sturdycCompetitor{
		cache: sturdyc.New[int](size, sturdycShards, time.Hour, sturdycEvictionPercent),
	}, nil
}

func (c *sturdycCompetitor) Get(key string) (int, bool) { return c.cache.Get(key) }

func (c *sturdycCompetitor) Add(key string, value int) bool {
	c.cache.Set(key, value)

	return false
}
