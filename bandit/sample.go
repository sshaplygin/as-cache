package bandit

import (
	"math"
	"math/rand/v2"
)

// betaSample draws from Beta(a, b) as the ratio of two Gamma draws, which is
// the standard construction: if X ~ Gamma(a,1) and Y ~ Gamma(b,1) then
// X/(X+Y) ~ Beta(a,b).
func betaSample(rng *rand.Rand, a, b float64) float64 {
	x := gammaSample(rng, a)
	y := gammaSample(rng, b)
	if x+y == 0 {
		return 0
	}

	return x / (x + y)
}

// gammaSample draws from Gamma(shape, 1) using Marsaglia and Tsang's method,
// with the standard boost for shapes below 1.
func gammaSample(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		// Gamma(a) == Gamma(a+1) * U^(1/a) for a < 1.
		return gammaSample(rng, shape+1) * math.Pow(rng.Float64(), 1/shape)
	}

	d := shape - 1.0/3.0
	c := 1 / math.Sqrt(9*d)

	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v

		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}
