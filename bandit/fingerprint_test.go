package bandit

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ascache "github.com/sshaplygin/as-cache"
)

func report(capacity int, rate float64, arms ...ascache.PolicyType) ascache.EpochReport {
	stats := make([]ascache.ShadowStats, 0, len(arms))
	for _, arm := range arms {
		stats = append(stats, ascache.ShadowStats{Policy: arm})
	}

	return ascache.EpochReport{Stats: stats, Capacity: capacity, SampleRate: rate}
}

func TestRegime_SeparatesWhatCannotBePooled(t *testing.T) {
	base := regimeOf(report(1000, 1, ascache.LRU, ascache.TinyLFU))

	tests := []struct {
		name  string
		other ascache.EpochReport
	}{
		{
			name:  "different capacity",
			other: report(100, 1, ascache.LRU, ascache.TinyLFU),
		},
		{
			name:  "different sample rate",
			other: report(1000, 0.05, ascache.LRU, ascache.TinyLFU),
		},
		{
			name:  "different arms",
			other: report(1000, 1, ascache.LRU, ascache.LFU),
		},
		{
			name:  "extra arm",
			other: report(1000, 1, ascache.LRU, ascache.TinyLFU, ascache.LFU),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other := regimeOf(tt.other)

			assert.False(t, base.equal(other))
			assert.NotEqual(t,
				scopedNamespace("app", base),
				scopedNamespace("app", other))
		})
	}
}

func TestRegime_IdenticalShapesPool(t *testing.T) {
	left := regimeOf(report(1000, 0.05, ascache.LRU, ascache.TinyLFU))
	right := regimeOf(report(1000, 0.05, ascache.LRU, ascache.TinyLFU))

	assert.True(t, left.equal(right))
	assert.Equal(t, scopedNamespace("app", left), scopedNamespace("app", right))
}

func TestRegime_ToleratesFloatNoiseInTheSampleRate(t *testing.T) {
	// The rate reaches the bandit having been through a capacity-floor
	// calculation, so two replicas configured identically can differ in the
	// last bits. Splitting a fleet over that would be invisible and
	// undiagnosable.
	exact := regimeOf(report(1000, 0.05, ascache.LRU))
	noisy := regimeOf(report(1000, 0.05+1e-12, ascache.LRU))

	assert.True(t, exact.equal(noisy))
}

func TestRegime_DistinguishesRatesThatActuallyDiffer(t *testing.T) {
	// The tolerance must not be so wide that genuinely different sampling
	// regimes pool with each other.
	coarse := regimeOf(report(1000, 0.05, ascache.LRU))
	finer := regimeOf(report(1000, 0.06, ascache.LRU))

	assert.False(t, coarse.equal(finer))
}

func TestRegime_StringIsLegible(t *testing.T) {
	// When a fleet mysteriously splits, this is the string worth logging, so
	// it has to say what actually differs.
	r := regimeOf(report(2048, 0.25, ascache.LRU, ascache.TinyLFU))

	assert.Equal(t, "arms=LRU,TinyLFU;cap=2048;rate=0.2500", r.String())
}

func TestRegime_FingerprintIsStableAndShort(t *testing.T) {
	r := regimeOf(report(1000, 1, ascache.LRU, ascache.TinyLFU))

	first := r.fingerprint()
	assert.Len(t, first, 12)
	for range 10 {
		assert.Equal(t, first, r.fingerprint())
	}
}

func TestScopedNamespace_KeepsTheCallersNameReadable(t *testing.T) {
	r := regimeOf(report(1000, 1, ascache.LRU))

	assert.Contains(t, scopedNamespace("sessions", r), "sessions:")
}
