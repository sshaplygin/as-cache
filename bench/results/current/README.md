# Current measurement results

Measured source: `3331d5f6156c969b31bf70e438ebc8ca25b501b6`; clean committed trees; three consecutive full evidence runs.
Nondeterministic subjects have 15 observations (three batches of five). Deterministic fixed arms run once per batch.
Median [min–max] describes these observations, not a confidence interval or a bound on future runs.
All trace-matrix outcomes are retained; relative hit rates do not decide whether this matrix passes.

## Trace matrix

All nine arms; warm migration; request-counted epochs; configured sampling 5%, floor 64.
This experimental floor differs from the library default 256. Effective rates are in the context table.

| Trace | Best fixed median (ties retained) | Worst fixed median | 10 epochs | 20 epochs | 50 epochs |
| --- | --- | --- | --- | --- | --- |
| twitter_cluster052.csv | SIEVE 59.78% | 41.44% | 58.66% [58.59–58.75] (-1.12 pp) | 58.81% [58.63–58.91] (-0.98 pp) | 58.24% [57.95–58.52] (-1.55 pp) |
| lirs_loop.trace | W-TinyLFU 44.20% | 0.00% | 40.09% [35.75–42.96] (-4.12 pp) | 41.47% [38.44–44.00] (-2.73 pp) | 43.76% [41.06–45.34] (-0.44 pp) |
| lirs_2_pools.trace | W-TinyLFU 54.76% | 49.99% | 54.41% [54.38–54.46] (-0.35 pp) | 54.39% [54.15–54.42] (-0.37 pp) | 54.22% [53.97–54.35] (-0.54 pp) |
| arc_p3 | W-TinyLFU 11.86% | 1.87% | 12.37% [12.07–12.81] (+0.50 pp) | 12.92% [12.46–13.45] (+1.06 pp) | 12.36% [12.16–13.48] (+0.50 pp) |
| arc_oltp | 2Q 68.25% | 45.43% | 67.48% [67.37–67.79] (-0.77 pp) | 66.97% [66.58–67.44] (-1.28 pp) | 66.07% [65.77–66.35] (-2.19 pp) |
| meta_kvcache_202206_1 | S3-FIFO 69.05% | 65.19% | 67.97% [67.71–68.25] (-1.08 pp) | 67.62% [67.52–67.83] (-1.44 pp) | 66.89% [66.65–67.16] (-2.17 pp) |
| msr_hm_0 | 2Q 17.01% | 11.40% | 14.48% [13.24–15.54] (-2.52 pp) | 12.88% [12.54–14.79] (-4.13 pp) | 14.36% [12.70–17.11] (-2.65 pp) |
| msr_prn_0 | LFU/SIEVE 1.06% | 0.73% | 0.87% [0.84–0.87] (-0.19 pp) | 0.73% [0.68–0.96] (-0.33 pp) | 1.05% [1.00–1.09] (-0.01 pp) |
| msr_proj_0 | S3-FIFO 5.79% | 4.30% | 5.14% [4.35–5.35] (-0.66 pp) | 5.10% [5.05–5.42] (-0.70 pp) | 5.37% [5.23–5.57] (-0.42 pp) |
| msr_src1_2 | 2Q 1.77% | 1.18% | 1.70% [1.47–1.70] (-0.06 pp) | 1.70% [1.54–1.70] (-0.06 pp) | 1.70% [1.24–1.70] (-0.06 pp) |
| msr_usr_0 | S3-FIFO 3.58% | 1.46% | 3.57% [3.00–3.57] (-0.01 pp) | 3.50% [3.50–3.50] (-0.08 pp) | 3.42% [3.41–3.66] (-0.16 pp) |
| msr_web_0 | LFU/SIEVE 2.61% | 2.28% | 2.35% [2.22–2.35] (-0.26 pp) | 2.41% [2.38–2.42] (-0.20 pp) | 2.44% [2.43–2.46] (-0.17 pp) |

The table compares medians with the best fixed median in this dataset. It makes no claim of a universal maximum deficit.
W-TinyLFU is asynchronous: a short batch may miss another performance mode, especially on LIRS loop.
A winning policy name is not evidence of a material or statistically established advantage.

Adaptive medians below the worst fixed median in this dataset:

- msr_prn_0, 20 epochs: -0.0007 percentage points.

## Workload context

Compulsory-miss ceiling assumes an empty cache: 100 × (requests − distinct keys) / requests.
The best/runner-up gap includes ties; tiny gaps cannot support a strong policy ranking.

| Trace | Requests | Distinct keys | Capacity | Capacity/keyspace | Effective sample | Hit ceiling | Best–runner-up | Best–worst |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| twitter_cluster052.csv | 1000000 | 255333 | 10000 | 3.916% | 5.00% | 74.47% | 0.0559 pp | 18.3414 pp |
| lirs_loop.trace | 505500 | 1011 | 500 | 49.456% | 12.80% | 99.80% | 24.5470 pp | 44.2042 pp |
| lirs_2_pools.trace | 100000 | 9939 | 1000 | 10.061% | 6.40% | 90.06% | 0.3430 pp | 4.7630 pp |
| arc_p3 | 2000000 | 426527 | 20000 | 4.689% | 5.00% | 78.67% | 1.1092 pp | 9.9937 pp |
| arc_oltp | 914145 | 186880 | 20000 | 10.702% | 5.00% | 79.56% | 0.4534 pp | 22.8282 pp |
| meta_kvcache_202206_1 | 2000000 | 340723 | 10000 | 2.935% | 5.00% | 82.96% | 0.1303 pp | 3.8611 pp |
| msr_hm_0 | 2000000 | 818034 | 20000 | 2.445% | 5.00% | 59.10% | 0.3573 pp | 5.6098 pp |
| msr_prn_0 | 2000000 | 1900653 | 20000 | 1.052% | 5.00% | 4.97% | 0.0000 pp | 0.3268 pp |
| msr_proj_0 | 2000000 | 1748195 | 20000 | 1.144% | 5.00% | 12.59% | 0.4098 pp | 1.4929 pp |
| msr_src1_2 | 2000000 | 1947636 | 20000 | 1.027% | 5.00% | 2.62% | 0.0393 pp | 0.5854 pp |
| msr_usr_0 | 2000000 | 1778953 | 20000 | 1.124% | 5.00% | 11.05% | 0.0121 pp | 2.1250 pp |
| msr_web_0 | 2000000 | 1855956 | 20000 | 1.078% | 5.00% | 7.20% | 0.0000 pp | 0.3281 pp |

On MSR prn and web the leading LFU/SIEVE rates tie exactly; on src1_2 and usr the best–runner-up margins are below 0.04 points.
The cache is roughly 1% of the distinct keyspace on most MSR prefixes; the ceilings above limit the available hit-rate signal.

## Every fixed arm

| Trace | Policy | Hit rate |
| --- | --- | --- |
| twitter_cluster052.csv | 2Q | 59.62% [59.62–59.62] |
| twitter_cluster052.csv | ARC | 58.99% [58.99–58.99] |
| twitter_cluster052.csv | LFU | 41.44% [41.44–41.44] |
| twitter_cluster052.csv | LRU | 58.39% [58.39–58.39] |
| twitter_cluster052.csv | Random | 54.80% [54.78–54.86] |
| twitter_cluster052.csv | S3-FIFO | 59.73% [59.73–59.73] |
| twitter_cluster052.csv | SIEVE | 59.78% [59.78–59.78] |
| twitter_cluster052.csv | TTL | 58.39% [58.39–58.39] |
| twitter_cluster052.csv | W-TinyLFU | 58.62% [54.70–58.94] |
| lirs_loop.trace | 2Q | 0.00% [0.00–0.00] |
| lirs_loop.trace | ARC | 0.00% [0.00–0.00] |
| lirs_loop.trace | LFU | 0.00% [0.00–0.00] |
| lirs_loop.trace | LRU | 0.00% [0.00–0.00] |
| lirs_loop.trace | Random | 19.66% [19.58–19.73] |
| lirs_loop.trace | S3-FIFO | 0.00% [0.00–0.00] |
| lirs_loop.trace | SIEVE | 0.00% [0.00–0.00] |
| lirs_loop.trace | TTL | 0.00% [0.00–0.00] |
| lirs_loop.trace | W-TinyLFU | 44.20% [32.38–45.90] |
| lirs_2_pools.trace | 2Q | 54.40% [54.40–54.40] |
| lirs_2_pools.trace | ARC | 54.37% [54.37–54.37] |
| lirs_2_pools.trace | LFU | 54.36% [54.36–54.36] |
| lirs_2_pools.trace | LRU | 54.41% [54.41–54.41] |
| lirs_2_pools.trace | Random | 49.99% [49.82–50.25] |
| lirs_2_pools.trace | S3-FIFO | 54.37% [54.37–54.37] |
| lirs_2_pools.trace | SIEVE | 54.36% [54.36–54.36] |
| lirs_2_pools.trace | TTL | 54.41% [54.41–54.41] |
| lirs_2_pools.trace | W-TinyLFU | 54.76% [54.67–54.87] |
| arc_p3 | 2Q | 7.74% [7.74–7.74] |
| arc_p3 | ARC | 10.25% [10.25–10.25] |
| arc_p3 | LFU | 4.82% [4.82–4.82] |
| arc_p3 | LRU | 1.87% [1.87–1.87] |
| arc_p3 | Random | 3.08% [3.06–3.09] |
| arc_p3 | S3-FIFO | 10.75% [10.75–10.75] |
| arc_p3 | SIEVE | 4.82% [4.82–4.82] |
| arc_p3 | TTL | 1.87% [1.87–1.87] |
| arc_p3 | W-TinyLFU | 11.86% [11.49–12.13] |
| arc_oltp | 2Q | 68.25% [68.25–68.25] |
| arc_oltp | ARC | 67.80% [67.80–67.80] |
| arc_oltp | LFU | 45.43% [45.43–45.43] |
| arc_oltp | LRU | 67.06% [67.06–67.06] |
| arc_oltp | Random | 63.02% [62.99–63.05] |
| arc_oltp | S3-FIFO | 67.79% [67.79–67.79] |
| arc_oltp | SIEVE | 67.72% [67.72–67.72] |
| arc_oltp | TTL | 67.06% [67.06–67.06] |
| arc_oltp | W-TinyLFU | 63.17% [63.04–63.27] |
| meta_kvcache_202206_1 | 2Q | 68.16% [68.16–68.16] |
| meta_kvcache_202206_1 | ARC | 68.27% [68.27–68.27] |
| meta_kvcache_202206_1 | LFU | 66.87% [66.87–66.87] |
| meta_kvcache_202206_1 | LRU | 66.39% [66.39–66.39] |
| meta_kvcache_202206_1 | Random | 65.19% [65.17–65.21] |
| meta_kvcache_202206_1 | S3-FIFO | 69.05% [69.05–69.05] |
| meta_kvcache_202206_1 | SIEVE | 68.92% [68.92–68.92] |
| meta_kvcache_202206_1 | TTL | 66.39% [66.39–66.39] |
| meta_kvcache_202206_1 | W-TinyLFU | 68.46% [68.32–68.59] |
| msr_hm_0 | 2Q | 17.01% [17.01–17.01] |
| msr_hm_0 | ARC | 16.65% [16.65–16.65] |
| msr_hm_0 | LFU | 14.99% [14.99–14.99] |
| msr_hm_0 | LRU | 11.40% [11.40–11.40] |
| msr_hm_0 | Random | 12.63% [12.61–12.64] |
| msr_hm_0 | S3-FIFO | 15.84% [15.84–15.84] |
| msr_hm_0 | SIEVE | 15.19% [15.19–15.19] |
| msr_hm_0 | TTL | 11.40% [11.40–11.40] |
| msr_hm_0 | W-TinyLFU | 16.22% [15.78–16.69] |
| msr_prn_0 | 2Q | 1.04% [1.04–1.04] |
| msr_prn_0 | ARC | 1.02% [1.02–1.02] |
| msr_prn_0 | LFU | 1.06% [1.06–1.06] |
| msr_prn_0 | LRU | 1.00% [1.00–1.00] |
| msr_prn_0 | Random | 0.95% [0.94–0.95] |
| msr_prn_0 | S3-FIFO | 1.01% [1.01–1.01] |
| msr_prn_0 | SIEVE | 1.06% [1.06–1.06] |
| msr_prn_0 | TTL | 1.00% [1.00–1.00] |
| msr_prn_0 | W-TinyLFU | 0.73% [0.65–0.82] |
| msr_proj_0 | 2Q | 5.38% [5.38–5.38] |
| msr_proj_0 | ARC | 5.38% [5.38–5.38] |
| msr_proj_0 | LFU | 4.73% [4.73–4.73] |
| msr_proj_0 | LRU | 5.35% [5.35–5.35] |
| msr_proj_0 | Random | 5.20% [5.20–5.21] |
| msr_proj_0 | S3-FIFO | 5.79% [5.79–5.79] |
| msr_proj_0 | SIEVE | 4.73% [4.73–4.73] |
| msr_proj_0 | TTL | 5.35% [5.35–5.35] |
| msr_proj_0 | W-TinyLFU | 4.30% [4.09–5.30] |
| msr_src1_2 | 2Q | 1.77% [1.77–1.77] |
| msr_src1_2 | ARC | 1.73% [1.73–1.73] |
| msr_src1_2 | LFU | 1.42% [1.42–1.42] |
| msr_src1_2 | LRU | 1.70% [1.70–1.70] |
| msr_src1_2 | Random | 1.63% [1.63–1.64] |
| msr_src1_2 | S3-FIFO | 1.70% [1.70–1.70] |
| msr_src1_2 | SIEVE | 1.42% [1.42–1.42] |
| msr_src1_2 | TTL | 1.70% [1.70–1.70] |
| msr_src1_2 | W-TinyLFU | 1.18% [0.91–1.26] |
| msr_usr_0 | 2Q | 3.57% [3.57–3.57] |
| msr_usr_0 | ARC | 3.55% [3.55–3.55] |
| msr_usr_0 | LFU | 1.46% [1.46–1.46] |
| msr_usr_0 | LRU | 3.57% [3.57–3.57] |
| msr_usr_0 | Random | 3.50% [3.49–3.50] |
| msr_usr_0 | S3-FIFO | 3.58% [3.58–3.58] |
| msr_usr_0 | SIEVE | 1.46% [1.46–1.46] |
| msr_usr_0 | TTL | 3.57% [3.57–3.57] |
| msr_usr_0 | W-TinyLFU | 2.86% [2.07–2.98] |
| msr_web_0 | 2Q | 2.61% [2.61–2.61] |
| msr_web_0 | ARC | 2.60% [2.60–2.60] |
| msr_web_0 | LFU | 2.61% [2.61–2.61] |
| msr_web_0 | LRU | 2.30% [2.30–2.30] |
| msr_web_0 | Random | 2.55% [2.55–2.57] |
| msr_web_0 | S3-FIFO | 2.49% [2.49–2.49] |
| msr_web_0 | SIEVE | 2.61% [2.61–2.61] |
| msr_web_0 | TTL | 2.30% [2.30–2.30] |
| msr_web_0 | W-TinyLFU | 2.28% [2.19–2.32] |

## ObserveOnly

LRU remains active, all nine arms are measured, 20 request epochs per replay, the same sample/floor settings.
Every run asserts serving hits equal standalone LRU. Advice rankings measure sampled shadows; they are not full-cache forecasts.
The table counts the final Advice.Best choices across 15 runs; all per-arm hit/miss reports are in traces.json.

| Trace | Serving hit rate | Recommended policies (count) |
| --- | --- | --- |
| twitter_cluster052.csv | 58.39% [58.39–58.39] | S3-FIFO: 2, SIEVE: 2, W-TinyLFU: 11 |
| lirs_loop.trace | 0.00% [0.00–0.00] | W-TinyLFU: 15 |
| lirs_2_pools.trace | 54.41% [54.41–54.41] | ARC: 3, LRU: 5, S3-FIFO: 2, TTL: 2, W-TinyLFU: 3 |
| arc_p3 | 1.87% [1.87–1.87] | W-TinyLFU: 15 |
| arc_oltp | 67.06% [67.06–67.06] | 2Q: 14, ARC: 1 |
| meta_kvcache_202206_1 | 66.39% [66.39–66.39] | W-TinyLFU: 15 |
| msr_hm_0 | 11.40% [11.40–11.40] | 2Q: 13, W-TinyLFU: 2 |
| msr_prn_0 | 1.00% [1.00–1.00] | LFU: 15 |
| msr_proj_0 | 5.35% [5.35–5.35] | S3-FIFO: 11, W-TinyLFU: 4 |
| msr_src1_2 | 1.70% [1.70–1.70] | 2Q: 15 |
| msr_usr_0 | 3.57% [3.57–3.57] | 2Q: 4, LRU: 2, S3-FIFO: 8, TTL: 1 |
| msr_web_0 | 2.30% [2.30–2.30] | 2Q: 7, LFU: 8 |

This is an offline sweep, not a service trial or proof that following Advice will improve production traffic.

## LFU/SIEVE diagnostic

Each request is checked against an independent visited-bit/hand model. A FIFO control and LFU run on the identical stream.

| Trace | LFU hits | SIEVE hits | FIFO hits | LFU/SIEVE decisions differ | SIEVE/model disagreements |
| --- | --- | --- | --- | --- | --- |
| twitter_cluster052.csv | 414436 | 597850 | 555634 | 216148 | 0 |
| lirs_loop.trace | 0 | 0 | 0 | 0 | 0 |
| lirs_2_pools.trace | 54361 | 54361 | 49957 | 0 | 0 |
| arc_p3 | 96324 | 96324 | 38093 | 0 | 0 |
| arc_oltp | 415260 | 619036 | 584567 | 230770 | 0 |
| meta_kvcache_202206_1 | 1337313 | 1378480 | 1313815 | 130305 | 0 |
| msr_hm_0 | 299772 | 303745 | 228175 | 34189 | 0 |
| msr_prn_0 | 21119 | 21119 | 20052 | 0 | 0 |
| msr_proj_0 | 94547 | 94547 | 102774 | 0 | 0 |
| msr_src1_2 | 28362 | 28362 | 34051 | 0 | 0 |
| msr_usr_0 | 29176 | 29176 | 70272 | 0 | 0 |
| msr_web_0 | 52205 | 52205 | 45560 | 0 | 0 |

Equal hit/miss streams do not imply equal eviction state. The nonzero FIFO differences rule out a general reduction to FIFO.
A capacity-2 counterexample separates the mechanisms: a,a,b,b,c,d,a,b gives LFU 3 hits and SIEVE 2; a,b,a,c,a gives SIEVE 2 and FIFO 1.
TestSieveLFUDistinction pins both examples. The real-trace model check tests the adapter without assuming these policies must have different totals.

## P3 tuning

This configuration example is restricted to P3; it does not identify a universal production setting.
Gates mean MinHitRateImprovement=0.02 and SwitchCooldownEpochs=3. All cells keep nine arms, 5% requested sampling and floor 64.

| Epochs | Migration | Gates | Hit rate |
| --- | --- | --- | --- |
| 10 | cold | False | 12.00% [11.55–12.47] |
| 10 | cold | True | 7.49% [4.46–7.73] |
| 10 | warm | False | 12.33% [11.84–12.70] |
| 10 | warm | True | 7.91% [5.37–8.03] |
| 20 | cold | False | 13.22% [11.23–13.46] |
| 20 | cold | True | 10.10% [7.22–10.41] |
| 20 | warm | False | 12.70% [12.38–13.07] |
| 20 | warm | True | 9.27% [8.10–9.93] |
| 50 | cold | False | 11.93% [4.11–12.09] |
| 50 | cold | True | 10.26% [8.11–12.31] |
| 50 | warm | False | 12.36% [8.50–13.41] |
| 50 | warm | True | 11.09% [9.76–12.66] |

## Meta object-count versus byte capacity

Pinned libCacheSim `1d7415569978330ea95c9cff06a260630406f7e3`, LRU, the same 2000000 GET requests in both modes.
The export retains request-time size + key_size. The pinned simulator charges insertion-time size until eviction; size changes on hits do not resize resident objects. Metadata overhead is excluded.
Byte budget = object capacity × mean first-seen size per distinct key (475.262 bytes), rounded down.
This is a stated budget convention, not proof of equal resident memory. Deltas are request miss ratios, not byte-weighted miss ratios.

| Objects | Bytes | Ignore sizes: miss | Account sizes: miss | Difference |
| --- | --- | --- | --- | --- |
| 2500 | 1188154 | 39.32% | 39.65% | +0.33 pp |
| 5000 | 2376308 | 36.67% | 37.34% | +0.67 pp |
| 10000 | 4752616 | 33.61% | 34.48% | +0.87 pp |
| 20000 | 9505233 | 29.80% | 30.88% | +1.08 pp |
| 40000 | 19010466 | 25.42% | 26.54% | +1.12 pp |

## Provenance and limits

manifest.json is generated by scripts/record_evidence.py and hashes every retained input/output.
Verify with `python3 scripts/record_evidence.py --verify bench/results/current`.
Reference calibration compares independent expansions of the same interpretation and LRU counting; it cannot detect a shared interpretation error.
Its tolerance is 0.0051 percentage points, just above the simulator's four-decimal rounding limit. All loaded traces must have reference coverage.
The byte experiment uses the Meta prefix only. Other traces remain entry-capacity, not equal-memory comparisons.
Per-operation timings and wall-clock experiments remain raw diagnostics in the logs, not stable product claims.

Reproduce: `AS_CACHE_TRACES=$PWD/traces python3 scripts/record_evidence.py --out <temporary-output-directory>`.
A complete run requires the pinned libCacheSim build and three sequential evidence runs. Replace current results after validation; do not create a historical archive.
