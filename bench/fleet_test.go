package bench_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
	"github.com/sshaplygin/as-cache/bench"
)

// fleetSettings is tuned the way the real-trace evidence says to tune it: an
// epoch long enough that the fleet is not spending its time migrating between
// policies, and warm migration so a switch does not throw the working set
// away.
func fleetSettings() ascache.Settings {
	return ascache.Settings{
		EpochDuration:               20 * time.Millisecond,
		EvictPartialCapacityFilling: true,
		MigrationStrategy:           ascache.MigrationWarm,
		ShadowSampleRate:            1,
	}
}

// TestFleet_DoesPoolingBeatDecidingAlone is the question the distributed
// bandit exists to answer, and it is set up so the answer can come back "no".
//
// A fleet is compared four ways on identical traffic: every replica running
// each fixed policy, every replica deciding alone, and the fleet pooling its
// evidence under each of the two coordination modes. The comparison that
// matters is pooled against local, since both are adaptive and only one of
// them can see the fleet.
func TestFleet_DoesPoolingBeatDecidingAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run: replays a fleet of caches, see `make evidence`")
	}

	const (
		replicas = 8
		size     = 500
	)

	workloads := []struct {
		name     string
		workload bench.Workload
		split    func(bench.Workload, int) []bench.Workload
	}{
		{
			name:     "zipf/homogeneous",
			workload: bench.Zipf(400_000, 20_000, 1.1, 11),
			split:    bench.Split,
		},
		{
			name:     "phase-shift/homogeneous",
			workload: bench.PhaseShift(4, 100_000, 20_000, 3_000, 12),
			split:    bench.Split,
		},
		{
			name:     "zipf/sharded",
			workload: bench.Zipf(400_000, 20_000, 1.1, 13),
			split:    bench.Shard,
		},
	}

	for _, w := range workloads {
		t.Run(w.name, func(t *testing.T) {
			shards := w.split(w.workload, replicas)

			perReplica := 0
			for _, shard := range shards {
				perReplica += shard.Len()
			}
			perReplica /= len(shards)

			results := make([]bench.FleetResult, 0, 12)

			for _, builder := range bench.FixedPolicies() {
				fixed, err := bench.ReplayFixedFleet(builder, shards, size)
				require.NoError(t, err)
				results = append(results, fixed)
			}

			setups := []bench.FleetSetup{
				bench.LocalFleet(0.7, 21),
				bench.PooledFleet("pooled/leader", bandit.ModeLeader, 50*time.Millisecond, 31),
				bench.PooledFleet("pooled/shared", bandit.ModeSharedPosterior, 50*time.Millisecond, 41),
			}

			adaptive := make(map[string]bench.FleetResult, len(setups))
			for _, setup := range setups {
				result, err := bench.ReplayFleet(setup, shards, size, fleetSettings())
				require.NoError(t, err)
				results = append(results, result)
				adaptive[setup.Name] = result
			}

			t.Logf("\n%s: %d replicas, %d requests each, cache size %d\n%s",
				w.name, replicas, perReplica, size, bench.FleetTable(results))

			best, worst := bestAndWorstFixed(results)
			t.Logf("best fixed policy %s at %.2f%%, worst %s at %.2f%%",
				best.Policy, best.HitRate()*100, worst.Policy, worst.HitRate()*100)

			for name, result := range adaptive {
				t.Logf("%s: %.2f%% (%+.2f vs best fixed, %+.2f vs local), %d policies in use at the end",
					name,
					result.HitRate()*100,
					(result.HitRate()-best.HitRate())*100,
					(result.HitRate()-adaptive["local"].HitRate())*100,
					result.Policies)
			}

			// The claim this repository is willing to make everywhere else is
			// the one asserted here: adaptive selection bounds the cost of
			// guessing wrong. Beating the best fixed policy is not claimed,
			// and is not asserted.
			for name, result := range adaptive {
				assert.Greater(t, result.HitRate(), worst.HitRate(),
					"%s did worse than the worst policy it could have been given", name)
			}

			// The fleet ending on one policy is reported, not asserted.
			// Replicas finish their shards at different moments, so each one's
			// last-applied decision is sampled at a different instant and two
			// of them straddling a fleet-wide switch is a property of when the
			// replay stopped, not of whether coordination worked. The
			// single-policy invariant is asserted where it can be checked
			// deterministically, against a clock the test controls: see
			// TestDistributed_FleetRunsOnePolicyAtATime in the bandit module.
		})
	}
}

// TestFleet_ThinTrafficIsWherePoolingShouldPay isolates the case the
// distributed bandit was built for: replicas each seeing too little traffic to
// rank arms on their own.
//
// It reports rather than asserts an improvement. If pooling does not help even
// here, that is the finding, and it belongs in the README rather than in a
// failing test.
func TestFleet_ThinTrafficIsWherePoolingShouldPay(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run: replays a fleet of caches, see `make evidence`")
	}

	const size = 300

	for _, replicas := range []int{2, 8, 32} {
		t.Run(fmt.Sprintf("%d-replicas", replicas), func(t *testing.T) {
			// The total traffic is fixed, so more replicas means each sees
			// less of it - which is exactly the axis pooling is supposed to
			// win on.
			shards := bench.Split(bench.Zipf(200_000, 20_000, 1.1, 17), replicas)

			local, err := bench.ReplayFleet(bench.LocalFleet(0.7, 5), shards, size, fleetSettings())
			require.NoError(t, err)

			pooled, err := bench.ReplayFleet(
				bench.PooledFleet("pooled/leader", bandit.ModeLeader, 50*time.Millisecond, 6),
				shards, size, fleetSettings())
			require.NoError(t, err)

			var best bench.FleetResult
			for _, builder := range bench.FixedPolicies() {
				fixed, err := bench.ReplayFixedFleet(builder, shards, size)
				require.NoError(t, err)
				if fixed.HitRate() > best.HitRate() {
					best = fixed
				}
			}

			t.Logf("%d replicas, %d requests each: local %.2f%%, pooled %.2f%% (%+.2f), best fixed %s %.2f%%",
				replicas, shards[0].Len(),
				local.HitRate()*100, pooled.HitRate()*100,
				(pooled.HitRate()-local.HitRate())*100,
				best.Policy, best.HitRate()*100)
		})
	}
}

// TestFleet_PacedThinTraffic is the case the distributed bandit was actually
// built for, and the only test here that reproduces it.
//
// Every other fleet measurement replays flat out, which delivers thousands of
// requests per epoch however small the workload is - so "thin traffic" never
// happens, the run just finishes sooner. Here each replica is held to a rate
// that puts a handful of requests in each cache epoch, which is the regime
// where a replica genuinely cannot rank its own arms and pooling has something
// to add.
//
// It costs wall-clock time to run, which is the point: the regime cannot be
// simulated any faster than it happens.
func TestFleet_PacedThinTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run: paced replay, takes tens of seconds")
	}

	const (
		replicas  = 8
		size      = 300
		perSecond = 400 // with a 20ms epoch, about 8 requests per epoch per replica
		requests  = 4_000
	)

	shards := bench.Split(bench.Zipf(requests*replicas, 20_000, 1.1, 77), replicas)

	local, err := bench.ReplayFleetPaced(
		bench.LocalFleet(0.7, 81), shards, size, fleetSettings(), perSecond)
	require.NoError(t, err)

	pooled, err := bench.ReplayFleetPaced(
		bench.PooledFleet("pooled/leader", bandit.ModeLeader, 200*time.Millisecond, 91),
		shards, size, fleetSettings(), perSecond)
	require.NoError(t, err)

	var best bench.FleetResult
	for _, builder := range bench.FixedPolicies() {
		fixed, err := bench.ReplayFixedFleet(builder, shards, size)
		require.NoError(t, err)
		if fixed.HitRate() > best.HitRate() {
			best = fixed
		}
	}

	t.Logf("paced fleet: %d replicas at %d req/s, %d requests each, ~%d requests per cache epoch",
		replicas, perSecond, shards[0].Len(),
		int(float64(perSecond)*fleetSettings().EpochDuration.Seconds()))
	t.Logf("  local          %.2f%% across %d policies", local.HitRate()*100, local.Policies)
	t.Logf("  pooled/leader  %.2f%% across %d policies (%+.2f vs local)",
		pooled.HitRate()*100, pooled.Policies, (pooled.HitRate()-local.HitRate())*100)
	t.Logf("  best fixed     %.2f%% (%s)", best.HitRate()*100, best.Policy)
}

// TestFleet_HeterogeneousShardsAreWherePoolingShouldHurt is the other side of
// the argument, and the reason ModeSharedPosterior exists at all.
//
// When replicas serve traffic of different shapes, one fleet-wide policy is a
// compromise, and a replica choosing for itself can do better than the fleet
// choosing for it. This measures how much that costs.
func TestFleet_HeterogeneousShardsAreWherePoolingShouldHurt(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run: replays a fleet of caches, see `make evidence`")
	}

	const (
		replicas = 6
		size     = 400
	)

	// Half the replicas get a looping scan, where recency policies serve
	// nothing; the other half get Zipf, where they do well. No single policy
	// is right for both.
	shards := make([]bench.Workload, 0, replicas)
	loop := bench.Split(bench.Loop(120_000, 2_000), replicas/2)
	zipf := bench.Split(bench.Zipf(120_000, 20_000, 1.1, 23), replicas/2)
	shards = append(shards, loop...)
	shards = append(shards, zipf...)

	local, err := bench.ReplayFleet(bench.LocalFleet(0.7, 51), shards, size, fleetSettings())
	require.NoError(t, err)

	pooled, err := bench.ReplayFleet(
		bench.PooledFleet("pooled/leader", bandit.ModeLeader, 50*time.Millisecond, 61),
		shards, size, fleetSettings())
	require.NoError(t, err)

	shared, err := bench.ReplayFleet(
		bench.PooledFleet("pooled/shared", bandit.ModeSharedPosterior, 50*time.Millisecond, 71),
		shards, size, fleetSettings())
	require.NoError(t, err)

	t.Logf("mixed fleet (%d loop replicas, %d zipf replicas):", replicas/2, replicas/2)
	t.Logf("  local          %.2f%% across %d policies", local.HitRate()*100, local.Policies)
	t.Logf("  pooled/leader  %.2f%% across %d policies (%+.2f vs local)",
		pooled.HitRate()*100, pooled.Policies, (pooled.HitRate()-local.HitRate())*100)
	t.Logf("  pooled/shared  %.2f%% across %d policies (%+.2f vs local)",
		shared.HitRate()*100, shared.Policies, (shared.HitRate()-local.HitRate())*100)

	assert.Equal(t, 1, pooled.Policies, "leader election commits the whole fleet to one policy")
}

// TestFleet_CoordinationEpochIsTheSettingThatMatters checks whether the gap
// between pooling and deciding alone is a consequence of how often the fleet
// gets to change its mind.
//
// The single-node evidence found epoch duration to be the setting that decides
// everything; the fleet has a second, slower clock, and a replay is only so
// many coordination rounds long. If the gap closes as the coordination epoch
// shortens, pooling is losing to a lack of decision points. If it does not,
// pooling is losing for a structural reason and no tuning will fix it.
func TestFleet_CoordinationEpochIsTheSettingThatMatters(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run: replays a fleet of caches, see `make evidence`")
	}

	const (
		replicas = 8
		size     = 500
	)

	shards := bench.Split(bench.Zipf(400_000, 20_000, 1.1, 11), replicas)

	local, err := bench.ReplayFleet(bench.LocalFleet(0.7, 21), shards, size, fleetSettings())
	require.NoError(t, err)
	t.Logf("local (no coordination): %.2f%%", local.HitRate()*100)

	for _, epoch := range []time.Duration{
		10 * time.Millisecond,
		25 * time.Millisecond,
		50 * time.Millisecond,
		200 * time.Millisecond,
	} {
		pooled, err := bench.ReplayFleet(
			bench.PooledFleet("pooled", bandit.ModeLeader, epoch, 31),
			shards, size, fleetSettings())
		require.NoError(t, err)

		t.Logf("coordination epoch %-6s: %.2f%% (%+.2f vs local), %d policies at the end",
			epoch, pooled.HitRate()*100,
			(pooled.HitRate()-local.HitRate())*100, pooled.Policies)
	}
}

func bestAndWorstFixed(results []bench.FleetResult) (best, worst bench.FleetResult) {
	fixedNames := make(map[string]struct{}, len(bench.FixedPolicies()))
	for _, builder := range bench.FixedPolicies() {
		fixedNames[builder.Name] = struct{}{}
	}

	first := true
	for _, result := range results {
		if _, ok := fixedNames[result.Policy]; !ok {
			continue
		}
		if first {
			best, worst, first = result, result, false

			continue
		}
		if result.HitRate() > best.HitRate() {
			best = result
		}
		if result.HitRate() < worst.HitRate() {
			worst = result
		}
	}

	return best, worst
}
