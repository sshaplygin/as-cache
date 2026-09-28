package bench_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sshaplygin/as-cache/bench"
)

// traceSpec names a trace file and how to read it.
type traceSpec struct {
	file   string
	load   func(path string) (bench.Workload, error)
	cache  int
	source string
}

// knownTraces are the traces ./scripts/fetch-traces.sh downloads. Each is
// required when a trace directory is configured; partial datasets are errors.
func knownTraces() []traceSpec {
	return []traceSpec{
		{
			file:   "twitter_cluster052.csv",
			load:   func(p string) (bench.Workload, error) { return bench.LoadTrace(p, bench.TwitterFormat, 0) },
			cache:  10000,
			source: "Twitter Twemcache production KV cache (OSDI '20)",
		},
		{
			file:   "lirs_loop.trace.gz",
			load:   func(p string) (bench.Workload, error) { return bench.LoadTrace(p, bench.LIRSFormat, 0) },
			cache:  500,
			source: "LIRS loop: cyclic scan, adversarial for LRU (SIGMETRICS '02)",
		},
		{
			file:   "lirs_2_pools.trace.gz",
			load:   func(p string) (bench.Workload, error) { return bench.LoadTrace(p, bench.LIRSFormat, 0) },
			cache:  1000,
			source: "LIRS 2_pools: two interleaved pools with different locality",
		},
		{
			file:   "arc_p3.gz",
			load:   func(p string) (bench.Workload, error) { return bench.LoadARCTrace(p, 2000000) },
			cache:  20000,
			source: "ARC paper P3 workstation trace (FAST '03)",
		},
		{
			file:   "arc_oltp.gz",
			load:   func(p string) (bench.Workload, error) { return bench.LoadARCTrace(p, 2000000) },
			cache:  20000,
			source: "ARC paper OLTP database trace (FAST '03)",
		},
		{
			file: "meta_kvcache_202206_1.csv",
			load: func(p string) (bench.Workload, error) {
				return bench.LoadMetaKVTrace(p, bench.MetaKVFormat{}, 2000000)
			},
			cache:  10000,
			source: "Meta production key-value cache, 500 hosts over 5 days (CacheBench kvcache/202206)",
		},
	}
}

// msrVolumes finds the MSR Cambridge volumes present locally.
//
// They are listed by pattern rather than by name because the trace set has
// thirteen servers and several volumes each: fetch-traces.sh takes six of
// them, and anyone may add others. Anything named msr_<volume>.csv
// (optionally gzipped) is picked up.
func msrVolumes(dir string) []traceSpec {
	matches, err := filepath.Glob(filepath.Join(dir, "msr_*.csv*"))
	if err != nil {
		return nil
	}
	sort.Strings(matches)

	specs := make([]traceSpec, 0, len(matches))
	for _, path := range matches {
		if !strings.HasSuffix(path, ".csv") && !strings.HasSuffix(path, ".csv.gz") {
			continue
		}
		specs = append(specs, traceSpec{
			file: filepath.Base(path),
			load: func(p string) (bench.Workload, error) {
				return bench.LoadMSRTrace(p, bench.MSRFormat{}, 2000000)
			},
			cache:  20000,
			source: "MSR Cambridge enterprise block I/O, read requests (FAST '08)",
		})
	}

	return specs
}

// loadKnownTraces returns the traces present locally, skipping the test when
// none are configured.
func loadKnownTraces(t *testing.T) []struct {
	spec     traceSpec
	workload bench.Workload
} {
	t.Helper()

	dir, err := bench.TraceDir()
	if err != nil {
		t.Skipf("%s; run ./scripts/fetch-traces.sh and set %s", err, bench.TraceDirEnv)
	}

	for _, volume := range []string{"hm_0", "prn_0", "proj_0", "src1_2", "usr_0", "web_0"} {
		_, plainErr := os.Stat(filepath.Join(dir, "msr_"+volume+".csv"))
		_, gzipErr := os.Stat(filepath.Join(dir, "msr_"+volume+".csv.gz"))
		require.True(t, plainErr == nil || gzipErr == nil, "required MSR volume absent: %s", volume)
	}
	var found []struct {
		spec     traceSpec
		workload bench.Workload
	}

	for _, spec := range append(knownTraces(), msrVolumes(dir)...) {
		path := filepath.Join(dir, spec.file)
		_, statErr := os.Stat(path)
		require.NoError(t, statErr, "required trace absent: %s", spec.file)

		w, loadErr := spec.load(path)
		require.NoError(t, loadErr, "load %s", spec.file)
		found = append(found, struct {
			spec     traceSpec
			workload bench.Workload
		}{spec, w})
	}

	if len(found) == 0 {
		t.Skipf("no known traces in %s; run ./scripts/fetch-traces.sh", dir)
	}

	return found
}

// TestTraceLoaders checks the parsers against the published ground truth for
// each trace, so a format misread cannot quietly produce a plausible-looking
// key stream and wrong evidence with it.
func TestTraceLoaders(t *testing.T) {
	if testing.Short() {
		t.Skip("evidence run; use make evidence")
	}

	dir, err := bench.TraceDir()
	if err != nil {
		t.Skipf("%s; run ./scripts/fetch-traces.sh", err)
	}

	// Counts measured from the published files.
	expectations := map[string]struct {
		requests int
		distinct int
		load     func(path string) (bench.Workload, error)
	}{
		"lirs_loop.trace.gz": {
			requests: 505500, distinct: 1011,
			load: func(p string) (bench.Workload, error) { return bench.LoadTrace(p, bench.LIRSFormat, 0) },
		},
		"lirs_2_pools.trace.gz": {
			requests: 100000, distinct: 9939,
			load: func(p string) (bench.Workload, error) { return bench.LoadTrace(p, bench.LIRSFormat, 0) },
		},
		// Counted independently from the published slice with awk, not read
		// back off this loader: a parser pinned against its own output would
		// agree with itself no matter what it did. The whole 128 MiB slice
		// holds 5,696,382 rows - 4,620,969 GET, 1,020,845 SET, 54,568 DELETE -
		// which expand by op_count to these 9,313,712 read requests.
		"meta_kvcache_202206_1.csv": {
			requests: 9313712, distinct: 1144478,
			load: func(p string) (bench.Workload, error) { return bench.LoadMetaKVTrace(p, bench.MetaKVFormat{}, 0) },
		},
	}

	for file, want := range expectations {
		path := filepath.Join(dir, file)
		if _, statErr := os.Stat(path); statErr != nil {
			continue
		}

		t.Run(file, func(t *testing.T) {
			w, loadErr := want.load(path)
			require.NoError(t, loadErr)

			assert.Equal(t, want.requests, w.Len(),
				"request count must match the published file; a parser that drops or invents records invalidates every number derived from it")
			assert.Equal(t, want.distinct, bench.DistinctKeys(w), "distinct key count")
		})
	}

	// The Meta layout collapses runs of identical operations into one row and
	// a repeat count, so the request count must exceed the row count. A loader
	// that reads one row as one request keeps every key and loses most of the
	// reuse, which shows up as hit rates that are wrong and plausible.
	metaPath := filepath.Join(dir, "meta_kvcache_202206_1.csv")
	if _, statErr := os.Stat(metaPath); statErr == nil {
		t.Run("meta kvcache expansion", func(t *testing.T) {
			const requests = 2000000

			w, loadErr := bench.LoadMetaKVTrace(metaPath, bench.MetaKVFormat{}, requests)
			require.NoError(t, loadErr)
			require.Equal(t, requests, w.Len(), "the limit must be met exactly")

			var rows, reads, writes int
			_, scanErr := fmt.Sscanf(w.Description,
				"Meta kvcache trace: %d rows (%d reads, %d writes)", &rows, &reads, &writes)
			require.NoError(t, scanErr, "description %q", w.Description)

			t.Logf("%d rows (%d reads, %d writes) -> %d requests over %d distinct keys",
				rows, reads, writes, w.Len(), bench.DistinctKeys(w))

			assert.Less(t, reads, requests,
				"op_count must expand: %d read rows cannot yield %d requests without it", reads, requests)
			assert.Positive(t, writes, "the trace contains SET rows, and they must be recognised as writes")
		})
	}

	// The ARC layout expands each record into blockCount accesses, so the
	// access count must exceed the line count. Getting this wrong yields a
	// workload incomparable with the published literature.
	arcPath := filepath.Join(dir, "arc_p3.gz")
	if _, statErr := os.Stat(arcPath); statErr == nil {
		t.Run("arc_p3 expansion", func(t *testing.T) {
			w, loadErr := bench.LoadARCTrace(arcPath, 0)
			require.NoError(t, loadErr)

			assert.True(t, strings.Contains(w.Description, "expanded"))
			assert.Greater(t, w.Len(), 3000000,
				"each ARC record stands for blockCount accesses; without the expansion p3 yields far too few")
		})
	}
}
