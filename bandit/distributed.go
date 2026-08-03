package bandit

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	ascache "github.com/sshaplygin/as-cache"
)

var (
	_ ascache.Bandit      = (*Distributed)(nil)
	_ ascache.EpochBandit = (*Distributed)(nil)
)

// Distributed pools every replica's measurements through a shared store, so a
// fleet of caches selects on the fleet's evidence rather than on each
// replica's own.
//
// It exists for the case the README gives as a reason not to use this library
// at all: a cache that sees too few requests per epoch to tell its arms apart.
// That is usually not a property of the traffic but of how it was divided - a
// fleet of a hundred replicas each sees a hundredth of it. Pooling puts the
// evidence back together without moving the caches.
//
// # Nothing here runs on the cache's path
//
// RecordEpoch folds numbers into a buffer and returns; SelectPolicy is an
// atomic load. Both are called by the cache while it holds its write lock,
// where a round trip to a store would stall every Get in the process for its
// duration - and Go's RWMutex queues readers behind a waiting writer, so a
// store that hangs would hang the cache. All I/O happens on this type's own
// goroutine, once per coordination epoch.
//
// # When the store is unreachable
//
// Selection falls back to a local Thompson bandit fed by this replica's own
// reports, which is exactly the behaviour of a cache that was never
// distributed. Nothing fails and nothing blocks; [Distributed.Snapshot]
// reports that it happened. Counts measured during the outage are discarded
// rather than replayed on recovery: evidence that arrives in the wrong window
// is worse than no evidence, because the whole scheme rests on recent buckets
// describing recent traffic.
type Distributed struct {
	cfg Config

	// local is the fallback, and it is fed on every report rather than only
	// during an outage - a bandit that started learning at the moment it was
	// needed would spend the outage exploring from scratch.
	local *Thompson

	// selection is what SelectPolicy returns, held as the numeric value of a
	// PolicyType. It is written only by the coordination goroutine and read on
	// the cache's epoch path, so it is atomic rather than mutex-guarded:
	// SelectPolicy must never wait on the goroutine, which may be
	// mid-round-trip.
	selection atomic.Uint64

	// rng is confined to the coordination goroutine: jitter and, under
	// ModeSharedPosterior, this replica's own draw. It is deliberately not
	// shared with local, which has its own.
	rng *rand.Rand

	mu sync.Mutex
	// pending accumulates what the cache has reported since the last sync.
	pending map[ArmKey]ascache.PolicyStats
	// shape is the measurement regime the reports describe and namespace is
	// the fingerprinted namespace derived from it. Both are set by the first
	// report and updated if the cache's shape ever changes - a resize moves
	// this replica to a different namespace, because its numbers stopped being
	// comparable with the fleet's.
	shape     regime
	haveShape bool
	namespace string
	// arms is the set of policies this cache actually has. A decision naming
	// anything else is refused: it means something with a different build or
	// configuration is publishing into this namespace.
	arms  map[ascache.PolicyType]struct{}
	state state

	// ctx is cancelled by Close, and every round trip derives its context from
	// it. Without that a Close arriving while a sync is in flight would wait
	// out the whole SyncTimeout against a store that has stopped answering -
	// so shutting down behind an unreachable store would take as long as the
	// store was allowed to take.
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// state is the observable part, guarded by mu.
type state struct {
	lastSync     time.Time
	lastBucket   Bucket
	lastErr      error
	syncs        int64
	syncFailures int64
	leaderships  int64
	decisions    int64
	rejected     int64
	fallback     bool
	fleet        map[ascache.PolicyType]weighted
}

// NewDistributed starts a distributed bandit and its coordination goroutine.
// Callers must call Close to stop it.
func NewDistributed(cfg Config) (*Distributed, error) {
	d, err := newDistributed(cfg)
	if err != nil {
		return nil, err
	}

	d.wg.Add(1)
	go d.coordinate()

	return d, nil
}

// newDistributed builds the bandit without starting its goroutine, so a test
// can drive sync rounds itself instead of racing a timer.
func newDistributed(cfg Config) (*Distributed, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	d := &Distributed{
		cfg:     cfg,
		local:   NewThompson(cfg.LocalDiscount, cfg.Seed),
		pending: make(map[ArmKey]ascache.PolicyStats),
		arms:    make(map[ascache.PolicyType]struct{}),
		//nolint:gosec // deliberate: a seeded, reproducible source, not a secret
		rng:    rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x2545f4914f6cdd1d)),
		ctx:    ctx,
		cancel: cancel,
	}
	d.selection.Store(uint64(ascache.Undefined))

	return d, nil
}

// RecordStats folds a single arm's report into the buffer.
//
// The cache calls RecordEpoch instead, since this type implements
// [ascache.EpochBandit]. This exists so a Distributed can still stand in
// wherever a plain [ascache.Bandit] is expected; the role is unknowable on
// this path, so counts are attributed to RoleShadow.
func (d *Distributed) RecordStats(stats ascache.ShadowStats) {
	d.local.RecordStats(stats)

	d.mu.Lock()
	defer d.mu.Unlock()

	d.arms[stats.Policy] = struct{}{}
	d.addLocked(ArmKey{Policy: stats.Policy, Role: RoleShadow}, stats)
}

// RecordEpoch folds one reporting epoch into the buffer and into the local
// fallback bandit. It performs no I/O and holds one uncontended mutex for the
// length of a few map writes, because the cache is holding its write lock
// while it runs.
func (d *Distributed) RecordEpoch(report ascache.EpochReport) {
	shape := regimeOf(report)

	for _, stats := range report.Stats {
		d.local.RecordStats(stats)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.haveShape || !d.shape.equal(shape) {
		// The cache's measurement regime changed - a Resize, most likely. Its
		// numbers are no longer comparable with what it published before, nor
		// with a fleet still running the old shape, so it moves to a different
		// namespace and starts accumulating there. Whatever was buffered was
		// measured under the old regime and goes with it.
		d.shape = shape
		d.haveShape = true
		d.namespace = scopedNamespace(d.cfg.Namespace, shape)
		clear(d.pending)

		d.arms = make(map[ascache.PolicyType]struct{}, len(report.Stats))
		for _, stats := range report.Stats {
			d.arms[stats.Policy] = struct{}{}
		}
	}

	for _, stats := range report.Stats {
		role := RoleShadow
		if stats.Policy == report.Active {
			role = RoleActive
		}

		d.addLocked(ArmKey{Policy: stats.Policy, Role: role}, stats)
	}
}

func (d *Distributed) addLocked(key ArmKey, stats ascache.ShadowStats) {
	counts := d.pending[key]
	counts.Hits += stats.Hits
	counts.Misses += stats.Misses
	d.pending[key] = counts
}

// SelectPolicy returns the policy the fleet has settled on, or - while the
// store is unreachable - the one this replica's own evidence favours. It
// returns [ascache.Undefined] until the first coordination round completes,
// which the cache reads as "no change".
//
// It is a single atomic load. The answer changes at the coordination cadence
// rather than the cache's epoch cadence, so a cache on a 50ms epoch and a
// bandit on a 1s one will see the same answer twenty times over. That is the
// design: a fleet-wide decision moves at the fleet's pace.
func (d *Distributed) SelectPolicy() ascache.PolicyType {
	// Only a PolicyType is ever stored here, by this type, so the round trip
	// through uint64 is lossless.
	return ascache.PolicyType(d.selection.Load())
}

// Close stops the coordination goroutine and waits for it to exit, cancelling
// any round trip in flight. It is idempotent. It does not close the store,
// which the caller supplied and may still be using.
func (d *Distributed) Close() error {
	d.stopOnce.Do(func() {
		d.cancel()
		d.wg.Wait()
	})

	return nil
}
