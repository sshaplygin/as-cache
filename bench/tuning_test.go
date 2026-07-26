package bench_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bench"
)

// TestAdaptiveTuning asks whether the gap between adaptive selection and the
// best fixed policy is a property of the idea or of how it was configured.
//
// The first trace runs used a 2ms epoch with warm migration, which on a 20k
// cache means copying 20,000 entries several hundred times during a replay.
// That is a configuration that spends most of its time migrating, and blaming
// the approach for it would be a measurement error rather than a finding.
func TestAdaptiveTuning(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	configs := []struct {
		name     string
		epoch    time.Duration
		strategy ascache.MigrationStrategy
		gates    bool
	}{
		{"2ms epoch, warm migration", 2 * time.Millisecond, ascache.MigrationWarm, false},
		{"2ms epoch, cold migration", 2 * time.Millisecond, ascache.MigrationCold, false},
		{"50ms epoch, warm migration", 50 * time.Millisecond, ascache.MigrationWarm, false},
		{"50ms epoch, warm + stability gates", 50 * time.Millisecond, ascache.MigrationWarm, true},
	}

	for _, found := range loadKnownTraces(t) {
		spec, w := found.spec, found.workload

		t.Run(w.Name, func(t *testing.T) {
			// The bar: the best any single policy manages on this trace.
			best := bench.Result{}
			bestName := ""
			for _, builder := range bench.FixedPolicies() {
				policy, err := builder.Build(spec.cache)
				require.NoError(t, err)
				r := bench.Replay(builder.Name, policy, w)
				if r.HitRate() > best.HitRate() {
					best, bestName = r, builder.Name
				}
			}

			t.Logf("\n%s: best fixed is %s at %.2f%%", w.Name, bestName, best.HitRate()*100)

			for _, cfg := range configs {
				arms, err := bench.AdaptiveArms(spec.cache)
				require.NoError(t, err)

				settings := &ascache.Settings{
					EpochDuration:               cfg.epoch,
					EvictPartialCapacityFilling: true,
					MigrationStrategy:           cfg.strategy,
					ShadowSampleRate:            0.05,
					MinShadowCapacity:           64,
				}
				if cfg.gates {
					settings.MinHitRateImprovement = 0.02
					settings.SwitchCooldownEpochs = 3
				}

				cache, err := ascache.NewAdaptiveCache(arms, bench.NewThompsonBandit(0.7, 13), settings)
				require.NoError(t, err)

				r := bench.Replay("adaptive", cache, w)
				_ = cache.Close()

				t.Logf("  %-36s %6.2f%%  (%+6.2f pts vs best)  %7.0f ns/op",
					cfg.name, r.HitRate()*100, (r.HitRate()-best.HitRate())*100, r.NsPerOp())
			}
		})
	}
}
