# Current measurement results

Measured source: `00bdcb103a9a7e56840a2dedda7bddc753d5269e`; clean committed trees; three consecutive full evidence runs.
Nondeterministic subjects have 15 observations (three batches of five). Deterministic fixed arms run once per batch.
Median [min–max] describes these observations, not a confidence interval or a bound on future runs.
All trace-matrix outcomes are retained; relative hit rates do not decide whether this matrix passes.

## Trace matrix

All nine arms; request-counted epochs. Actual constructor settings are retained for every adaptive and ObserveOnly cell.
Effective sample rates come from the measured caches' Advice and are shown in the context table.

| Trace | Best fixed median [min–max] (ties retained) | Worst fixed median [min–max] (ties retained) | 10 epochs | 20 epochs | 50 epochs |
| --- | --- | --- | --- | --- | --- |
| twitter_cluster052.csv | SIEVE 59.78% [59.78–59.78] | LFU 41.44% [41.44–41.44] | 58.70% [58.56–59.76] (-1.09 pp) | 58.78% [58.60–58.91] (-1.00 pp) | 58.21% [57.95–58.45] (-1.57 pp) |
| lirs_loop.trace | W-TinyLFU 44.24% [40.52–45.80] | 2Q 0.00% [0.00–0.00]<br>ARC 0.00% [0.00–0.00]<br>LFU 0.00% [0.00–0.00]<br>LRU 0.00% [0.00–0.00]<br>S3-FIFO 0.00% [0.00–0.00]<br>SIEVE 0.00% [0.00–0.00]<br>TTL 0.00% [0.00–0.00] | 40.96% [38.50–47.71] (-3.28 pp) | 41.77% [40.21–57.14] (-2.47 pp) | 43.59% [40.78–46.62] (-0.65 pp) |
| lirs_2_pools.trace | W-TinyLFU 54.70% [54.41–57.98] | Random 49.98% [49.85–50.11] | 54.40% [54.36–54.44] (-0.30 pp) | 54.39% [54.37–55.09] (-0.31 pp) | 54.22% [53.99–54.33] (-0.49 pp) |
| arc_p3 | W-TinyLFU 11.92% [11.29–12.34] | LRU 1.87% [1.87–1.87]<br>TTL 1.87% [1.87–1.87] | 12.31% [11.93–12.67] (+0.39 pp) | 12.75% [12.34–13.16] (+0.82 pp) | 12.14% [8.65–13.46] (+0.22 pp) |
| arc_oltp | 2Q 68.25% [68.25–68.25] | LFU 45.43% [45.43–45.43] | 67.42% [67.37–67.74] (-0.84 pp) | 67.02% [66.71–67.24] (-1.24 pp) | 66.12% [65.85–66.42] (-2.14 pp) |
| meta_kvcache_202206_1 | S3-FIFO 69.05% [69.05–69.05] | Random 65.19% [65.16–65.21] | 67.94% [67.71–68.45] (-1.11 pp) | 67.58% [67.48–67.97] (-1.48 pp) | 66.87% [66.70–67.07] (-2.18 pp) |
| msr_hm_0 | 2Q 17.01% [17.01–17.01] | LRU 11.40% [11.40–11.40]<br>TTL 11.40% [11.40–11.40] | 14.46% [13.30–15.61] (-2.55 pp) | 12.91% [12.12–14.87] (-4.09 pp) | 14.43% [13.85–17.21] (-2.58 pp) |
| msr_prn_0 | LFU 1.06% [1.06–1.06]<br>SIEVE 1.06% [1.06–1.06] | W-TinyLFU 0.77% [0.69–0.81] | 0.87% [0.84–0.90] (-0.19 pp) | 0.72% [0.70–0.97] (-0.34 pp) | 1.06% [0.94–1.09] (-0.00 pp) |
| msr_proj_0 | S3-FIFO 5.79% [5.79–5.79] | W-TinyLFU 4.36% [4.07–5.14] | 5.14% [5.13–5.35] (-0.66 pp) | 5.09% [5.07–5.18] (-0.70 pp) | 5.37% [5.34–5.56] (-0.42 pp) |
| msr_src1_2 | 2Q 1.77% [1.77–1.77] | W-TinyLFU 1.17% [0.91–1.22] | 1.70% [1.70–1.70] (-0.06 pp) | 1.70% [1.70–1.70] (-0.06 pp) | 1.70% [1.24–1.70] (-0.06 pp) |
| msr_usr_0 | S3-FIFO 3.58% [3.58–3.58] | LFU 1.46% [1.46–1.46]<br>SIEVE 1.46% [1.46–1.46] | 3.57% [3.00–3.57] (-0.01 pp) | 3.50% [3.50–3.52] (-0.08 pp) | 3.64% [3.41–3.66] (+0.05 pp) |
| msr_web_0 | LFU 2.61% [2.61–2.61]<br>SIEVE 2.61% [2.61–2.61] | W-TinyLFU 2.28% [2.23–2.33] | 2.35% [2.28–2.35] (-0.26 pp) | 2.41% [2.40–2.41] (-0.20 pp) | 2.44% [2.43–2.47] (-0.17 pp) |

Adaptive medians trail the best fixed median on 10/12 traces at every tested epoch setting.
Counts below the best fixed median by setting: 10 epochs: 11/12; 20 epochs: 11/12; 50 epochs: 10/12.
Traces above the best fixed median at every setting: arc_p3.
The table compares medians with the best fixed median in this dataset. It makes no claim of a universal maximum deficit.
W-TinyLFU is asynchronous: a short batch may miss another performance mode, especially on LIRS loop.
A winning policy name is not evidence of a material or statistically established advantage.

Adaptive medians below the worst fixed median in this dataset:

- msr_prn_0, 20 epochs: -0.0510 percentage points versus W-TinyLFU 0.77% [0.69–0.81].

## Workload context

Compulsory-miss ceiling assumes an empty cache: 100 × (requests − distinct keys) / requests.
The best/runner-up gap includes ties; tiny gaps cannot support a strong policy ranking.

| Trace | Requests | Distinct keys | Capacity | Capacity/keyspace | Effective sample | Hit ceiling | Best–runner-up | Best–worst |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| twitter_cluster052.csv | 1000000 | 255333 | 10000 | 3.916% | 5.00% | 74.47% | 0.0559 pp | 18.3414 pp |
| lirs_loop.trace | 505500 | 1011 | 500 | 49.456% | 12.80% | 99.80% | 24.5602 pp | 44.2411 pp |
| lirs_2_pools.trace | 100000 | 9939 | 1000 | 10.061% | 6.40% | 90.06% | 0.2860 pp | 4.7240 pp |
| arc_p3 | 2000000 | 426527 | 20000 | 4.689% | 5.00% | 78.67% | 1.1708 pp | 10.0553 pp |
| arc_oltp | 914145 | 186880 | 20000 | 10.702% | 5.00% | 79.56% | 0.4534 pp | 22.8282 pp |
| meta_kvcache_202206_1 | 2000000 | 340723 | 10000 | 2.935% | 5.00% | 82.96% | 0.1303 pp | 3.8678 pp |
| msr_hm_0 | 2000000 | 818034 | 20000 | 2.445% | 5.00% | 59.10% | 0.3573 pp | 5.6098 pp |
| msr_prn_0 | 2000000 | 1900653 | 20000 | 1.052% | 5.00% | 4.97% | 0.0000 pp | 0.2842 pp |
| msr_proj_0 | 2000000 | 1748195 | 20000 | 1.144% | 5.00% | 12.59% | 0.4098 pp | 1.4346 pp |
| msr_src1_2 | 2000000 | 1947636 | 20000 | 1.027% | 5.00% | 2.62% | 0.0393 pp | 0.6002 pp |
| msr_usr_0 | 2000000 | 1778953 | 20000 | 1.124% | 5.00% | 11.05% | 0.0121 pp | 2.1250 pp |
| msr_web_0 | 2000000 | 1855956 | 20000 | 1.078% | 5.00% | 7.20% | 0.0000 pp | 0.3283 pp |

Use the margins and ties above when interpreting policy names; small differences do not establish a useful ranking.
The cache is roughly 1% of the distinct keyspace on most MSR prefixes; the ceilings above limit the available hit-rate signal.

## Every fixed arm

| Trace | Policy | Hit rate |
| --- | --- | --- |
| twitter_cluster052.csv | 2Q | 59.62% [59.62–59.62] |
| twitter_cluster052.csv | ARC | 58.99% [58.99–58.99] |
| twitter_cluster052.csv | LFU | 41.44% [41.44–41.44] |
| twitter_cluster052.csv | LRU | 58.39% [58.39–58.39] |
| twitter_cluster052.csv | Random | 54.80% [54.78–54.85] |
| twitter_cluster052.csv | S3-FIFO | 59.73% [59.73–59.73] |
| twitter_cluster052.csv | SIEVE | 59.78% [59.78–59.78] |
| twitter_cluster052.csv | TTL | 58.39% [58.39–58.39] |
| twitter_cluster052.csv | W-TinyLFU | 57.41% [54.34–58.75] |
| lirs_loop.trace | 2Q | 0.00% [0.00–0.00] |
| lirs_loop.trace | ARC | 0.00% [0.00–0.00] |
| lirs_loop.trace | LFU | 0.00% [0.00–0.00] |
| lirs_loop.trace | LRU | 0.00% [0.00–0.00] |
| lirs_loop.trace | Random | 19.68% [19.61–19.76] |
| lirs_loop.trace | S3-FIFO | 0.00% [0.00–0.00] |
| lirs_loop.trace | SIEVE | 0.00% [0.00–0.00] |
| lirs_loop.trace | TTL | 0.00% [0.00–0.00] |
| lirs_loop.trace | W-TinyLFU | 44.24% [40.52–45.80] |
| lirs_2_pools.trace | 2Q | 54.40% [54.40–54.40] |
| lirs_2_pools.trace | ARC | 54.37% [54.37–54.37] |
| lirs_2_pools.trace | LFU | 54.36% [54.36–54.36] |
| lirs_2_pools.trace | LRU | 54.41% [54.41–54.41] |
| lirs_2_pools.trace | Random | 49.98% [49.85–50.11] |
| lirs_2_pools.trace | S3-FIFO | 54.37% [54.37–54.37] |
| lirs_2_pools.trace | SIEVE | 54.36% [54.36–54.36] |
| lirs_2_pools.trace | TTL | 54.41% [54.41–54.41] |
| lirs_2_pools.trace | W-TinyLFU | 54.70% [54.41–57.98] |
| arc_p3 | 2Q | 7.74% [7.74–7.74] |
| arc_p3 | ARC | 10.25% [10.25–10.25] |
| arc_p3 | LFU | 4.82% [4.82–4.82] |
| arc_p3 | LRU | 1.87% [1.87–1.87] |
| arc_p3 | Random | 3.08% [3.07–3.10] |
| arc_p3 | S3-FIFO | 10.75% [10.75–10.75] |
| arc_p3 | SIEVE | 4.82% [4.82–4.82] |
| arc_p3 | TTL | 1.87% [1.87–1.87] |
| arc_p3 | W-TinyLFU | 11.92% [11.29–12.34] |
| arc_oltp | 2Q | 68.25% [68.25–68.25] |
| arc_oltp | ARC | 67.80% [67.80–67.80] |
| arc_oltp | LFU | 45.43% [45.43–45.43] |
| arc_oltp | LRU | 67.06% [67.06–67.06] |
| arc_oltp | Random | 63.01% [62.97–63.04] |
| arc_oltp | S3-FIFO | 67.79% [67.79–67.79] |
| arc_oltp | SIEVE | 67.72% [67.72–67.72] |
| arc_oltp | TTL | 67.06% [67.06–67.06] |
| arc_oltp | W-TinyLFU | 63.15% [62.98–63.55] |
| meta_kvcache_202206_1 | 2Q | 68.16% [68.16–68.16] |
| meta_kvcache_202206_1 | ARC | 68.27% [68.27–68.27] |
| meta_kvcache_202206_1 | LFU | 66.87% [66.87–66.87] |
| meta_kvcache_202206_1 | LRU | 66.39% [66.39–66.39] |
| meta_kvcache_202206_1 | Random | 65.19% [65.16–65.21] |
| meta_kvcache_202206_1 | S3-FIFO | 69.05% [69.05–69.05] |
| meta_kvcache_202206_1 | SIEVE | 68.92% [68.92–68.92] |
| meta_kvcache_202206_1 | TTL | 66.39% [66.39–66.39] |
| meta_kvcache_202206_1 | W-TinyLFU | 68.50% [68.31–68.65] |
| msr_hm_0 | 2Q | 17.01% [17.01–17.01] |
| msr_hm_0 | ARC | 16.65% [16.65–16.65] |
| msr_hm_0 | LFU | 14.99% [14.99–14.99] |
| msr_hm_0 | LRU | 11.40% [11.40–11.40] |
| msr_hm_0 | Random | 12.62% [12.61–12.65] |
| msr_hm_0 | S3-FIFO | 15.84% [15.84–15.84] |
| msr_hm_0 | SIEVE | 15.19% [15.19–15.19] |
| msr_hm_0 | TTL | 11.40% [11.40–11.40] |
| msr_hm_0 | W-TinyLFU | 16.23% [15.69–16.93] |
| msr_prn_0 | 2Q | 1.04% [1.04–1.04] |
| msr_prn_0 | ARC | 1.02% [1.02–1.02] |
| msr_prn_0 | LFU | 1.06% [1.06–1.06] |
| msr_prn_0 | LRU | 1.00% [1.00–1.00] |
| msr_prn_0 | Random | 0.95% [0.94–0.95] |
| msr_prn_0 | S3-FIFO | 1.01% [1.01–1.01] |
| msr_prn_0 | SIEVE | 1.06% [1.06–1.06] |
| msr_prn_0 | TTL | 1.00% [1.00–1.00] |
| msr_prn_0 | W-TinyLFU | 0.77% [0.69–0.81] |
| msr_proj_0 | 2Q | 5.38% [5.38–5.38] |
| msr_proj_0 | ARC | 5.38% [5.38–5.38] |
| msr_proj_0 | LFU | 4.73% [4.73–4.73] |
| msr_proj_0 | LRU | 5.35% [5.35–5.35] |
| msr_proj_0 | Random | 5.21% [5.20–5.21] |
| msr_proj_0 | S3-FIFO | 5.79% [5.79–5.79] |
| msr_proj_0 | SIEVE | 4.73% [4.73–4.73] |
| msr_proj_0 | TTL | 5.35% [5.35–5.35] |
| msr_proj_0 | W-TinyLFU | 4.36% [4.07–5.14] |
| msr_src1_2 | 2Q | 1.77% [1.77–1.77] |
| msr_src1_2 | ARC | 1.73% [1.73–1.73] |
| msr_src1_2 | LFU | 1.42% [1.42–1.42] |
| msr_src1_2 | LRU | 1.70% [1.70–1.70] |
| msr_src1_2 | Random | 1.63% [1.63–1.64] |
| msr_src1_2 | S3-FIFO | 1.70% [1.70–1.70] |
| msr_src1_2 | SIEVE | 1.42% [1.42–1.42] |
| msr_src1_2 | TTL | 1.70% [1.70–1.70] |
| msr_src1_2 | W-TinyLFU | 1.17% [0.91–1.22] |
| msr_usr_0 | 2Q | 3.57% [3.57–3.57] |
| msr_usr_0 | ARC | 3.55% [3.55–3.55] |
| msr_usr_0 | LFU | 1.46% [1.46–1.46] |
| msr_usr_0 | LRU | 3.57% [3.57–3.57] |
| msr_usr_0 | Random | 3.49% [3.48–3.51] |
| msr_usr_0 | S3-FIFO | 3.58% [3.58–3.58] |
| msr_usr_0 | SIEVE | 1.46% [1.46–1.46] |
| msr_usr_0 | TTL | 3.57% [3.57–3.57] |
| msr_usr_0 | W-TinyLFU | 2.25% [2.12–2.85] |
| msr_web_0 | 2Q | 2.61% [2.61–2.61] |
| msr_web_0 | ARC | 2.60% [2.60–2.60] |
| msr_web_0 | LFU | 2.61% [2.61–2.61] |
| msr_web_0 | LRU | 2.30% [2.30–2.30] |
| msr_web_0 | Random | 2.56% [2.55–2.56] |
| msr_web_0 | S3-FIFO | 2.49% [2.49–2.49] |
| msr_web_0 | SIEVE | 2.61% [2.61–2.61] |
| msr_web_0 | TTL | 2.30% [2.30–2.30] |
| msr_web_0 | W-TinyLFU | 2.28% [2.23–2.33] |

## ObserveOnly

LRU remains active, all nine arms are measured, 20 request epochs per replay, the same sample/floor settings.
Every run asserts serving hits equal standalone LRU. Advice rankings measure sampled shadows; they are not full-cache forecasts.
The table counts the final Advice.Best choices across 15 runs; all per-arm hit/miss reports are in traces.json.

| Trace | Serving hit rate | Recommended policies (count) |
| --- | --- | --- |
| twitter_cluster052.csv | 58.39% [58.39–58.39] | SIEVE: 5, W-TinyLFU: 10 |
| lirs_loop.trace | 0.00% [0.00–0.00] | W-TinyLFU: 15 |
| lirs_2_pools.trace | 54.41% [54.41–54.41] | ARC: 3, LFU: 2, LRU: 3, S3-FIFO: 1, TTL: 1, W-TinyLFU: 5 |
| arc_p3 | 1.87% [1.87–1.87] | W-TinyLFU: 15 |
| arc_oltp | 67.06% [67.06–67.06] | 2Q: 15 |
| meta_kvcache_202206_1 | 66.39% [66.39–66.39] | W-TinyLFU: 15 |
| msr_hm_0 | 11.40% [11.40–11.40] | 2Q: 10, W-TinyLFU: 5 |
| msr_prn_0 | 1.00% [1.00–1.00] | LFU: 15 |
| msr_proj_0 | 5.35% [5.35–5.35] | S3-FIFO: 8, W-TinyLFU: 7 |
| msr_src1_2 | 1.70% [1.70–1.70] | 2Q: 15 |
| msr_usr_0 | 3.57% [3.57–3.57] | 2Q: 1, LRU: 1, S3-FIFO: 7, TTL: 6 |
| msr_web_0 | 2.30% [2.30–2.30] | 2Q: 7, ARC: 1, LFU: 6, Random: 1 |

Retrospective modal agreement: 9/12 traces. Each trace counts once, and all tied modal choices must belong to the tied best-fixed set to count as agreement.
Individual final recommendations in the best-fixed set: 116/180 (64.44%). Every run counts once; any tied best-fixed arm counts as agreement.
These compare final sampled Advice choices with standalone full-cache medians in this dataset, not forecast accuracy or a causal cost of following Advice. Serving remained LRU.

Modal mismatches (each tied nonwinning mode has its own row):

| Trace | Modal recommendation: standalone median [min–max] | Best fixed median [min–max] | Standalone median difference |
| --- | --- | --- | --- |
| twitter_cluster052.csv | W-TinyLFU 57.41% [54.34–58.75] | SIEVE 59.78% [59.78–59.78] | -2.3720 pp |
| meta_kvcache_202206_1 | W-TinyLFU 68.50% [68.31–68.65] | S3-FIFO 69.05% [69.05–69.05] | -0.5497 pp |
| msr_web_0 | 2Q 2.61% [2.61–2.61] | LFU 2.61% [2.61–2.61]<br>SIEVE 2.61% [2.61–2.61] | -0.0002 pp |

For example, on twitter_cluster052.csv the modal W-TinyLFU recommendation has a standalone median -2.3720 points relative to the best fixed median. This subtraction compares separate full-cache replays; the sampled ObserveOnly cache did not switch to W-TinyLFU or measure that difference as a serving loss.

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
Every cell retains its actual migration, sampling and stability settings in tuning.json; nine arms are measured.

| Epochs | Migration | Gates | Hit rate |
| --- | --- | --- | --- |
| 10 | cold | False | 12.24% [11.83–12.44] |
| 10 | cold | True | 7.61% [4.52–7.84] |
| 10 | warm | False | 12.47% [12.03–12.74] |
| 10 | warm | True | 7.90% [7.68–8.19] |
| 20 | cold | False | 12.86% [11.13–13.58] |
| 20 | cold | True | 10.14% [8.81–10.36] |
| 20 | warm | False | 12.78% [12.52–13.12] |
| 20 | warm | True | 9.30% [9.04–10.07] |
| 50 | cold | False | 11.99% [8.49–12.27] |
| 50 | cold | True | 10.54% [8.21–12.27] |
| 50 | warm | False | 12.30% [8.39–13.41] |
| 50 | warm | True | 10.90% [9.83–12.61] |

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
The pinned input catalog has 13 files. lirs_multi2.trace.gz is inventoried but not replayed in the 12-trace matrix or reference gate.
Verify with `python3 scripts/record_evidence.py --verify bench/results/current`.
Reference calibration compares independent expansions of the same interpretation and LRU counting; it cannot detect a shared interpretation error.
The reference accepts finite ratios in [0, 1] with exactly four decimals. Tolerance is half that rounding quantum plus numerical slack: 0.0051 percentage points. All loaded traces must have reference coverage.
The byte experiment uses the Meta prefix only. Other traces remain entry-capacity, not equal-memory comparisons.
Per-operation timings and wall-clock experiments remain raw diagnostics in the logs, not stable product claims.

Reproduce: `AS_CACHE_TRACES=$PWD/traces python3 scripts/record_evidence.py --out <temporary-output-directory>`.
A complete run requires the pinned libCacheSim build and three sequential evidence runs. Replace current results after validation; do not create a historical archive.
