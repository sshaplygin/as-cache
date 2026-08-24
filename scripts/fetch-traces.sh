#!/usr/bin/env bash
# Fetch real cache traces for the evidence harness.
#
# Traces are NOT committed to this repository: they are large, and most carry
# licences that do not grant redistribution. This downloads them to a local
# directory and prints the environment variable the harness reads.
#
# Usage:
#   ./scripts/fetch-traces.sh [target-dir]     # default: ./traces (gitignored)
#   AS_CACHE_TRACES=$(pwd)/traces make evidence
set -euo pipefail

TRACES="${1:-$(pwd)/traces}"
mkdir -p "$TRACES"

fetch() {
	local name="$1" url="$2"
	if [ -s "$TRACES/$name" ]; then
		echo "  have  $name"
		return
	fi
	echo "  get   $name"
	# --fail so an HTML error page is never mistaken for trace data.
	curl -fSL --retry 3 -o "$TRACES/$name" "$url"
}

echo "Fetching traces into $TRACES"

# --- Twitter Twemcache, CC BY 4.0 -------------------------------------------
# A real production in-memory key-value workload, which is this library's
# actual target domain rather than block I/O. ~1M requests, string keys.
# Cite: Yang, Yue & Rashmi, "A Large Scale Analysis of Hundreds of In-memory
# Cache Clusters at Twitter", OSDI '20.
fetch twitter_cluster052.csv \
	https://raw.githubusercontent.com/twitter/cache-trace/master/samples/2020Mar/cluster052

# --- LIRS research traces ---------------------------------------------------
# Tiny and deliberately adversarial. `loop` is a cyclic scan that defeats LRU
# outright, which is the clearest demonstration of why policy choice matters.
# No explicit licence: benchmark against them and cite, but do not vendor.
# Cite: Jiang & Zhang, "LIRS", SIGMETRICS '02.
LIRS=https://raw.githubusercontent.com/ben-manes/caffeine/master/simulator/src/main/resources/com/github/benmanes/caffeine/cache/simulator/parser/lirs
for f in loop 2_pools multi2; do
	fetch "lirs_$f.trace.gz" "$LIRS/$f.trace.gz"
done

# --- ARC paper traces -------------------------------------------------------
# The traces the ARC paper reported on, so numbers here are comparable with the
# literature. Each record expands into blockCount consecutive accesses - see
# LoadARCTrace. The canonical IBM host is long dead; these are mirrored in
# otter's benchmark suite.
# Cite: Megiddo & Modha, "ARC", FAST '03.
ARC=https://raw.githubusercontent.com/maypok86/otter/main/benchmarks/simulator/trace/arc
for f in p3 oltp; do
	fetch "arc_$f.gz" "$ARC/$f.gz"
done

# --- Meta kvcache, from the CacheBench workload bucket ----------------------
# A production key-value cache trace: five consecutive days across a 500-host
# cluster. Same domain as the Twitter trace and an order of magnitude larger,
# which is what makes it worth having in addition.
#
# The published file is 4.9 GB, so only its first slice is fetched. The bucket
# serves range requests over plain HTTPS, so no AWS credentials or CLI are
# needed. Override the size with AS_CACHE_META_BYTES.
#
# The slice ends mid-line; LoadMetaKVTrace skips the truncated last row.
# Note the op_count column: a row stands for that many requests, and the loader
# expands it. See docs/benchmarking.md.
# Cite: Meta CacheLib, https://cachelib.org/docs/Cache_Library_User_Guides/Cachebench_FB_HW_eval/
META_BYTES="${AS_CACHE_META_BYTES:-134217728}"
META=https://cachelib-workload-sharing.s3.amazonaws.com/pub/kvcache/202206/kvcache_traces_1.csv
if [ -s "$TRACES/meta_kvcache_202206_1.csv" ]; then
	echo "  have  meta_kvcache_202206_1.csv"
else
	echo "  get   meta_kvcache_202206_1.csv (first $META_BYTES bytes of 4.9 GB)"
	curl -fSL --retry 3 -H "Range: bytes=0-$((META_BYTES - 1))" \
		-o "$TRACES/meta_kvcache_202206_1.csv" "$META"
fi

# --- MSR Cambridge block I/O, from the SNIA IOTTA repository -----------------
# Thirteen enterprise servers traced for a week: the block-cache counterpart to
# the key-value traces above, and the trace set the S3-FIFO paper leans on for
# its scan and loop patterns.
#
# This one cannot be scripted end to end. SNIA serves the files behind a
# click-through licence and a cookie check, so the fetch below usually returns
# an error page rather than data - which is why it is guarded and skipped
# rather than allowed to fail the script.
#
# To get them by hand: open https://iotta.snia.org/traces/block-io?only=388,
# accept the SNIA Trace Data Files Download License, download one or more
# per-volume CSVs (hm_0, prn_0, proj_0, src1_2, usr_0, web_0 and the rest),
# and drop them into this directory named msr_<volume>.csv[.gz].
# Cite: Narayanan, Donnelly & Rowstron, "Write Off-Loading", FAST '08.
MSR_LIST=$(find "$TRACES" -name 'msr_*.csv*' 2>/dev/null | head -1)
if [ -n "$MSR_LIST" ]; then
	echo "  have  $(basename "$MSR_LIST") (and any siblings)"
else
	echo "  skip  msr_*.csv - see the note in this script; SNIA needs a browser"
fi

echo
echo "Done. Run the evidence harness with:"
echo "  AS_CACHE_TRACES=$TRACES make evidence"
