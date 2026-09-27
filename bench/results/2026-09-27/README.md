# Trace baseline, 2026-09-27

Measured code: `0df604679a77bbed5a94c6a4bb65772f62a105cf`, clean tracked tree.
The nine policies include the experimental S3-FIFO and SIEVE adapters.
Their presence in this experiment does not mean the FIFO module is released.

## Files

- `traces.json`: every fixed-policy and adaptive hit-rate observation, with settings and measurement revision.
- `manifest.json`: input SHA-256 hashes, host/tool versions and exact commands. It inventories all 13 downloaded files; `lirs_multi2.trace.gz` was not used in the 12-trace matrix.
- `reference.log`: 60 LRU calibration points against libCacheSim at the pinned revision.
- `evidence.log`: complete successful `make evidence` output (1471.381 seconds); wall-clock experiments are separate from the matrix below.
- `reference.tsv`: those same simulator observations extracted from the log, readable by `TestLRUMatchesReference`.

No input traces are included. Trace hit rates count requests/entries, not bytes; MSR reads expand into 512-byte blocks. These results do not measure byte miss ratios or generalize a prefix to the complete original trace.

Both commands exited 0. The reference test is intentionally skipped inside `make evidence` without `AS_CACHE_LRU_REFERENCE`; it passed separately in `make verify-ref`.

## Method

The matrix contains 384 replays: seven deterministic fixed arms once per trace, Random and W-TinyLFU five times each, and the adaptive cache five times at each of 10/20/50 requested epochs. Adaptive settings use warm migration, sampling 0.05, minimum shadow capacity 64 and `bandit.NewThompson(0.7, 13)`. Exact epoch request counts and capacities are in the JSON.

Request-counted epochs remove scheduling from the epoch boundary. They do not make this nine-arm experiment deterministic: the shadow sampler gets a fresh random hash seed, Random is unseeded by the harness, and W-TinyLFU maintains its cache asynchronously and can exceed nominal capacity. The TTL is one hour, longer than an individual replay.

The table below is copied from the test output. Values are median [minimum-maximum]; parentheses are percentage-point differences from the best fixed median. These are five observed runs, not confidence intervals or a significance test. Tied winners are represented by one policy name according to the harness's alphabetical tie-break.

| Trace | Requests | Best fixed | Worst fixed | Adaptive, 10 epochs | Adaptive, 20 epochs | Adaptive, 50 epochs |
| --- | --- | --- | --- | --- | --- | --- |
| twitter_cluster052.csv | 1000000 | SIEVE 59.78% | LFU 41.44% | 58.60% [58.55-59.07] (-1.19) | 58.81% [58.66-59.13] (-0.97) | 58.17% [58.09-58.32] (-1.61) |
| lirs_loop.trace | 505500 | W-TinyLFU 43.72% [30.36-50.05] | 2Q 0.00% | 38.70% [37.32-41.82] (-5.01) | 43.19% [39.66-45.54] (-0.53) | 43.06% [39.02-47.98] (-0.66) |
| lirs_2_pools.trace | 100000 | W-TinyLFU 54.67% [54.51-54.70] | Random 49.93% [49.88-50.12] | 54.42% [54.37-54.42] (-0.25) | 54.41% [54.12-54.42] (-0.25) | 54.24% [54.11-54.28] (-0.43) |
| arc_p3 | 2000000 | W-TinyLFU 12.11% [11.28-12.39] | LRU 1.87% | 12.56% [12.06-12.78] (+0.46) | 12.77% [11.18-13.36] (+0.66) | 12.40% [11.92-13.36] (+0.29) |
| arc_oltp | 914145 | 2Q 68.25% | LFU 45.43% | 67.64% [67.37-67.75] (-0.61) | 66.92% [66.45-67.04] (-1.34) | 66.11% [65.94-66.25] (-2.15) |
| meta_kvcache_202206_1 | 2000000 | S3-FIFO 69.05% | Random 65.18% [65.17-65.20] | 67.96% [67.83-68.11] (-1.09) | 67.53% [67.50-67.61] (-1.52) | 66.81% [66.77-66.91] (-2.24) |
| msr_hm_0 | 2000000 | 2Q 17.01% | LRU 11.40% | 15.22% [14.39-15.77] (-1.79) | 13.31% [12.95-14.40] (-3.70) | 14.39% [13.79-15.77] (-2.61) |
| msr_prn_0 | 2000000 | LFU 1.06% | W-TinyLFU 0.78% [0.73-0.87] | 0.87% [0.84-0.87] (-0.19) | 0.84% [0.71-0.97] (-0.22) | 1.05% [0.73-1.08] (-0.00) |
| msr_proj_0 | 2000000 | S3-FIFO 5.79% | W-TinyLFU 4.35% [4.29-4.43] | 5.13% [5.13-5.14] (-0.66) | 5.10% [5.07-5.29] (-0.70) | 5.39% [5.36-5.58] (-0.40) |
| msr_src1_2 | 2000000 | 2Q 1.77% | W-TinyLFU 1.16% [0.94-1.18] | 1.70% (-0.06) | 1.70% [1.27-1.70] (-0.06) | 1.70% [1.67-1.70] (-0.07) |
| msr_usr_0 | 2000000 | S3-FIFO 3.58% | LFU 1.46% | 3.57% [3.00-3.57] (-0.01) | 3.50% [3.50-3.52] (-0.08) | 3.42% [3.41-3.66] (-0.17) |
| msr_web_0 | 2000000 | LFU 2.61% | W-TinyLFU 2.28% [2.26-2.33] | 2.35% [2.28-2.35] (-0.26) | 2.41% [2.41-2.41] (-0.20) | 2.44% [2.44-2.47] (-0.17) |

## What this run supports

- The best fixed policy depends on the trace; no single fixed policy wins throughout the matrix.
- Adaptive medians exceed the best fixed median only on ARC P3, at all three epoch lengths; the observed ranges overlap there. This is not evidence of a statistically established win.
- Adaptive medians trail the best fixed median on the other eleven traces. The largest observed median deficit is 5.01 percentage points on LIRS loop at 10 epochs.
- Epoch length matters. Reporting all three columns avoids choosing a favorable setting after seeing each trace.
- At the measured capacities, all 60 LRU reference points have matching request counts and miss-ratio differences no larger than 0.005 percentage points at the log's precision. This validates the checked loaders/LRU combinations, not the other eviction policies or the adaptive mechanism.

## Reproduction

From the repository root at the measured revision, with the files matching the manifest in `traces/`:

```sh
AS_CACHE_TRACES="$PWD/traces" make verify-ref
AS_CACHE_TRACES="$PWD/traces" AS_CACHE_EVIDENCE_OUT="$PWD/traces.json" make evidence
```

`make verify-ref` needs the documented libCacheSim build dependencies. The complete evidence command also runs synthetic, memory, sampling and wall-clock tuning experiments; those are separate from the request-counted matrix above.
