package ascache

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeySampler_FullRateAdmitsEverything(t *testing.T) {
	for _, rate := range []float64{1, 1.5, 2} {
		s := newKeySampler[string](rate)
		require.False(t, s.sampling, "rate %v must disable filtering", rate)

		for i := 0; i < 1000; i++ {
			assert.True(t, s.sampled("key-"+strconv.Itoa(i)),
				"rate %v must admit every key", rate)
		}
	}
}

func TestKeySampler_Deterministic(t *testing.T) {
	s := newKeySampler[string](0.05)

	for i := 0; i < 1000; i++ {
		key := "key-" + strconv.Itoa(i)
		want := s.sampled(key)
		for r := 0; r < 5; r++ {
			assert.Equal(t, want, s.sampled(key),
				"sampling decision for %q must be stable", key)
		}
	}
}

func TestKeySampler_RateIsApproximatelyHonoured(t *testing.T) {
	const keys = 200000

	for _, rate := range []float64{0.01, 0.05, 0.25} {
		s := newKeySampler[string](rate)

		sampled := 0
		for i := 0; i < keys; i++ {
			if s.sampled("key-" + strconv.Itoa(i)) {
				sampled++
			}
		}

		got := float64(sampled) / float64(keys)
		// Generous tolerance: this asserts the sampler is not wildly off,
		// not that a hash is perfectly uniform.
		assert.InDelta(t, rate, got, rate*0.15,
			"rate %v: sampled %d of %d keys (%.4f)", rate, sampled, keys, got)
	}
}

func TestKeySampler_ZeroRateAdmitsNothing(t *testing.T) {
	s := newKeySampler[string](0)

	for i := 0; i < 1000; i++ {
		require.False(t, s.sampled("key-"+strconv.Itoa(i)),
			"a zero rate must admit no keys")
	}
}

func TestKeySampler_WorksForNonStringKeys(t *testing.T) {
	type composite struct {
		A int
		B string
	}

	si := newKeySampler[int](0.5)
	sc := newKeySampler[composite](0.5)

	intSampled, compositeSampled := 0, 0
	for i := 0; i < 10000; i++ {
		if si.sampled(i) {
			intSampled++
		}
		if sc.sampled(composite{A: i, B: strconv.Itoa(i)}) {
			compositeSampled++
		}
	}

	assert.InDelta(t, 0.5, float64(intSampled)/10000, 0.05, "int keys")
	assert.InDelta(t, 0.5, float64(compositeSampled)/10000, 0.05, "struct keys")
}

func TestShadowCapacity(t *testing.T) {
	tests := []struct {
		name        string
		nominal     int
		rate        float64
		minCapacity int
		wantCap     int
		wantRate    float64
	}{
		{
			name:    "rate 1 disables sampling",
			nominal: 10000, rate: 1, minCapacity: 256,
			wantCap: 10000, wantRate: 1,
		},
		{
			name:    "capacity shrinks with the rate",
			nominal: 100000, rate: 0.05, minCapacity: 256,
			wantCap: 5000, wantRate: 0.05,
		},
		{
			name:    "floor raises the rate, not just the capacity",
			nominal: 1000, rate: 0.01, minCapacity: 256,
			wantCap: 256, wantRate: 0.256,
		},
		{
			name:    "a cache smaller than the floor disables sampling",
			nominal: 100, rate: 0.05, minCapacity: 256,
			wantCap: 100, wantRate: 1,
		},
		{
			name:    "zero capacity is left alone",
			nominal: 0, rate: 0.05, minCapacity: 256,
			wantCap: 0, wantRate: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCap, gotRate := shadowCapacity(tt.nominal, tt.rate, tt.minCapacity)
			assert.Equal(t, tt.wantCap, gotCap, "capacity")
			assert.InDelta(t, tt.wantRate, gotRate, 1e-9, "effective rate")
		})
	}
}

// TestShadowCapacity_PreservesSimulationIdentity checks the property the
// miniature relies on: its capacity is the same fraction of the full cache
// that the sample is of the keyspace. If the two drift apart the shadow is no
// longer simulating the cache it claims to.
func TestShadowCapacity_PreservesSimulationIdentity(t *testing.T) {
	for _, nominal := range []int{1000, 10000, 100000} {
		for _, rate := range []float64{0.01, 0.05, 0.2} {
			gotCap, gotRate := shadowCapacity(nominal, rate, 256)

			assert.InDelta(t, gotRate, float64(gotCap)/float64(nominal), 0.001,
				"nominal=%d rate=%v: capacity fraction must match the effective rate", nominal, rate)
		}
	}
}
