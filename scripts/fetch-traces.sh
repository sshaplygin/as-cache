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

echo
echo "Done. Run the evidence harness with:"
echo "  AS_CACHE_TRACES=$TRACES make evidence"
