# Benchmarking and reproducible replays

## Reproducible replays

`EpochDuration` measures on a wall clock. A trace replay can therefore
re-evaluate a different number of times when the machine is busy.
`EpochRequests` ends an epoch every N `Get` calls instead, fixing the request
boundaries independently of replay speed. Other sources of variation remain.

```go
&ascache.Settings{EpochRequests: 10_000} // no EpochDuration: no wall clock at all
```

`Get` is the unit because `Get` is where hits and misses are recorded, so this
counts exactly the requests the bandit is shown. A write-only workload never
ends an epoch, which is correct — there is nothing to compare policies on. The
epoch runs on whichever goroutine makes the Nth `Get`, so that call pays for
the switch and any migration. `EpochDuration` performs that work on a
background goroutine instead; it changes the timing model of the experiment.
Setting both applies both.

Exact replay also requires the following:

- **Seed the bandit.** `bandit.NewThompson(discount, seed)` takes one.
- **Disable random key sampling for exact replay.** `ShadowSampleRate: 0`
  uses full-size shadows. With sampling enabled, each cache gets a fresh hash
  seed, which is not controlled by the bandit's seed. The real-trace matrix
  deliberately measures this variation over three batches of five replays.
- **Keep TTL longer than a replay.** Request-counted epochs do not change
  wall-clock expiry; the suite uses a one-hour TTL to measure its LRU behavior.
- **Every arm must be deterministic for exact replay.** Random seeds itself
  at construction and W-TinyLFU evicts asynchronously. Its retained size can
  also exceed nominal capacity. Either arm makes the complete adaptive run
  nondeterministic even with fixed request epochs and a seeded bandit.
  Random is in `DefaultArms` as a control and can outperform LRU on cyclic
  traffic; it must not be dismissed as an always-weak policy.

## Benchmark harnesses

The `benchclient` module adapts the cache to the client contract used by Go
cache benchmark suites -- [maypok86/benchmarks](https://github.com/maypok86/benchmarks),
whose hit-ratio simulator and throughput harness both drive a cache through
`Init`/`Get`/`Set`/`Name`/`Close`:

```go
c := &benchclient.Cache[uint64, uint64]{}
c.Init(capacity)
defer c.Close()

if _, ok := c.Get(key); !ok {
    c.Set(key, value)
}
```

It imports nothing from any benchmark suite. The contract is five methods and
Go interfaces are structural, so the adapter satisfies it by shape alone --
which keeps a harness's dependency tree out of this one, and leaves the package
usable by anything wanting the same five methods.

It is configured for reproducibility rather than for the best number:
request-counted epochs, a seeded bandit, and no sampling. `DefaultArms` is LRU,
LFU, 2Q and Random — all deterministic except `Random`, which is
noted above.
`ArmsWithWindowTinyLFU`
adds another workload-dependent baseline and gives up repeatability to do it — that trade is
yours to make explicitly, which is why it is a second function rather than an
option. ARC is absent for the [patent reason](policies.md#arc-is-a-separate-module);
a harness that wants it can supply its own `Arms`.

## The repository's own suite

`AS_CACHE_TRACES=... make evidence` requires all thirteen files in
[scripts/trace-inputs.json](../scripts/trace-inputs.json), verifies their sizes
and hashes, then replays the synthetic and real-trace suites. Fetch them first
with `./scripts/fetch-traces.sh`. Missing inputs fail before any tests run.
The measured/reference matrix has twelve traces: `lirs_multi2.trace.gz` is part
of the downloaded and hashed inventory but is not replayed by this suite.

For synthetic diagnostics without downloaded traces, run:

```sh
(cd bench && env -u AS_CACHE_TRACES -u AS_CACHE_LRU_REFERENCE go test -count=1 -timeout 45m -v ./...)
```

That command skips the trace-dependent tests and cannot produce a publishable
complete dataset. The workload generators are in
[bench/workload.go](../bench/workload.go); the current interpretation is in
[evidence](evidence.md).

Evidence tests are guarded by `testing.Short()` and excluded from `make test`.
Under `-race` epoch pacing changes by roughly 15x and the measurements become
meaningless, so run them through `make evidence` rather than `go test -race`.

The trace *loaders*, by contrast, are ordinary tests and do run in `make test`.
A format misread is a correctness bug, not evidence: it produces a workload
that looks entirely plausible and quietly invalidates every number taken from
it. They are pinned against fixtures copied from the real files in
[bench/trace_formats_test.go](../bench/trace_formats_test.go).

## Saved baseline

The [current artifact](../bench/results/current/README.md) retains all twelve
traces, three full evidence logs and generated provenance for one clean commit.
The trace matrix uses nine arms, warm migration, requested sampling 0.05 and
minimum shadow capacity 64 (the library default is 256). Per-trace effective
rates and 10/20/50 request epochs are recorded explicitly.

Each batch runs Random, W-TinyLFU and each adaptive setting five times;
deterministic fixed arms run once. Three consecutive full batches provide
fifteen observations per nondeterministic cell. Every outcome is retained,
including comparisons below fixed baselines. Min/max are not confidence bounds.
The artifact also includes an ObserveOnly sweep, P3 tuning and a Meta size
comparison; see [evidence](evidence.md) for their different scopes.

To produce and verify a new current dataset from committed HEAD:

```sh
AS_CACHE_TRACES="$PWD/traces" python3 scripts/record_evidence.py --out /tmp/as-cache-current
python3 scripts/record_evidence.py --verify /tmp/as-cache-current
```

Use an empty output directory and run heavy measurements sequentially. The
recorder calibrates LRU, runs the object/byte comparison, executes `make evidence`
three times, merges the observations, generates the tables and hashes the
outputs. After review, replace `bench/results/current/`; do not retain past
iteration datasets. The recorder exports one resolved HEAD into a temporary,
private Git checkout and runs that commit's scripts there. Developer edits,
ignored tests, embeds and other untracked assets cannot enter the measurements.
The original workspace, including preserved local probes, is left untouched.
Commit changes before recording if they should be measured. The recorder controls the build environment: ambient Make, shell, Git and Go
configuration cannot inject source overrides. Tool paths, caches, locale and
network proxy settings remain available. The printed commit identifies the snapshot; routine local tests can still include developer files
that CI does not see. Final acceptance uses a clean committed checkout.

Output must be empty and either outside the repository or inside an ignored
directory; unsuitable output is rejected before recording starts. The recorder
rejects unexpected files introduced into its private snapshot, missing required
traces and failed commands. Failed commands
retain their log and exit code for diagnosis. The verifier requires every
artifact and measurement command and recomputes the pooled JSON from the raw
batches, checking settings, trace inventories and observation counts.

For one diagnostic batch, use `AS_CACHE_TRACES=... AS_CACHE_EVIDENCE_OUT=... make evidence`.
This is not a replacement for the three-batch publication procedure.

## Real traces

| Trace | Loader | Obtained by |
| --- | --- | --- |
| Twitter Twemcache | `LoadTrace(p, TwitterFormat, n)` | script |
| LIRS (`loop`, `2_pools`, `multi2`) | `LoadTrace(p, LIRSFormat, n)` | script |
| ARC paper (`p3`, `oltp`) | `LoadARCTrace(p, n)` | script |
| Meta kvcache | `LoadMetaKVTrace(p, MetaKVFormat{}, n)` | script, partial download |
| MSR Cambridge | `LoadMSRTrace(p, MSRFormat{}, n)` | script, six volumes from a mirror — see below |

Three of these layouts expand: **one record is not one request**, and reading
them as though it were produces a workload with the same keys, far fewer
requests and much less reuse than the traffic they were taken from.

- The **ARC** layout is `startBlock blockCount`, and stands for `blockCount`
  consecutive accesses.
- The **MSR** layout carries a byte offset and a byte length, and stands for as
  many block accesses as fit in the length. A 64 KiB read is 128 accesses at
  the default 512-byte block size, not one. `MSRFormat.BlockSize` changes that
  granularity, and therefore changes the workload — two runs at different block
  sizes cannot be compared. Reads only by default; `IncludeWrites` models a
  write-back cache instead, which is a different measurement.
- The **Meta kvcache** layout collapses runs of identical operations into one
  row and an `op_count`, which CacheBench replays that many times. On a 2M
  request slice of `kvcache/202206`, 942,355 read rows expand to 2,000,000
  requests. `GET` and `GET_LEASE` are the reads; `SET` and the rest are only
  replayed under `IncludeWrites`, and counting them by default would hand every
  policy a free hit per write, since almost every `SET` in these files is
  immediately followed by a `GET` of the same key.

The Meta files are 5 to 10 GB each, so the script fetches only the first slice
of one over a byte-range request — no AWS credentials or CLI needed. The slice
ends mid-line and the loader skips the truncated row. The 128 MiB prefix and
its SHA-256 are pinned in `scripts/trace-inputs.json`; a different slice needs
a deliberate catalog update, not an unchecked size override.

**MSR Cambridge comes from a mirror, not from SNIA.** The canonical source is
[SNIA IOTTA trace 388](https://iotta.snia.org/traces/block-io/388), which hands
files out only through a browser form (cookies, name, affiliation and email)
and did not respond at all in September 2026. The script takes the files from
the [cacheMon](https://github.com/cacheMon/cache_dataset) mirror instead, which
holds SNIA's original `msr-cambridge1.tar` and `msr-cambridge2.tar`. That is a
lawful copy: the SNIA Trace Data Files Download License (v2.0) permits use and
redistribution without restriction.

The two archives total 5.3 GB, so the script fetches six volumes by byte range
— `hm_0`, `prn_0`, `proj_0`, `src1_2`, `usr_0` and `web_0`, about 210 MB — and
checks each against the MD5 in the archive's own `MD5.txt`. Those checksums
travel inside the mirrored archive, so they catch a corrupted or repacked
download, not a mirror that altered the data deliberately; nobody here has
compared the mirror against a copy downloaded from SNIA.

Any file named `msr_<volume>.csv` (`.gz` is fine) in the trace directory is
picked up, so other volumes, or files you downloaded from SNIA yourself, need
no code change. Cite Narayanan, Donnelly and Rowstron, *Write Off-Loading*,
FAST '08, as the traces' README asks.

## Checking the loaders against libCacheSim

A loader that misreads a format produces a workload that looks entirely
plausible, and the fixtures above only prove that the loader reads the rows it
was tested on. `make verify-ref` checks the whole pipeline — loader, replay and
hit counting — against an independent implementation on every trace the
evidence suite reads:

```bash
AS_CACHE_TRACES=$(pwd)/traces make verify-ref
```

1. Each trace is expanded into one key per request by awk in
   [scripts/verify-ref.sh](../scripts/verify-ref.sh), straight from the raw
   file and following the loader's documented rules. `oracle_trace.py` exports
   the sequence in oracleGeneralBin format with unit sizes.
2. [libCacheSim](https://github.com/1a1a11a/libCacheSim) replays that through
   LRU, object sizes ignored, at 0.25, 0.5, 1, 2 and 4 times the capacity the
   evidence suite uses. The script builds it into `.tools/` at a pinned commit,
   `1d7415569978330ea95c9cff06a260630406f7e3`; on macOS that needs
   `brew install glib argp-standalone zstd cmake pkg-config`.
   `AS_CACHE_LIBCACHESIM` points it at an existing checkout instead.
3. `TestLRUMatchesReference` loads the same files through the Go loaders,
   replays this repository's LRU at the same capacities, and requires the same
   request count and a miss ratio within 0.0051 percentage points. The reference
   must contain finite ratios in [0, 1] with exactly four decimal places, the
   pinned simulator format. The bound is half its rounding quantum (0.005
   percentage points) plus 0.0001 numerical slack; coarser input is rejected.

The gate fails when it cannot run: libCacheSim that will not build, a trace
missing from the directory, or the Go test skipping. On all twelve traces at
all five capacities, 60 points, the largest difference is 0.005 points, which
is the rounding of cachesim's four-decimal output, and every request count
matches.

Agreement establishes that two independent expansions of the same interpretation
and their LRU hit counting agree within the simulator's rounding. It cannot
detect a shared misunderstanding of a source format, and says nothing about
other policies or automatic selection. Coverage is bidirectional: an additional
MSR volume makes the gate fail until it is included in the reference inventory.

The fetcher verifies recorded sizes and SHA-256 hashes of existing files on
every run; new downloads are verified as `.part` files before rename. Corrupt
cached files fail with their name. The complete evidence command verifies the
same catalog before loading any trace, so partial input sets cannot quietly
become a publication dataset.

`make evidence-check` verifies the retained artifact hashes, pooled observations,
and exact generated report without downloading traces or running measurements.
It is part of `make all` and CI. After committing a report-template change, use
`python3 scripts/record_evidence.py --render bench/results/current` to validate
the existing inputs and regenerate presentation; the manifest records the new
report-generator commit. Refresh executes the generator from an isolated export
of that commit, so Git index flags cannot substitute working-tree code while
claiming committed provenance. Strict verification compares exact UTF-8 report
bytes, including line endings, without modifying any artifact.
