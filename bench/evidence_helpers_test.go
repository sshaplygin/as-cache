package bench_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEvidenceHelpersEmpty(t *testing.T) {
	assert.NotPanics(t, func() {
		assert.True(t, math.IsNaN((spread{}).median()))
		assert.Equal(t, "n/a", (spread{}).String())
		best, worst := (traceRecord{}).bestAndWorst()
		assert.Empty(t, best)
		assert.Empty(t, worst)
	})
}

func TestEvidenceMedianEven(t *testing.T) {
	assert.Equal(t, 2.5, (spread{Runs: []float64{4, 1, 2, 3}}).median())
}
