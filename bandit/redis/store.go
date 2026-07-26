package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
)

var _ bandit.Store = (*Store)(nil)

// ErrNilClient is returned by New when Options.Client is nil.
var ErrNilClient = errors.New("redis: client must not be nil")

// Options configures a Store.
type Options struct {
	// Client is the connection to Valkey or Redis. Required.
	//
	// It is a UniversalClient so a plain client, a cluster client and a ring
	// are all accepted, and it is supplied rather than dialled here because
	// connection settings - timeouts, TLS, credentials, pool sizes - belong to
	// the application, which almost always has a client already.
	Client goredis.UniversalClient

	// KeyPrefix begins every key this store writes. Defaults to
	// DefaultKeyPrefix.
	KeyPrefix string
}

// Store implements bandit.Store over Valkey or Redis.
type Store struct {
	client goredis.UniversalClient
	prefix string
}

// New returns a Store over the given client. It does not take ownership of the
// client: Close leaves it open for the application to keep using.
func New(opts Options) (*Store, error) {
	if opts.Client == nil {
		return nil, ErrNilClient
	}

	prefix := opts.KeyPrefix
	if prefix == "" {
		prefix = DefaultKeyPrefix
	}

	return &Store{client: opts.Client, prefix: prefix}, nil
}

// Sync publishes one replica's counts, claims the bucket if asked and if it is
// unclaimed, and reports any decision published for it - in a single round
// trip.
func (s *Store) Sync(ctx context.Context, req bandit.SyncRequest) (bandit.SyncResult, error) {
	epochMillis := req.EpochMillis
	if epochMillis <= 0 {
		epochMillis = 1
	}

	lead := "0"
	if req.Lead {
		lead = "1"
	}

	args := make([]any, 0, 6+4*len(req.Counts))
	args = append(args,
		s.keyBase(req.Namespace),
		strconv.FormatInt(epochMillis, 10),
		strconv.FormatInt(millis(req.CounterTTL), 10),
		strconv.FormatInt(millis(req.LeaderTTL), 10),
		req.NodeID,
		lead,
	)

	for _, count := range req.Counts {
		// Zero deltas are dropped rather than sent. They would cost a HINCRBY
		// each and create the counter field for an arm that measured nothing,
		// which reads back as evidence of a zero hit rate rather than as an
		// absence of evidence.
		if count.Hits != 0 {
			args = append(args,
				countField(count.Policy, count.Role, true),
				strconv.FormatInt(count.Hits, 10))
		}
		if count.Misses != 0 {
			args = append(args,
				countField(count.Policy, count.Role, false),
				strconv.FormatInt(count.Misses, 10))
		}
	}

	raw, err := syncScript.Run(ctx, s.client, []string{s.anchorKey(req.Namespace)}, args...).Result()
	if err != nil {
		return bandit.SyncResult{}, fmt.Errorf("redis: sync: %w", err)
	}

	return parseSyncReply(raw)
}

// parseSyncReply converts the script's three-element reply.
func parseSyncReply(raw any) (bandit.SyncResult, error) {
	values, ok := raw.([]any)
	if !ok || len(values) != 3 {
		return bandit.SyncResult{}, fmt.Errorf("redis: sync: unexpected reply %T %v", raw, raw)
	}

	bucket, ok := values[0].(int64)
	if !ok {
		return bandit.SyncResult{}, fmt.Errorf("redis: sync: bucket is %T, want integer", values[0])
	}

	leader, ok := values[1].(int64)
	if !ok {
		return bandit.SyncResult{}, fmt.Errorf("redis: sync: leader flag is %T, want integer", values[1])
	}

	result := bandit.SyncResult{
		Bucket: bandit.Bucket(bucket),
		Leader: leader == 1,
	}

	decision, ok := values[2].(string)
	if !ok {
		return bandit.SyncResult{}, fmt.Errorf("redis: sync: decision is %T, want string", values[2])
	}
	if decision != "" {
		policy, err := parsePolicy(decision)
		if err != nil {
			return bandit.SyncResult{}, err
		}
		result.Decision, result.HasDecision = policy, true
	}

	return result, nil
}

// Window reads the aggregated counts for a range of buckets, pipelined into
// one round trip. Buckets that expired or were never written come back empty
// and are omitted, so a leader can tell a quiet epoch from a missing one.
func (s *Store) Window(
	ctx context.Context,
	namespace string,
	first, last bandit.Bucket,
) ([]bandit.WindowCounts, error) {
	if first > last {
		return nil, nil
	}

	buckets := make([]bandit.Bucket, 0, last-first+1)
	commands := make([]*goredis.MapStringStringCmd, 0, last-first+1)

	pipe := s.client.Pipeline()
	for bucket := first; bucket <= last; bucket++ {
		buckets = append(buckets, bucket)
		commands = append(commands, pipe.HGetAll(ctx, s.countsKey(namespace, bucket)))
	}

	// redis.Nil surfaces here only if a command in the pipeline missed, which
	// HGETALL never does - it returns an empty map. Any other error is real.
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("redis: window: %w", err)
	}

	window := make([]bandit.WindowCounts, 0, len(commands))
	for i, cmd := range commands {
		fields, err := cmd.Result()
		if err != nil {
			if errors.Is(err, goredis.Nil) {
				continue
			}

			return nil, fmt.Errorf("redis: window: bucket %d: %w", buckets[i], err)
		}
		if len(fields) == 0 {
			continue
		}

		arms := parseArms(fields)
		if len(arms) == 0 {
			continue
		}

		window = append(window, bandit.WindowCounts{Bucket: buckets[i], Arms: arms})
	}

	return window, nil
}

// parseArms turns a bucket's hash into per-arm counts, skipping anything it
// does not recognise.
func parseArms(fields map[string]string) map[bandit.ArmKey]ascache.PolicyStats {
	arms := make(map[bandit.ArmKey]ascache.PolicyStats, len(fields)/2)

	for field, value := range fields {
		policy, role, hits, ok := parseCountField(field)
		if !ok {
			continue
		}

		count, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			continue
		}

		key := bandit.ArmKey{Policy: policy, Role: role}
		stats := arms[key]
		if hits {
			stats.Hits += count
		} else {
			stats.Misses += count
		}
		arms[key] = stats
	}

	return arms
}

// Decide publishes the policy the fleet should run for a bucket and returns
// the decision actually in force for it.
func (s *Store) Decide(
	ctx context.Context,
	namespace string,
	bucket bandit.Bucket,
	policy ascache.PolicyType,
	ttl time.Duration,
) (ascache.PolicyType, error) {
	raw, err := decideScript.Run(ctx, s.client,
		[]string{s.anchorKey(namespace)},
		s.keyBase(namespace),
		strconv.FormatInt(int64(bucket), 10),
		strconv.FormatUint(uint64(policy), 10),
		strconv.FormatInt(millis(ttl), 10),
	).Result()
	if err != nil {
		return ascache.Undefined, fmt.Errorf("redis: decide: %w", err)
	}

	text, ok := raw.(string)
	if !ok {
		return ascache.Undefined, fmt.Errorf("redis: decide: reply is %T, want string", raw)
	}

	return parsePolicy(text)
}

// Close releases the store. The client belongs to the caller and is left open.
func (s *Store) Close() error { return nil }

// parsePolicy reads the numeric PolicyType a decision is stored as.
func parsePolicy(text string) (ascache.PolicyType, error) {
	number, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return ascache.Undefined, fmt.Errorf("redis: decision %q is not a policy: %w", text, err)
	}

	return ascache.PolicyType(number), nil
}

// millis rounds a TTL up to at least one millisecond. PEXPIRE with a
// non-positive argument deletes the key it was meant to keep alive.
func millis(d time.Duration) int64 {
	if ms := d.Milliseconds(); ms > 0 {
		return ms
	}

	return 1
}
