#!/usr/bin/env bash
# Calibrate this repository's trace loaders and LRU against libCacheSim.
#
# Every published number from a real trace rests on two things that no unit
# test can vouch for: that a loader turned the file into the right request
# sequence, and that a replay counts hits the way everyone else does. This
# checks both against an independent implementation, on every trace the
# evidence suite reads:
#
#   1. Each trace is expanded into one key per request here, with awk, from the
#      raw file - not through the Go loaders, so a loader bug cannot cancel out.
#   2. libCacheSim's cachesim replays it through LRU at five capacities around
#      the one the suite uses, ignoring object sizes.
#   3. TestLRUMatchesReference loads the same files through the Go loaders,
#      replays this repository's LRU at the same capacities, and requires the
#      same request count and a miss ratio within 0.5 percentage points.
#
# The gate fails when it cannot run - libCacheSim missing and not buildable, a
# trace absent, the test skipped - rather than reporting success over nothing.
#
# Usage:
#   AS_CACHE_TRACES=$(pwd)/traces ./scripts/verify-ref.sh
#   AS_CACHE_LIBCACHESIM=/path/to/libCacheSim   # reuse an existing checkout
set -Eeuo pipefail

# Under set -e a failing command ends the script with no word of why. A gate
# that stops silently reads like one that finished, so say where it stopped.
trap 'echo "FAIL: ${BASH_SOURCE[0]}:$LINENO exited with status $?" >&2' ERR

# Pinned: a reference that moves is not a reference. Record this commit next to
# any result that cites the gate.
LIBCACHESIM_REPO=https://github.com/1a1a11a/libCacheSim.git
LIBCACHESIM_COMMIT=1d7415569978330ea95c9cff06a260630406f7e3

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TRACES="${AS_CACHE_TRACES:?set AS_CACHE_TRACES to the directory ./scripts/fetch-traces.sh filled}"
LCS="${AS_CACHE_LIBCACHESIM:-$ROOT/.tools/libCacheSim}"
CACHESIM="$LCS/_build/bin/cachesim"

# Capacity multipliers around each trace's evidence capacity.
GRID=(0.25 0.5 1 2 4)

# file, expander, capacity the evidence suite uses (bench/trace_test.go)
TRACE_LIST=(
	"twitter_cluster052.csv    twitter 10000"
	"lirs_loop.trace.gz        lirs    500"
	"lirs_2_pools.trace.gz     lirs    1000"
	"arc_p3.gz                 arc     20000"
	"arc_oltp.gz               arc     20000"
	"meta_kvcache_202206_1.csv meta    10000"
	"msr_hm_0.csv.gz           msr     20000"
	"msr_prn_0.csv.gz          msr     20000"
	"msr_proj_0.csv.gz         msr     20000"
	"msr_src1_2.csv.gz         msr     20000"
	"msr_usr_0.csv.gz          msr     20000"
	"msr_web_0.csv.gz          msr     20000"
)

# The loaders cap how many requests a trace yields; the expansions must too.
LIMIT=2000000

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

build_libcachesim() {
	if [ -x "$CACHESIM" ]; then
		return
	fi
	echo "Building libCacheSim $LIBCACHESIM_COMMIT into $LCS"
	for tool in git cmake pkg-config cc; do
		command -v "$tool" >/dev/null 2>&1 || fail "$tool is required to build libCacheSim"
	done
	if [ ! -d "$LCS/.git" ]; then
		git clone --quiet "$LIBCACHESIM_REPO" "$LCS"
	fi
	git -C "$LCS" checkout --quiet "$LIBCACHESIM_COMMIT"
	# glib, argp and zstd are libCacheSim's own requirements; on macOS:
	#   brew install glib argp-standalone zstd cmake pkg-config
	cmake -S "$LCS" -B "$LCS/_build" -DCMAKE_BUILD_TYPE=Release >/dev/null ||
		fail "cmake could not configure libCacheSim; see its scripts/install_dependency.sh"
	cmake --build "$LCS/_build" --target cachesim -j >/dev/null ||
		fail "libCacheSim did not build"
}

# decompress streams a .gz file to a reader that may stop early. The expansions
# exit once they reach LIMIT, which kills gzip with SIGPIPE (status 141); under
# pipefail that would end the script midway through a trace. Any other failure
# still fails.
decompress() {
	gzip -dc "$1" || [ "$?" -eq 141 ]
}

# expand <kind> <file>: one key per line on stdout, matching what the
# corresponding Go loader yields for the evidence suite.
expand() {
	case "$1" in
	twitter) # LoadTrace(TwitterFormat): comma-separated, key in column 1
		awk -F, -v lim="$LIMIT" 'NF >= 2 { k = $2; gsub(/^[ \t]+|[ \t]+$/, "", k); if (k == "") next; print k; if (++n >= lim) exit }' "$2" ;;
	lirs) # LoadTrace(LIRSFormat): first whitespace field; lines starting with * are markers
		decompress "$2" | awk -v lim="$LIMIT" 'index($0, "*") == 1 || NF == 0 { next } { print $1; if (++n >= lim) exit }' ;;
	arc) # LoadARCTrace: "start count ..." stands for count consecutive blocks
		decompress "$2" | awk -v lim="$LIMIT" 'NF >= 2 && $1 ~ /^[0-9]+$/ && $2 ~ /^[0-9]+$/ { for (i = 0; i < $2; i++) { print $1 + i; if (++n >= lim) exit } }' ;;
	meta) # LoadMetaKVTrace: GET* rows only, columns by header name, op_count repeats, capped at 65536
		awk -F, -v lim="$LIMIT" 'NR == 1 { for (i = 1; i <= NF; i++) col[$i] = i; next }
			{ k = $col["key"]; c = $col["op_count"]; if (k == "" || c !~ /^[0-9]+$/ || toupper(substr($col["op"], 1, 3)) != "GET") next
			  if (c > 65536) c = 65536
			  for (i = 0; i < c; i++) { print k; if (++n >= lim) exit } }' "$2" ;;
	msr) # LoadMSRTrace: reads only, host:disk:block over every 512-byte block the range touches, capped at 65536
		decompress "$2" | awk -F, -v lim="$LIMIT" -v bs=512 'NF == 7 && $5 ~ /^ *[0-9]+ *$/ && $6 ~ /^ *[0-9]+ *$/ {
			t = tolower($4); gsub(/ /, "", t); if (t != "read") next
			off = $5 + 0; sz = $6 + 0; if (sz == 0) next
			s = int(off / bs); last = s + int((sz - 1 + off % bs) / bs); c = last - s + 1; if (c > 65536) c = 65536
			h = $2; d = $3; gsub(/ /, "", h); gsub(/ /, "", d)
			for (i = 0; i < c; i++) { print h ":" d ":" (s + i); if (++n >= lim) exit } }' ;;
	*) fail "unknown expander $1" ;;
	esac
}

build_libcachesim
[ "$(git -C "$LCS" rev-parse HEAD)" = "$LIBCACHESIM_COMMIT" ] ||
	fail "$LCS is not at the pinned commit $LIBCACHESIM_COMMIT"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
REFERENCE="$WORK/reference.tsv"
: >"$REFERENCE"

echo "libCacheSim $LIBCACHESIM_COMMIT, LRU, object sizes ignored"
for entry in "${TRACE_LIST[@]}"; do
	read -r file kind capacity <<<"$entry"
	[ -s "$TRACES/$file" ] || fail "$file is absent from $TRACES; run ./scripts/fetch-traces.sh"

	expand "$kind" "$TRACES/$file" >"$WORK/keys.txt"
	for m in "${GRID[@]}"; do
		size=$(awk -v c="$capacity" -v m="$m" 'BEGIN { printf "%d", c * m }')
		# cachesim writes a result directory into its working directory.
		line=$(cd "$WORK" && "$CACHESIM" "$WORK/keys.txt" txt lru "$size" \
			--ignore-obj-size true --num-thread 1 2>/dev/null | grep "miss ratio") ||
			fail "cachesim produced no result for $file at $size"
		requests=$(sed -E 's/.*, +([0-9]+) req.*/\1/' <<<"$line")
		miss=$(sed -E 's/.*miss ratio ([0-9.]+).*/\1/' <<<"$line")
		printf "%s\t%s\t%s\t%s\n" "$file" "$size" "$requests" "$miss" >>"$REFERENCE"
		printf "  %-26s %6s  %8s requests  miss %s\n" "$file" "$size" "$requests" "$miss"
	done
done

echo
echo "Replaying the same traces through the Go loaders and LRU"
out=$(cd "$ROOT/bench" && AS_CACHE_TRACES="$TRACES" AS_CACHE_LRU_REFERENCE="$REFERENCE" \
	go test -count=1 -run '^TestLRUMatchesReference$' -v . 2>&1) || {
	echo "$out"
	fail "the Go replay does not match libCacheSim"
}
echo "$out" | grep -E '^\s+reference_test.go' || true
grep -q -- '--- PASS: TestLRUMatchesReference' <<<"$out" || {
	echo "$out"
	fail "TestLRUMatchesReference did not run to a pass"
}

echo
echo "Reference gate passed: $(wc -l <"$REFERENCE" | tr -d ' ') points, libCacheSim $LIBCACHESIM_COMMIT."
