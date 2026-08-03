// Package benchclient adapts an AdaptiveCache to the client contract used by
// cache benchmark suites, in particular github.com/maypok86/benchmarks, whose
// hit-ratio simulator and throughput harness both drive a cache through:
//
//	Init(capacity int)
//	Get(key K) (V, bool)
//	Set(key K, value V)
//	Name() string
//	Close()
//
// Nothing here imports that repository. The contract is five methods, and Go
// interfaces are structural, so an adapter satisfies it by shape alone -- which
// keeps a benchmark suite's dependency tree out of this one, and keeps this
// package usable by any harness that wants the same five methods.
//
// # Determinism
//
// A benchmark that cannot be reproduced is a rumour. Cache adapts on an epoch
// clock, and a wall-clock epoch makes the result depend on how fast the machine
// replaying the trace happens to be: the same trace re-evaluates a different
// number of times on a loaded machine and lands on a different hit rate. This
// package therefore drives epochs by request count (ascache.Settings.
// EpochRequests) and seeds its bandit explicitly, so one trace produces one
// answer on any machine. Do not swap EpochRequests for EpochDuration here to
// make it look more like production; that trades the only property a benchmark
// needs for one it does not.
package benchclient

import (
	"fmt"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
	"github.com/sshaplygin/as-cache/policies"
	"github.com/sshaplygin/as-cache/policies/tinylfu"
)

// contract is the shape a benchmark harness requires. It is declared here, and
// asserted below, so that a change in this package that breaks the contract
// fails to compile rather than failing to register at run time.
type contract[K comparable, V any] interface {
	Init(capacity int)
	Get(key K) (V, bool)
	Set(key K, value V)
	Name() string
	Close()
}

var _ contract[uint64, uint64] = (*Cache[uint64, uint64])(nil)

// Default settings applied to a zero-value Cache.
const (
	// DefaultName is what the cache calls itself in a results table.
	DefaultName = "as-cache"

	// DefaultEpochRequests is how many Get calls end an epoch. It is a
	// compromise: too few and the cache spends the replay migrating rather
	// than serving, too many and a short trace finishes before the bandit has
	// ranked anything. Traces in these suites run to millions of requests, so
	// this yields hundreds to thousands of epochs.
	DefaultEpochRequests = 10_000

	// DefaultDiscount is the Thompson bandit's discount factor. Below 1 so the
	// bandit can change its mind when a trace changes phase, which is the
	// behaviour being measured.
	DefaultDiscount = 0.9

	// DefaultSeed fixes the bandit's random draws. A benchmark that reports a
	// different number each run reports nothing.
	DefaultSeed = 1
)

// Cache adapts an AdaptiveCache to the benchmark client contract. The zero
// value is usable and applies the defaults above; set any field before Init to
// override it.
//
// It is not safe to call Init or Close concurrently with the cache methods,
// which matches how a harness uses it: Init, drive, Close.
type Cache[K comparable, V any] struct {
	// Arms builds the policies to run for a given capacity. Nil uses
	// DefaultArms.
	Arms func(capacity int) ([]ascache.Policy[K, V], error)

	// Label overrides the name reported in results. Empty uses DefaultName.
	Label string

	// EpochRequests is how many Get calls end an epoch. Zero uses
	// DefaultEpochRequests.
	EpochRequests int64

	// ShadowSampleRate is passed through to ascache.Settings. Zero means every
	// key is measured, which is what a hit-ratio comparison wants: sampling
	// trades measurement fidelity for throughput, and a benchmark is paying
	// for fidelity.
	ShadowSampleRate float64

	// MigrationStrategy is passed through to ascache.Settings. The zero value
	// is MigrationCold.
	MigrationStrategy ascache.MigrationStrategy

	// Discount and Seed configure the Thompson bandit. Zero uses the defaults.
	Discount float64
	Seed     uint64

	cache *ascache.AdaptiveCache[K, V]
}

// DefaultArms is the policy set used when Cache.Arms is nil: LRU, LFU, 2Q and
// Random. Every one of them is deterministic, which is what makes a replay
// through this package reproducible.
//
// Two absences are deliberate.
//
// ARC is patented by IBM (US 6,996,676), which is why it lives in its own
// module here; pulling it into a package anyone might import would defeat that
// separation.
//
// W-TinyLFU is left out because it is not reproducible. Measured directly,
// with no cache and no bandit above it, one trace replayed three times gave
// three different hit counts and left the cache at 527, 504 and 545 entries
// against a capacity of 500: otter evicts asynchronously and reports an
// approximate size, so its result depends on how the run was scheduled. It is
// the strongest arm available and worth including when a comparison matters
// more than repeatability - see ArmsWithWindowTinyLFU, which is that trade
// made explicitly.
func DefaultArms[K comparable, V any](capacity int) ([]ascache.Policy[K, V], error) {
	lru, err := policies.NewLRU[K, V](capacity)
	if err != nil {
		return nil, fmt.Errorf("build LRU arm: %w", err)
	}

	lfu, err := policies.NewLFU[K, V](capacity)
	if err != nil {
		return nil, fmt.Errorf("build LFU arm: %w", err)
	}

	twoQueue, err := policies.NewTwoQueue[K, V](capacity)
	if err != nil {
		return nil, fmt.Errorf("build 2Q arm: %w", err)
	}

	return []ascache.Policy[K, V]{
		lru,
		lfu,
		twoQueue,
		policies.NewRandomPolicy[K, V](capacity),
	}, nil
}

// ArmsWithWindowTinyLFU is DefaultArms plus a W-TinyLFU arm.
//
// This is the set to use when the question is "how well does adaptive
// selection do against the best baselines", and the wrong one when the answer
// has to be the same twice: W-TinyLFU's result moves between runs for the
// reasons given on DefaultArms, and one unstable arm is enough to move which
// policy the bandit selects and therefore the whole replay.
func ArmsWithWindowTinyLFU[K comparable, V any](capacity int) ([]ascache.Policy[K, V], error) {
	arms, err := DefaultArms[K, V](capacity)
	if err != nil {
		return nil, err
	}

	windowTinyLFU, err := tinylfu.NewPolicy[K, V](capacity)
	if err != nil {
		return nil, fmt.Errorf("build W-TinyLFU arm: %w", err)
	}

	return append(arms, windowTinyLFU), nil
}

// Init builds the cache at the given capacity.
//
// It panics on failure, which is the contract every adapter in these suites
// follows: a harness has no way to report a cache that will not build, and
// silently benchmarking a nil cache would produce a number rather than an
// error.
func (c *Cache[K, V]) Init(capacity int) {
	arms := c.Arms
	if arms == nil {
		arms = DefaultArms[K, V]
	}

	built, err := arms(capacity)
	if err != nil {
		panic(fmt.Errorf("benchclient: build arms at capacity %d: %w", capacity, err))
	}

	epochRequests := c.EpochRequests
	if epochRequests <= 0 {
		epochRequests = DefaultEpochRequests
	}

	discount := c.Discount
	if discount <= 0 || discount > 1 {
		discount = DefaultDiscount
	}

	seed := c.Seed
	if seed == 0 {
		seed = DefaultSeed
	}

	cache, err := ascache.NewAdaptiveCache(built, bandit.NewThompson(discount, seed), &ascache.Settings{
		EpochRequests:     epochRequests,
		ShadowSampleRate:  c.ShadowSampleRate,
		MigrationStrategy: c.MigrationStrategy,
		// W-TinyLFU reports an approximate Len, so the capacity gate's exact
		// Len == Cap comparison may never fire and would silence measurement
		// for the whole replay.
		EvictPartialCapacityFilling: true,
	})
	if err != nil {
		panic(fmt.Errorf("benchclient: build cache at capacity %d: %w", capacity, err))
	}

	c.cache = cache
}

// Get returns the value stored for key, and whether it was present.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	return c.cache.Get(key)
}

// Set stores value under key. The contract has no return value, so the
// eviction report is discarded.
func (c *Cache[K, V]) Set(key K, value V) {
	_ = c.cache.Add(key, value)
}

// Name reports the label used in results tables.
func (c *Cache[K, V]) Name() string {
	if c.Label != "" {
		return c.Label
	}

	return DefaultName
}

// Close stops the cache's background goroutine and releases the instance, so a
// harness that reuses one adapter across capacities cannot leak a goroutine per
// run. The contract returns nothing; Close on an AdaptiveCache cannot fail.
func (c *Cache[K, V]) Close() {
	if c.cache == nil {
		return
	}

	_ = c.cache.Close()
	c.cache = nil
}

// ActivePolicy reports which policy is serving traffic, for a harness that
// wants to record what the cache settled on rather than only how it scored.
// It returns ascache.Undefined before Init and after Close.
func (c *Cache[K, V]) ActivePolicy() ascache.PolicyType {
	if c.cache == nil {
		return ascache.Undefined
	}

	return c.cache.ActivePolicy()
}
