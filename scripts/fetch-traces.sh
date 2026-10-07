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
set -Eeuo pipefail
PENDING_PART=""
on_error() {
    local result="$1" failure_line="$2" callers="$3"
    if [ -n "$PENDING_PART" ]; then
        rm -f "$PENDING_PART"
    fi
    echo "FAIL: ${BASH_SOURCE[0]}:$failure_line exited with status $result (caller lines: $callers)" >&2
    exit "$result"
}
trap 'on_error "$?" "$LINENO" "${BASH_LINENO[*]}"' ERR
ROOT=$(cd "$(dirname "$0")/.." && pwd)
verify() {
    python3 "$ROOT/scripts/trace_inputs.py" "$TRACES" "$1" --path "$2"
}

TRACES="${1:-$(pwd)/traces}"
mkdir -p "$TRACES"

fetch() {
    local name="$1" url="$2"
    if [ -e "$TRACES/$name" ]; then
        verify "$name" "$TRACES/$name"
        echo "  have  $name"
        return
    fi
    echo "  get   $name"
    # --fail so an HTML error page is never mistaken for trace data.
    PENDING_PART="$TRACES/$name.part"
    curl -fSL --retry 3 -o "$PENDING_PART" "$url"
    verify "$name" "$TRACES/$name.part"
    mv "$TRACES/$name.part" "$TRACES/$name"
    PENDING_PART=""
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
# needed. The pinned size and checksum live in scripts/trace-inputs.json.
#
# The slice ends mid-line; LoadMetaKVTrace skips the truncated last row.
# Note the op_count column: a row stands for that many requests, and the loader
# expands it. See docs/benchmarking.md.
# Cite: Meta CacheLib, https://cachelib.org/docs/Cache_Library_User_Guides/Cachebench_FB_HW_eval/
META_BYTES="${AS_CACHE_META_BYTES:-134217728}"
[ "$META_BYTES" = 134217728 ] || { echo "FAIL: Meta slice must match scripts/trace-inputs.json (134217728 bytes)" >&2; exit 1; }
META=https://cachelib-workload-sharing.s3.amazonaws.com/pub/kvcache/202206/kvcache_traces_1.csv
if [ -e "$TRACES/meta_kvcache_202206_1.csv" ]; then
    verify meta_kvcache_202206_1.csv "$TRACES/meta_kvcache_202206_1.csv"
    echo "  have  meta_kvcache_202206_1.csv"
else
    echo "  get   meta_kvcache_202206_1.csv (first $META_BYTES bytes of 4.9 GB)"
    PENDING_PART="$TRACES/meta_kvcache_202206_1.csv.part"
    curl -fSL --retry 3 -H "Range: bytes=0-$((META_BYTES - 1))" \
        -o "$TRACES/meta_kvcache_202206_1.csv.part" "$META"
    verify meta_kvcache_202206_1.csv "$TRACES/meta_kvcache_202206_1.csv.part"
    mv "$TRACES/meta_kvcache_202206_1.csv.part" "$TRACES/meta_kvcache_202206_1.csv"
    PENDING_PART=""
fi

# --- MSR Cambridge block I/O, SNIA IOTTA trace 388 ---------------------------
# Thirteen enterprise servers traced for a week: the block-cache counterpart to
# the key-value traces above, and the trace set the S3-FIFO paper leans on for
# its scan and loop patterns.
#
# The canonical source is https://iotta.snia.org/traces/block-io/388, but it
# serves files only through a browser form (cookies, name, affiliation, email)
# and did not respond at all when this was written. The files come instead from
# the mirror kept by the cacheMon project (https://github.com/cacheMon/cache_dataset),
# which holds SNIA's original archives, msr-cambridge1.tar and
# msr-cambridge2.tar. The SNIA Trace Data Files Download License (v2.0) permits
# use and redistribution without restriction, so the mirror is a lawful copy.
#
# The archives are 3.3 GB and 2.0 GB, uncompressed tars of per-volume .csv.gz
# files, and S3 serves byte ranges, so only the volumes listed below are
# fetched: about 210 MB in all. Each entry pins where the volume sits in its
# archive and the MD5 the archive's own MD5.txt gives for it. A download that
# does not match fails the script rather than replaying different data under a
# familiar name. If the mirror is repacked or gone, the tar-header check or the
# checksum says so; fall back to SNIA by hand and name the files
# msr_<volume>.csv.gz.
# Cite: Narayanan, Donnelly & Rowstron, "Write Off-Loading", FAST '08.
MSR_MIRROR=https://cache-datasets.s3.amazonaws.com/cache_dataset_txt/2008_msr

# volume, archive, offset of its tar header, size in bytes, MD5 from MD5.txt
MSR_VOLUMES=(
    "hm_0   msr-cambridge1.tar       7168 41967571 e8a4059b21e91921256f737df3e0e5c9"
    "prn_0  msr-cambridge1.tar   79446528 44469556 d6402a3a42063dabbf940dbf27f14219"
    "proj_0 msr-cambridge1.tar  249739264 54999265 523b81261912744d33e70be92ae699e1"
    "src1_2 msr-cambridge2.tar 1089042432 21339692 55fb3869c8e9e3ff4a31d88e8aea4e7e"
    "usr_0  msr-cambridge2.tar 1213713920 25999401 e5478f9ca3d247b3b995cf3ff029c9d8"
    "web_0  msr-cambridge2.tar 1933530624 24066938 b7cbd5bdb352b49eb33a0029111111ae"
)

md5_of() {
    if command -v md5sum >/dev/null 2>&1; then
        md5sum "$1" | cut -d' ' -f1
    else
        md5 -q "$1"
    fi
}

fetch_msr() {
    local volume="$1" archive="$2" header="$3" size="$4" want="$5"
    local name="msr_$volume.csv.gz"
    local out="$TRACES/$name" url="$MSR_MIRROR/$archive"
    if [ -e "$out" ]; then
        verify "$name" "$out"
        echo "  have  $name"
        return
    fi

    # The first 100 bytes of a tar header are the member's name. Checking it
    # before downloading turns a repacked archive into a clear error instead of
    # tens of megabytes of the wrong volume.
    local member
    member=$(curl -fsSL --retry 3 -r "$header-$((header + 99))" "$url" | tr -d '\0')
    if [ "$member" != "MSR-Cambridge/$volume.csv.gz" ]; then
        echo "  FAIL  $name: $archive holds '$member' at byte $header; the mirror has changed" >&2
        return 1
    fi

    echo "  get   $name ($((size / 1048576)) MB from $archive)"
    local start=$((header + 512))
    PENDING_PART="$out.part"
    curl -fSL --retry 3 -r "$start-$((start + size - 1))" -o "$PENDING_PART" "$url"

    local got
    got=$(md5_of "$out.part")
    if [ "$got" != "$want" ]; then
        echo "  FAIL  $name: MD5 $got, expected $want from the archive's MD5.txt" >&2
        rm -f "$out.part"
        return 1
    fi
    verify "$name" "$out.part"
    mv "$out.part" "$out"
    PENDING_PART=""
}

for entry in "${MSR_VOLUMES[@]}"; do
    # shellcheck disable=SC2086 # the entry is split into its fields on purpose
    fetch_msr $entry
done

echo
echo "Done. Run the evidence harness with:"
echo "  AS_CACHE_TRACES=$TRACES make evidence"
