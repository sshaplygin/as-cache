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
  deliberately measures this variation over five replays.
- **Keep TTL longer than a replay.** Request-counted epochs do not change
  wall-clock expiry; the suite uses a one-hour TTL to measure its LRU behavior.
- **Every arm must be deterministic.** LRU, LFU, 2Q, S3-FIFO and SIEVE are.
  **Random is not**, despite being the simplest arm here: it seeds itself from
  the global source at construction, so three identical replays served 44, 44
  and 40 hits over 20,000 requests. It is in `DefaultArms` as the control arm,
  so replays through `benchclient` are reproducible up to that arm rather than
  exactly — and note that Random is not always the weak arm it looks like: on
  a cyclic workload it serves 82.2% where LRU and LFU serve 0.00%, so the
  workloads where the bandit would actually pick it are the ones that inherit
  its jitter.
  **W-TinyLFU is not**: otter evicts asynchronously and reports an approximate
  size, so replaying one trace three times against it directly gave three
  different hit counts and left 527, 504 and 545 entries in a cache with a
  capacity of 500. One unstable arm moves which policy the bandit picks, and
  with it the whole replay.

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
LFU, 2Q, Random and S3-FIFO — all deterministic except `Random`, which is
noted above.
`ArmsWithWindowTinyLFU`
adds the strongest arm and gives up repeatability to do it — that trade is
yours to make explicitly, which is why it is a second function rather than an
option. ARC is absent for the [patent reason](policies.md#arc-is-a-separate-module);
a harness that wants it can supply its own `Arms`.

## The repository's own suite

`make evidence` replays a suite of deterministic workloads against every policy,
against the adaptive cache, and against competing Go cache libraries. The
generators are in [bench/workload.go](../bench/workload.go) and the results are
written up in [evidence](evidence.md).

`./scripts/fetch-traces.sh` downloads published traces (nothing is committed),
after which `AS_CACHE_TRACES=... make evidence` replays those too.

Evidence tests are guarded by `testing.Short()` and excluded from `make test`.
Under `-race` epoch pacing changes by roughly 15x and the measurements become
meaningless, so run them through `make evidence` rather than `go test -race`.

The trace *loaders*, by contrast, are ordinary tests and do run in `make test`.
A format misread is a correctness bug, not evidence: it produces a workload
that looks entirely plausible and quietly invalidates every number taken from
it. They are pinned against fixtures copied from the real files in
[bench/trace_formats_test.go](../bench/trace_formats_test.go).

## Saved baseline

The [2026-09-27 artifact](../bench/results/2026-09-27/) retains all twelve
trace results, the full test log and provenance for the measured source revision.
The trace matrix uses nine arms, sampling 0.05, warm migration and request-counted
epochs at 10/20/50 requested epochs per trace. Random, W-TinyLFU and adaptive
selection each run five times; deterministic fixed arms run once. Results are
median [min-max], not confidence intervals. Request-counted epochs do not make
this sampled, asynchronous experiment deterministic.

Save a fresh matrix alongside the complete output:

```sh
AS_CACHE_TRACES="$PWD/traces" make verify-ref
AS_CACHE_TRACES="$PWD/traces" AS_CACHE_EVIDENCE_OUT="$PWD/traces.json" make evidence > evidence.log 2>&1
```

The JSON names the measured commit, tracked-tree state, platform, settings and
every hit-rate observation. The retained baseline adds input checksums and
libCacheSim provenance in its manifest. `make evidence` skips unavailable trace
files, so verify that a new artifact includes all expected traces before
publishing it. The twelve-trace baseline did; its LRU calibration covered all
sixty capacity points. The complete suite also includes synthetic experiments
and slower wall-clock tuning runs, separate from the request-counted matrix.

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
ends mid-line and the loader skips the truncated row. `AS_CACHE_META_BYTES`
sets the size; the default of 128 MiB is about 5M rows.

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
   file and following the loader's documented rules, so a loader bug cannot
   cancel itself out.
2. [libCacheSim](https://github.com/1a1a11a/libCacheSim) replays that through
   LRU, object sizes ignored, at 0.25, 0.5, 1, 2 and 4 times the capacity the
   evidence suite uses. The script builds it into `.tools/` at a pinned commit,
   `1d7415569978330ea95c9cff06a260630406f7e3`; on macOS that needs
   `brew install glib argp-standalone zstd cmake pkg-config`.
   `AS_CACHE_LIBCACHESIM` points it at an existing checkout instead.
3. `TestLRUMatchesReference` loads the same files through the Go loaders,
   replays this repository's LRU at the same capacities, and requires the same
   request count and a miss ratio within 0.5 percentage points.

The gate fails when it cannot run: libCacheSim that will not build, a trace
missing from the directory, or the Go test skipping. On all twelve traces at
all five capacities, 60 points, the largest difference is 0.005 points, which
is the rounding of cachesim's four-decimal output, and every request count
matches.

It checks LRU and the loaders, nothing more. Agreement says the workloads and
the counting are right; it says nothing about any other policy or about the
adaptive cache.
