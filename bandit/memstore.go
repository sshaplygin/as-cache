package bandit

import (
	"context"
	"sync"
	"time"

	ascache "github.com/sshaplygin/as-cache"
)

var _ Store = (*MemStore)(nil)

// MemStore is a Store held in memory, shared by every replica in one process.
//
// It is not a stand-in for a real store in production - a fleet in one process
// is not a fleet - but it is the right thing for three jobs: testing a
// distributed bandit without a server, simulating a fleet to see whether
// pooling helps before deploying anything, and giving the store contract one
// executable definition that the Valkey and Redis adapters are checked
// against.
//
// It implements the same semantics the adapters must: buckets come from the
// store's clock and never a replica's, leadership is first-come per bucket,
// a published decision is immutable, and everything expires.
type MemStore struct {
	mu sync.Mutex

	now func() time.Time

	counts    map[bucketKey]*bucketEntry
	leaders   map[bucketKey]entry[string]
	decisions map[bucketKey]entry[ascache.PolicyType]

	// failure, when non-nil, is returned by every call. It exists so a test
	// can take the store away mid-run and watch replicas fall back.
	failure error
}

type bucketKey struct {
	namespace string
	bucket    Bucket
}

type entry[T any] struct {
	value   T
	expires time.Time
}

type bucketEntry struct {
	arms    map[ArmKey]ascache.PolicyStats
	expires time.Time
}

// NewMemStore returns an empty in-memory store using the wall clock.
func NewMemStore() *MemStore {
	return &MemStore{
		now:       time.Now,
		counts:    make(map[bucketKey]*bucketEntry),
		leaders:   make(map[bucketKey]entry[string]),
		decisions: make(map[bucketKey]entry[ascache.PolicyType]),
	}
}

// SetClock replaces the store's clock, which is what assigns buckets. A test
// that drives this clock controls bucket boundaries exactly, with no sleeping
// and no dependence on how fast the machine is.
func (s *MemStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.now = now
}

// Fail makes every subsequent call return err, or restores normal operation
// when err is nil. It is how a test simulates the store going away.
func (s *MemStore) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failure = err
}

// Sync publishes one replica's counts, claims the bucket if asked and if it is
// unclaimed, and reports any decision already published for it.
func (s *MemStore) Sync(ctx context.Context, req SyncRequest) (SyncResult, error) {
	if err := ctx.Err(); err != nil {
		return SyncResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failure != nil {
		return SyncResult{}, s.failure
	}

	now := s.now()
	s.expireLocked(now)

	bucket := bucketAt(now, req.EpochMillis)
	key := bucketKey{namespace: req.Namespace, bucket: bucket}

	if len(req.Counts) > 0 {
		state, ok := s.counts[key]
		if !ok {
			state = &bucketEntry{arms: make(map[ArmKey]ascache.PolicyStats)}
			s.counts[key] = state
		}
		// The TTL is refreshed by every writer, so a bucket outlives its last
		// contribution rather than its first.
		state.expires = now.Add(req.CounterTTL)

		for _, count := range req.Counts {
			armKey := ArmKey{Policy: count.Policy, Role: count.Role}
			stats := state.arms[armKey]
			stats.Hits += count.Hits
			stats.Misses += count.Misses
			state.arms[armKey] = stats
		}
	}

	result := SyncResult{Bucket: bucket}

	if req.Lead {
		if _, taken := s.leaders[key]; !taken {
			s.leaders[key] = entry[string]{value: req.NodeID, expires: now.Add(req.LeaderTTL)}
			result.Leader = true
		}
	}

	if decision, ok := s.decisions[key]; ok {
		result.Decision, result.HasDecision = decision.value, true
	}

	return result, nil
}

// Window returns the buckets in [first, last] that hold counts. Buckets that
// were never written, or that have expired, are omitted rather than reported
// as zero.
func (s *MemStore) Window(ctx context.Context, namespace string, first, last Bucket) ([]WindowCounts, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failure != nil {
		return nil, s.failure
	}

	s.expireLocked(s.now())

	// The span is caller-supplied, and sizing the slice from it would let a
	// wide range allocate for buckets that were never written. Most windows
	// hold a handful of buckets; append covers the rest.
	window := make([]WindowCounts, 0, min(max(0, int(last-first)+1), 64))
	for bucket := first; bucket <= last; bucket++ {
		state, ok := s.counts[bucketKey{namespace: namespace, bucket: bucket}]
		if !ok {
			continue
		}

		arms := make(map[ArmKey]ascache.PolicyStats, len(state.arms))
		for armKey, stats := range state.arms {
			arms[armKey] = stats
		}
		window = append(window, WindowCounts{Bucket: bucket, Arms: arms})
	}

	return window, nil
}

// Decide publishes a decision for a bucket, leaving any existing one in place,
// and returns whichever decision is in force.
func (s *MemStore) Decide(
	ctx context.Context,
	namespace string,
	bucket Bucket,
	policy ascache.PolicyType,
	ttl time.Duration,
) (ascache.PolicyType, error) {
	if err := ctx.Err(); err != nil {
		return ascache.Undefined, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failure != nil {
		return ascache.Undefined, s.failure
	}

	now := s.now()
	s.expireLocked(now)

	key := bucketKey{namespace: namespace, bucket: bucket}
	if existing, exists := s.decisions[key]; exists {
		// Immutable once published: replicas act on it, and one that changed
		// underneath them would move the fleet mid-epoch for no reason.
		return existing.value, nil
	}

	s.decisions[key] = entry[ascache.PolicyType]{value: policy, expires: now.Add(ttl)}

	return policy, nil
}

// Close discards everything the store holds.
func (s *MemStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clear(s.counts)
	clear(s.leaders)
	clear(s.decisions)

	return nil
}

// expireLocked drops everything past its TTL. A real store does this itself;
// doing it here keeps a long-running simulation from growing without bound and
// keeps the two implementations behaving the same when a leader reads back
// further than the counters were kept for.
func (s *MemStore) expireLocked(now time.Time) {
	for key, state := range s.counts {
		if now.After(state.expires) {
			delete(s.counts, key)
		}
	}
	for key, state := range s.leaders {
		if now.After(state.expires) {
			delete(s.leaders, key)
		}
	}
	for key, state := range s.decisions {
		if now.After(state.expires) {
			delete(s.decisions, key)
		}
	}
}

// bucketAt divides the store's clock into coordination epochs. Every replica
// gets its bucket from here rather than computing one locally, which is what
// makes the scheme immune to clock skew across the fleet: a replica's own
// clock is never consulted, so it cannot be wrong in a way that matters.
func bucketAt(now time.Time, epochMillis int64) Bucket {
	if epochMillis <= 0 {
		epochMillis = 1
	}

	return Bucket(now.UnixMilli() / epochMillis)
}
