package bench_test

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
	"github.com/sshaplygin/as-cache/bench"
)

// The published MSR and Meta traces are tens of gigabytes and cannot be
// committed, so the layout is pinned here against fixtures copied from the
// real files instead. These run in the ordinary test suite rather than under
// `make evidence`: a format misread is a correctness bug, not evidence, and it
// produces a plausible-looking workload that quietly invalidates every number
// derived from it.

// writeFixture writes a trace fixture and returns its path.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

// writeGzipFixture writes a gzipped trace fixture and returns its path.
func writeGzipFixture(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	file, err := os.Create(path)
	require.NoError(t, err)

	writer := gzip.NewWriter(file)
	_, err = writer.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())

	return path
}

// msrFixture is four records copied from the MSR Cambridge layout:
// Timestamp,Hostname,DiskNumber,Type,Offset,Size,ResponseTime. The offsets are
// exact multiples of 512 so the expected block numbers are exact.
const msrFixture = `128166372003061629,hm,0,Write,7014912,4096,26
128166372016382155,hm,0,Read,1074696192,4096,113
128166372026382155,hm,1,Read,1074696192,512,113
128166372036382155,prn,0,Read,1074696192,1024,113
`

func TestLoadMSRTraceExpandsEachRecordIntoItsBlocks(t *testing.T) {
	path := writeFixture(t, "msr_hm_0.csv", msrFixture)

	w, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 0)
	require.NoError(t, err)

	// 4096 bytes is eight 512-byte blocks, 512 is one, 1024 is two. Reading a
	// record as a single access would give three.
	assert.Equal(t, 11, w.Len(),
		"each record covers Size bytes and stands for that many block accesses")
	assert.Equal(t, []string{
		"hm:0:2099016", "hm:0:2099017", "hm:0:2099018", "hm:0:2099019",
		"hm:0:2099020", "hm:0:2099021", "hm:0:2099022", "hm:0:2099023",
		"hm:1:2099016",
		"prn:0:2099016", "prn:0:2099017",
	}, w.Keys)
	assert.Equal(t, "msr_hm_0", w.Name)
	assert.Contains(t, w.Description, "512-byte blocks")
}

// TestLoadMSRTraceNamespacesByHostAndDisk covers the trap in a trace set that
// spans thirteen servers: volumes are numbered from zero on each of them, so
// the same block number on two hosts is two different blocks.
func TestLoadMSRTraceNamespacesByHostAndDisk(t *testing.T) {
	path := writeFixture(t, "msr.csv", msrFixture)

	w, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 0)
	require.NoError(t, err)

	assert.Equal(t, 11, bench.DistinctKeys(w),
		"block 2099016 on hm:0, hm:1 and prn:0 must be three distinct keys, not one reused three times")
}

func TestLoadMSRTraceSkipsWritesUnlessAsked(t *testing.T) {
	path := writeFixture(t, "msr.csv", msrFixture)

	reads, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 0)
	require.NoError(t, err)

	both, err := bench.LoadMSRTrace(path, bench.MSRFormat{IncludeWrites: true}, 0)
	require.NoError(t, err)

	assert.Equal(t, 11, reads.Len())
	assert.Equal(t, 19, both.Len(), "the write record covers eight further blocks")
	assert.Equal(t, "hm:0:13701", both.Keys[0], "and comes first, at its own offset")
}

func TestLoadMSRTraceHonoursBlockSizeAndLimit(t *testing.T) {
	path := writeFixture(t, "msr.csv", msrFixture)

	coarse, err := bench.LoadMSRTrace(path, bench.MSRFormat{BlockSize: 4096}, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"hm:0:262377", "hm:1:262377", "prn:0:262377"}, coarse.Keys,
		"at 4 KiB every one of these reads is a single block")

	limited, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 3)
	require.NoError(t, err)
	assert.Equal(t, 3, limited.Len(), "the limit must cut inside a record's expansion, not after it")
}

func TestLoadMSRTraceToleratesHeadersAndTruncation(t *testing.T) {
	path := writeGzipFixture(t, "msr.csv.gz",
		"Timestamp,Hostname,DiskNumber,Type,Offset,Size,ResponseTime\n"+
			msrFixture+
			"128166372046382155,hm,0,Read,10747")

	w, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 0)
	require.NoError(t, err)

	assert.Equal(t, 11, w.Len(),
		"a header line and the truncated last line of a partial download must both be skipped")
}

func TestLoadMSRTraceRejectsAFileWithNoRecords(t *testing.T) {
	path := writeFixture(t, "msr.csv", "not,a,trace\n")

	_, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 0)

	require.Error(t, err, "a file that yields nothing must fail rather than return an empty workload")
}

// metaKV2022Fixture is the layout of the kvcache/202206 and 202210 releases.
const metaKV2022Fixture = `key,op,size,op_count,key_size
1668757755,SET,82,1,40
1668757755,GET,0,1,40
1665497896,GET,169,18,78
1665497896,GET,169,2,78
`

// metaKV2024Fixture is the layout of the kvcache/202401 release, which added
// columns and reordered the ones that were already there.
const metaKV2024Fixture = `op_time,key,key_size,op,op_count,size,cache_hits,ttl,usecase,sub_usecase
604038471,82131353f9ddc8c6,48,GET,1,87,1,0,366387042,3003229276
604038471,a54f88ab02ece24d,24,GET,3,0,0,0,699950563,699950563
604038473,828769c1e1ce3a29,67,SET,1,65,0,7200,2777918327,655456894
604038473,749ab7237b8d0372,47,GET_LEASE,7,10,7,0,176962275,176962275
`

// TestLoadMetaKVTraceExpandsOpCount is the property the whole loader exists
// for. Reading one row as one request would turn 21 requests into 3 and take
// most of the reuse out of the workload with them.
func TestLoadMetaKVTraceExpandsOpCount(t *testing.T) {
	path := writeFixture(t, "kvcache_traces_1.csv", metaKV2022Fixture)

	w, err := bench.LoadMetaKVTrace(path, bench.MetaKVFormat{}, 0)
	require.NoError(t, err)

	assert.Equal(t, 21, w.Len(), "1 + 18 + 2 requests across the three GET rows")
	assert.Equal(t, 2, bench.DistinctKeys(w))
	assert.Equal(t, "1668757755", w.Keys[0])
	assert.Equal(t, "1665497896", w.Keys[1], "the repeats of a row must be consecutive")
	assert.Contains(t, w.Description, "expanded by op_count")
}

// TestLoadMetaKVTraceReadsThe2024Layout covers the release that moved the
// columns. Located by position rather than by name, the key column of this
// file is the operation and nothing at all would parse.
func TestLoadMetaKVTraceReadsThe2024Layout(t *testing.T) {
	path := writeFixture(t, "kvcache_traces_1.csv", metaKV2024Fixture)

	w, err := bench.LoadMetaKVTrace(path, bench.MetaKVFormat{}, 0)
	require.NoError(t, err)

	assert.Equal(t, 11, w.Len(), "1 + 3 GET requests and 7 GET_LEASE requests")
	assert.Equal(t, 3, bench.DistinctKeys(w))
	assert.Contains(t, w.Keys, "749ab7237b8d0372", "GET_LEASE is a read")
	assert.NotContains(t, w.Keys, "828769c1e1ce3a29", "SET is not")
}

func TestLoadMetaKVTraceIncludesWritesOnRequest(t *testing.T) {
	path := writeFixture(t, "kvcache.csv", metaKV2022Fixture)

	w, err := bench.LoadMetaKVTrace(path, bench.MetaKVFormat{IncludeWrites: true}, 0)
	require.NoError(t, err)

	assert.Equal(t, 22, w.Len(), "the SET row adds one request")
	assert.Contains(t, w.Description, "reads and writes")
}

func TestLoadMetaKVTraceHonoursTheLimitInsideARow(t *testing.T) {
	path := writeFixture(t, "kvcache.csv", metaKV2022Fixture)

	w, err := bench.LoadMetaKVTrace(path, bench.MetaKVFormat{}, 5)
	require.NoError(t, err)

	assert.Equal(t, 5, w.Len(),
		"a row expanding into 18 requests must be cut at the limit rather than overshooting it")
}

func TestLoadMetaKVTraceToleratesATruncatedTail(t *testing.T) {
	path := writeGzipFixture(t, "kvcache.csv.gz", metaKV2024Fixture+"604038473,ccdddef945c01aea,")

	w, err := bench.LoadMetaKVTrace(path, bench.MetaKVFormat{}, 0)
	require.NoError(t, err)

	assert.Equal(t, 11, w.Len(),
		"these files are only usable partially downloaded, so the last row is normally incomplete")
}

func TestLoadMetaKVTraceRejectsAnUnrecognisedHeader(t *testing.T) {
	path := writeFixture(t, "kvcache.csv", "timestamp,value\n1,2\n")

	_, err := bench.LoadMetaKVTrace(path, bench.MetaKVFormat{}, 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognised header",
		"a layout this loader does not know must fail loudly; read at the wrong offsets it would look like an empty trace")
}

// TestEveryArmDrivesAnAdaptiveCache guards the wiring the whole evidence suite
// rests on.
//
// AdaptiveArms builds one policy per entry in FixedPolicies, and every replay
// through an AdaptiveCache uses it. Two arms reporting the same PolicyType
// collide in the cache's policy map and the constructor rejects the whole set,
// so a copy-paste in a new adapter takes out every adaptive measurement at
// once - and a policy added to FixedPolicies but not reachable as an arm would
// be measured on its own and silently left out of the comparison it exists for.
//
// It runs in `make test` rather than under `make evidence` because it is
// wiring, not evidence: no traces, no timing, nothing to interpret.
func TestEveryArmDrivesAnAdaptiveCache(t *testing.T) {
	const size = 256

	arms, err := bench.AdaptiveArms(size)
	require.NoError(t, err, "every arm must build")
	require.Len(t, arms, len(bench.FixedPolicies()),
		"AdaptiveArms must offer the bandit every policy the fixed comparison measures")

	seen := map[ascache.PolicyType]bool{}
	for _, arm := range arms {
		require.NotEqual(t, ascache.Undefined, arm.GetType(),
			"an arm reporting Undefined would be unaddressable by any bandit")
		require.False(t, seen[arm.GetType()],
			"two arms report %s; the cache rejects the whole set", arm.GetType())
		seen[arm.GetType()] = true
	}

	// The constructor is the real check: it is what rejects a duplicate.
	cache, err := ascache.NewAdaptiveCache(arms, bandit.NewThompson(0.9, 1), &ascache.Settings{
		EpochRequests:     64,
		MigrationStrategy: ascache.MigrationWarm,
	})
	require.NoError(t, err, "the full arm set must build an AdaptiveCache")
	t.Cleanup(func() { _ = cache.Close() })

	for i := 0; i < size*8; i++ {
		key := "k" + strconv.Itoa(i%(size*2))
		if _, ok := cache.Get(key); !ok {
			cache.Add(key, i)
		}
	}

	// Deliberately a loose bound. W-TinyLFU is in this set, and otter admits on
	// the calling goroutine while evicting on a maintenance pass, so whenever
	// that arm is the active one the cache sits above its limit until
	// maintenance catches up - documented in docs/policies.md, and measured at
	// up to 1.22x there. Capacity honesty has its own test; this one is about
	// the wiring, and only needs to catch a cache that is not bounded at all.
	assert.LessOrEqual(t, cache.Len(), size*3/2,
		"the cache must stay bounded by roughly its capacity, whichever arm is active")
	assert.NotEqual(t, ascache.Undefined, cache.ActivePolicy(),
		"the cache must be serving from a real policy after a replay")
}

// TestLoadMSRTraceCoversEveryBlockAByteRangeTouches guards the arithmetic that
// turns a byte range into block accesses.
//
// Counting ceil(Size/BlockSize) blocks from Offset/BlockSize is right only when
// the offset is block-aligned. At the default 512-byte block size every offset
// in these traces is, which is why the default agrees with Caffeine's reader -
// but BlockSize is a caller-settable knob with no alignment guarantee, and the
// simpler form silently drops the last block of every unaligned request.
func TestLoadMSRTraceCoversEveryBlockAByteRangeTouches(t *testing.T) {
	// 7014912 is 4096*1712 + 2560, so a 4096-byte read from there runs into
	// block 1713. Counting from the length alone stops at 1712.
	path := writeFixture(t, "msr.csv", "1,hm,0,Read,7014912,4096,1\n")

	w, err := bench.LoadMSRTrace(path, bench.MSRFormat{BlockSize: 4096}, 0)
	require.NoError(t, err)

	assert.Equal(t, []string{"hm:0:1712", "hm:0:1713"}, w.Keys,
		"an unaligned read must count every block it touches")
}

// TestLoadMSRTraceSeesReuseBetweenOverlappingRequests is the same defect seen
// from the side that matters for a cache: two requests touching one block must
// produce reuse, or the workload understates the locality it was traced from.
func TestLoadMSRTraceSeesReuseBetweenOverlappingRequests(t *testing.T) {
	path := writeFixture(t, "msr.csv",
		"1,hm,0,Read,256,512,1\n"+
			"2,hm,0,Read,512,512,1\n")

	w, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 0)
	require.NoError(t, err)

	assert.Equal(t, []string{"hm:0:0", "hm:0:1", "hm:0:1"}, w.Keys,
		"both requests touch block 1, so the trace contains reuse the loader must not lose")
}

// TestLoadMSRTraceCapsOneRecordsExpansion covers the failure mode where a
// single corrupt length takes over the whole workload: unbounded, one record
// claiming a terabyte allocates until the process dies, and under a limit it
// silently consumes the entire request budget so every policy is compared on
// one record's block range.
func TestLoadMSRTraceCapsOneRecordsExpansion(t *testing.T) {
	path := writeFixture(t, "msr.csv",
		"1,hm,0,Read,0,512,1\n"+
			"2,hm,0,Read,0,18446744073709551000,1\n")

	w, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 0)
	require.NoError(t, err)

	assert.Less(t, w.Len(), 1<<17,
		"one record must not be able to size the entire workload")
	assert.Equal(t, "hm:0:0", w.Keys[0], "the good record must still be there")
}

// TestLoadMSRTraceRequiresEveryColumn covers the truncated tail of a partial
// download. The guard has to be the exact column count: a cut past the size
// column still leaves parseable numbers, so a looser check replays a phantom
// block range, and a cut inside the type turns "Write" into something that a
// deny-list would admit as a read.
func TestLoadMSRTraceRequiresEveryColumn(t *testing.T) {
	for name, tail := range map[string]string{
		"cut inside size": "2,hm,0,Read,1074696192,40",
		"cut inside type": "2,hm,0,Wri,4096,512,1",
		"cut inside time": "2,hm,0,Read,1074696",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFixture(t, "msr.csv", "1,hm,0,Read,0,512,1\n"+tail)

			w, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 0)
			require.NoError(t, err)

			assert.Equal(t, []string{"hm:0:0"}, w.Keys,
				"only the one complete record may be replayed")
		})
	}
}

// TestLoadMSRTraceCountsOnlyRecognisedTypes checks the type filter is an
// allow-list. Treating everything that is not a Write as a read admits every
// unrecognised or garbled type into a workload labelled "reads only".
func TestLoadMSRTraceCountsOnlyRecognisedTypes(t *testing.T) {
	path := writeFixture(t, "msr.csv",
		"1,hm,0,Read,0,512,1\n"+
			"2,hm,0,Flush,0,512,1\n"+
			"3,hm,0,,0,512,1\n"+
			"4,hm,0,WriteBack,0,512,1\n")

	w, err := bench.LoadMSRTrace(path, bench.MSRFormat{}, 0)
	require.NoError(t, err)

	assert.Equal(t, []string{"hm:0:0"}, w.Keys,
		"Flush, an empty type and WriteBack are none of them reads")
	assert.Contains(t, w.Description, "1 records")
}

// TestLoadMetaKVTraceRequiresOpCount covers the quietest failure this loader
// has: a header without op_count parses, every row expands to one request, and
// the description cheerfully reports that it expanded. Same keys, a fraction of
// the requests, most of the reuse gone, no error.
func TestLoadMetaKVTraceRequiresOpCount(t *testing.T) {
	path := writeFixture(t, "kvcache.csv",
		"key,op,size,key_size\n"+
			"1668757755,GET,82,40\n"+
			"1665497896,GET,169,78\n")

	_, err := bench.LoadMetaKVTrace(path, bench.MetaKVFormat{}, 0)

	require.Error(t, err, "a trace with no op_count column cannot be expanded and must be refused")
	assert.Contains(t, err.Error(), "op_count")
}

// TestLoadMetaKVTraceSkipsRowsWithAnUnreadableOpCount checks a corrupt count is
// not quietly read as one request, which would invent a request the trace does
// not contain and hide the corruption behind a plausible number.
func TestLoadMetaKVTraceSkipsRowsWithAnUnreadableOpCount(t *testing.T) {
	path := writeFixture(t, "kvcache.csv",
		"key,op,size,op_count,key_size\n"+
			"a,GET,1,2,1\n"+
			"b,GET,1,0,1\n"+
			"c,GET,1,-5,1\n"+
			"d,GET,1,notanumber,1\n")

	w, err := bench.LoadMetaKVTrace(path, bench.MetaKVFormat{}, 0)
	require.NoError(t, err)

	assert.Equal(t, []string{"a", "a"}, w.Keys, "only the one readable row may be replayed")
	assert.Contains(t, w.Description, "3 rows had an unreadable op_count")
}

// TestLoadersReturnWhatTheyReadFromATruncatedGzip is the property both loaders
// document. These files are fetched as byte ranges, so the stream stops
// mid-record; a gzip cut mid-block surfaces as a scanner error, and failing the
// whole load would throw away every record already read and turn a usable
// partial download into an empty workload.
func TestLoadersReturnWhatTheyReadFromATruncatedGzip(t *testing.T) {
	truncate := func(t *testing.T, name, content string) string {
		t.Helper()

		full := writeGzipFixture(t, name, content)
		raw, err := os.ReadFile(full)
		require.NoError(t, err)

		cut := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(cut, raw[:len(raw)-12], 0o600))

		return cut
	}

	t.Run("msr", func(t *testing.T) {
		body := ""
		for i := 0; i < 400; i++ {
			body += "1,hm,0,Read," + strconv.Itoa(i*512) + ",512,1\n"
		}

		w, err := bench.LoadMSRTrace(truncate(t, "msr.csv.gz", body), bench.MSRFormat{}, 0)

		require.NoError(t, err, "a truncated download must yield the records it did contain")
		assert.Positive(t, w.Len())
	})

	t.Run("meta", func(t *testing.T) {
		body := "key,op,size,op_count,key_size\n"
		for i := 0; i < 400; i++ {
			body += "k" + strconv.Itoa(i) + ",GET,1,1,1\n"
		}

		w, err := bench.LoadMetaKVTrace(truncate(t, "kvcache.csv.gz", body), bench.MetaKVFormat{}, 0)

		require.NoError(t, err, "a truncated download must yield the rows it did contain")
		assert.Positive(t, w.Len())
	})
}
