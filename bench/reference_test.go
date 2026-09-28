package bench_test

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sshaplygin/as-cache/bench"
)

// referenceEnv names the file of libCacheSim results written by
// scripts/verify-ref.sh: one "file, capacity, requests, miss ratio" row per
// point, tab-separated.
const referenceEnv = "AS_CACHE_LRU_REFERENCE"

// referenceTolerance is the largest miss-ratio difference, in percentage
// points, accepted between this repository and libCacheSim at any one point.
// cachesim prints four decimals, so agreement shows up as a difference no
// larger than its rounding, 0.005 points.
const referenceTolerance = 0.0051

type referencePoint struct {
	file     string
	capacity int
	requests int
	miss     float64
}

// TestLRUMatchesReference is the Go half of scripts/verify-ref.sh. It loads
// every trace the reference covers through this repository's loaders, replays
// LRU at each capacity the reference lists, and requires libCacheSim's request
// count and a miss ratio within referenceTolerance.
//
// It skips unless the script has produced a reference, which is why the script
// rather than this test is the gate: the script fails if this test skips.
func TestLRUMatchesReference(t *testing.T) {
	path := os.Getenv(referenceEnv)
	if path == "" {
		t.Skipf("%s is not set; run ./scripts/verify-ref.sh", referenceEnv)
	}
	dir, err := bench.TraceDir()
	require.NoError(t, err)

	points := readReference(t, path)
	require.NotEmpty(t, points, "the reference file has no points")

	specs := map[string]traceSpec{}
	for _, spec := range append(knownTraces(), msrVolumes(dir)...) {
		specs[spec.file] = spec
	}

	byFile := map[string][]referencePoint{}
	order := []string{}
	for _, p := range points {
		if _, seen := byFile[p.file]; !seen {
			order = append(order, p.file)
		}
		byFile[p.file] = append(byFile[p.file], p)
	}

	for file := range specs {
		require.Contains(t, byFile, file, "evidence trace %s has no reference calibration", file)
	}

	lru := fixedPolicy(t, "LRU")
	for _, file := range order {
		spec, ok := specs[file]
		require.True(t, ok, "the reference covers %s, which the evidence suite does not read", file)

		t.Run(file, func(t *testing.T) {
			w, loadErr := spec.load(filepath.Join(dir, file))
			require.NoError(t, loadErr)

			evidenceCapacityCovered := false
			for _, p := range byFile[file] {
				evidenceCapacityCovered = evidenceCapacityCovered || p.capacity == spec.cache

				policy, buildErr := lru.Build(p.capacity)
				require.NoError(t, buildErr)
				miss := 1 - bench.Replay("LRU", policy, w).HitRate()

				delta := math.Abs(miss-p.miss) * 100
				t.Logf("%-26s %6d  requests %d/%d  miss %.4f/%.4f  |d| %.3f pts",
					file, p.capacity, len(w.Keys), p.requests, miss, p.miss, delta)

				assert.Equal(t, p.requests, len(w.Keys),
					"%s: the loader yields a different request count from the independent expansion", file)
				assert.LessOrEqual(t, delta, referenceTolerance,
					"%s at capacity %d: LRU miss ratio %.4f against libCacheSim's %.4f", file, p.capacity, miss, p.miss)
			}

			assert.True(t, evidenceCapacityCovered,
				"%s: the reference does not include capacity %d, the one the evidence suite uses", file, spec.cache)
		})
	}
}

func readReference(t *testing.T, path string) []referencePoint {
	t.Helper()

	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()

	var points []referencePoint
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		require.Len(t, fields, 4, "malformed reference row %q", scanner.Text())

		capacity, capErr := strconv.Atoi(fields[1])
		requests, reqErr := strconv.Atoi(fields[2])
		miss, missErr := strconv.ParseFloat(fields[3], 64)
		require.NoError(t, capErr)
		require.NoError(t, reqErr)
		require.NoError(t, missErr)

		points = append(points, referencePoint{fields[0], capacity, requests, miss})
	}
	require.NoError(t, scanner.Err())

	return points
}
