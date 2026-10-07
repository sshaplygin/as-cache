# Current measurement results

Measured source: `1cb65fe0242bfae6312d5c1f54b8660d68e40e24`; clean committed trees; three consecutive full evidence runs.
Nondeterministic subjects have 15 observations (three batches of five). Deterministic fixed arms run once per batch.
Median [min–max] describes these observations, not a confidence interval or a bound on future runs.
All trace-matrix outcomes are retained; relative hit rates do not decide whether this matrix passes.

## Trace matrix

All nine arms; request-counted epochs. Actual constructor settings are retained for every adaptive and ObserveOnly cell.
Effective sample rates come from the measured caches' Advice and are shown in the context table.

| Trace | Best fixed median [min–max] (ties retained) | Worst fixed median [min–max] (ties retained) | 10 epochs | 20 epochs | 50 epochs |
| --- | --- | --- | --- | --- | --- |
| twitter_cluster052.csv | SIEVE 59.78% [59.78–59.78] | LFU 41.44% [41.44–41.44] | 58.68% [58.59–59.03] (-1.10 pp) | 58.80% [58.61–58.97] (-0.98 pp) | 58.19% [57.92–58.35] (-1.60 pp) |
| lirs_loop.trace | W-TinyLFU 49.26% [42.39–59.77] | 2Q 0.00% [0.00–0.00]<br>ARC 0.00% [0.00–0.00]<br>LFU 0.00% [0.00–0.00]<br>LRU 0.00% [0.00–0.00]<br>S3-FIFO 0.00% [0.00–0.00]<br>SIEVE 0.00% [0.00–0.00]<br>TTL 0.00% [0.00–0.00] | 40.67% [35.81–72.96] (-8.58 pp) | 42.86% [39.60–45.53] (-6.39 pp) | 44.08% [41.15–45.69] (-5.17 pp) |
| lirs_2_pools.trace | W-TinyLFU 54.78% [54.59–55.86] | Random 49.95% [49.77–50.05] | 54.41% [54.38–54.43] (-0.37 pp) | 54.40% [54.23–54.69] (-0.38 pp) | 54.23% [54.16–54.28] (-0.55 pp) |
| arc_p3 | W-TinyLFU 12.10% [11.52–12.38] | LRU 1.87% [1.87–1.87]<br>TTL 1.87% [1.87–1.87] | 12.44% [11.93–12.68] (+0.35 pp) | 12.92% [12.24–13.11] (+0.82 pp) | 12.42% [8.58–13.59] (+0.32 pp) |
| arc_oltp | 2Q 68.25% [68.25–68.25] | LFU 45.43% [45.43–45.43] | 67.51% [67.37–67.74] (-0.74 pp) | 66.92% [66.68–67.22] (-1.34 pp) | 66.14% [65.86–66.59] (-2.11 pp) |
| meta_kvcache_202206_1 | S3-FIFO 69.05% [69.05–69.05] | Random 65.18% [65.17–65.19] | 68.10% [67.74–68.29] (-0.96 pp) | 67.60% [67.42–67.78] (-1.45 pp) | 66.90% [66.74–66.97] (-2.16 pp) |
| msr_hm_0 | 2Q 17.01% [17.01–17.01] | LRU 11.40% [11.40–11.40]<br>TTL 11.40% [11.40–11.40] | 14.49% [12.53–15.59] (-2.51 pp) | 13.03% [12.12–14.41] (-3.97 pp) | 14.60% [13.48–16.08] (-2.40 pp) |
| msr_prn_0 | LFU 1.06% [1.06–1.06]<br>SIEVE 1.06% [1.06–1.06] | W-TinyLFU 0.75% [0.67–0.93] | 0.87% [0.84–0.87] (-0.19 pp) | 0.73% [0.70–0.97] (-0.33 pp) | 1.05% [0.98–1.09] (-0.00 pp) |
| msr_proj_0 | S3-FIFO 5.79% [5.79–5.79] | W-TinyLFU 4.26% [4.11–5.06] | 5.14% [5.13–5.35] (-0.66 pp) | 5.09% [5.07–5.18] (-0.70 pp) | 5.39% [5.33–5.56] (-0.41 pp) |
| msr_src1_2 | 2Q 1.77% [1.77–1.77] | W-TinyLFU 1.15% [0.91–1.22] | 1.70% [1.70–1.70] (-0.06 pp) | 1.70% [1.70–1.70] (-0.06 pp) | 1.70% [1.24–1.70] (-0.07 pp) |
| msr_usr_0 | S3-FIFO 3.58% [3.58–3.58] | LFU 1.46% [1.46–1.46]<br>SIEVE 1.46% [1.46–1.46] | 3.57% [3.00–3.57] (-0.01 pp) | 3.50% [3.47–3.52] (-0.08 pp) | 3.42% [3.41–3.67] (-0.16 pp) |
| msr_web_0 | LFU 2.61% [2.61–2.61]<br>SIEVE 2.61% [2.61–2.61] | W-TinyLFU 2.28% [2.24–2.32] | 2.35% [2.22–2.35] (-0.26 pp) | 2.41% [2.39–2.41] (-0.20 pp) | 2.44% [2.41–2.48] (-0.17 pp) |

Adaptive medians trail the best fixed median on 11/12 traces at every tested epoch setting.
Counts below the best fixed median by setting: 10 epochs: 11/12; 20 epochs: 11/12; 50 epochs: 11/12.
Traces above the best fixed median at every setting: arc_p3.
The table compares medians with the best fixed median in this dataset. It makes no claim of a universal maximum deficit.
W-TinyLFU is asynchronous: a short batch may miss another performance mode, especially on LIRS loop.
A winning policy name is not evidence of a material or statistically established advantage.

Adaptive medians below the worst fixed median in this dataset:

- msr_prn_0, 20 epochs: -0.0207 percentage points versus W-TinyLFU 0.75% [0.67–0.93].

## Workload context

Compulsory-miss ceiling assumes an empty cache: 100 × (requests − distinct keys) / requests.
The best/runner-up gap includes ties; tiny gaps cannot support a strong policy ranking.

| Trace | Requests | Distinct keys | Capacity | Capacity/keyspace | Effective sample | Hit ceiling | Best–runner-up | Best–worst |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| twitter_cluster052.csv | 1000000 | 255333 | 10000 | 3.916% | 5.00% | 74.47% | 0.0559 pp | 18.3414 pp |
| lirs_loop.trace | 505500 | 1011 | 500 | 49.456% | 12.80% | 99.80% | 29.5960 pp | 49.2564 pp |
| lirs_2_pools.trace | 100000 | 9939 | 1000 | 10.061% | 6.40% | 90.06% | 0.3640 pp | 4.8260 pp |
| arc_p3 | 2000000 | 426527 | 20000 | 4.689% | 5.00% | 78.67% | 1.3433 pp | 10.2278 pp |
| arc_oltp | 914145 | 186880 | 20000 | 10.702% | 5.00% | 79.56% | 0.4534 pp | 22.8282 pp |
| meta_kvcache_202206_1 | 2000000 | 340723 | 10000 | 2.935% | 5.00% | 82.96% | 0.1303 pp | 3.8695 pp |
| msr_hm_0 | 2000000 | 818034 | 20000 | 2.445% | 5.00% | 59.10% | 0.3573 pp | 5.6098 pp |
| msr_prn_0 | 2000000 | 1900653 | 20000 | 1.052% | 5.00% | 4.97% | 0.0000 pp | 0.3062 pp |
| msr_proj_0 | 2000000 | 1748195 | 20000 | 1.144% | 5.00% | 12.59% | 0.4098 pp | 1.5390 pp |
| msr_src1_2 | 2000000 | 1947636 | 20000 | 1.027% | 5.00% | 2.62% | 0.0393 pp | 0.6143 pp |
| msr_usr_0 | 2000000 | 1778953 | 20000 | 1.124% | 5.00% | 11.05% | 0.0121 pp | 2.1250 pp |
| msr_web_0 | 2000000 | 1855956 | 20000 | 1.078% | 5.00% | 7.20% | 0.0000 pp | 0.3335 pp |

Use the margins and ties above when interpreting policy names; small differences do not establish a useful ranking.
The cache is roughly 1% of the distinct keyspace on most MSR prefixes; the ceilings above limit the available hit-rate signal.

## Every fixed arm

| Trace | Policy | Hit rate |
| --- | --- | --- |
| twitter_cluster052.csv | 2Q | 59.62% [59.62–59.62] |
| twitter_cluster052.csv | ARC | 58.99% [58.99–58.99] |
| twitter_cluster052.csv | LFU | 41.44% [41.44–41.44] |
| twitter_cluster052.csv | LRU | 58.39% [58.39–58.39] |
| twitter_cluster052.csv | Random | 54.80% [54.76–54.83] |
| twitter_cluster052.csv | S3-FIFO | 59.73% [59.73–59.73] |
| twitter_cluster052.csv | SIEVE | 59.78% [59.78–59.78] |
| twitter_cluster052.csv | TTL | 58.39% [58.39–58.39] |
| twitter_cluster052.csv | W-TinyLFU | 57.61% [54.48–58.76] |
| lirs_loop.trace | 2Q | 0.00% [0.00–0.00] |
| lirs_loop.trace | ARC | 0.00% [0.00–0.00] |
| lirs_loop.trace | LFU | 0.00% [0.00–0.00] |
| lirs_loop.trace | LRU | 0.00% [0.00–0.00] |
| lirs_loop.trace | Random | 19.66% [19.61–19.74] |
| lirs_loop.trace | S3-FIFO | 0.00% [0.00–0.00] |
| lirs_loop.trace | SIEVE | 0.00% [0.00–0.00] |
| lirs_loop.trace | TTL | 0.00% [0.00–0.00] |
| lirs_loop.trace | W-TinyLFU | 49.26% [42.39–59.77] |
| lirs_2_pools.trace | 2Q | 54.40% [54.40–54.40] |
| lirs_2_pools.trace | ARC | 54.37% [54.37–54.37] |
| lirs_2_pools.trace | LFU | 54.36% [54.36–54.36] |
| lirs_2_pools.trace | LRU | 54.41% [54.41–54.41] |
| lirs_2_pools.trace | Random | 49.95% [49.77–50.05] |
| lirs_2_pools.trace | S3-FIFO | 54.37% [54.37–54.37] |
| lirs_2_pools.trace | SIEVE | 54.36% [54.36–54.36] |
| lirs_2_pools.trace | TTL | 54.41% [54.41–54.41] |
| lirs_2_pools.trace | W-TinyLFU | 54.78% [54.59–55.86] |
| arc_p3 | 2Q | 7.74% [7.74–7.74] |
| arc_p3 | ARC | 10.25% [10.25–10.25] |
| arc_p3 | LFU | 4.82% [4.82–4.82] |
| arc_p3 | LRU | 1.87% [1.87–1.87] |
| arc_p3 | Random | 3.08% [3.07–3.09] |
| arc_p3 | S3-FIFO | 10.75% [10.75–10.75] |
| arc_p3 | SIEVE | 4.82% [4.82–4.82] |
| arc_p3 | TTL | 1.87% [1.87–1.87] |
| arc_p3 | W-TinyLFU | 12.10% [11.52–12.38] |
| arc_oltp | 2Q | 68.25% [68.25–68.25] |
| arc_oltp | ARC | 67.80% [67.80–67.80] |
| arc_oltp | LFU | 45.43% [45.43–45.43] |
| arc_oltp | LRU | 67.06% [67.06–67.06] |
| arc_oltp | Random | 63.02% [62.97–63.05] |
| arc_oltp | S3-FIFO | 67.79% [67.79–67.79] |
| arc_oltp | SIEVE | 67.72% [67.72–67.72] |
| arc_oltp | TTL | 67.06% [67.06–67.06] |
| arc_oltp | W-TinyLFU | 63.11% [62.04–63.40] |
| meta_kvcache_202206_1 | 2Q | 68.16% [68.16–68.16] |
| meta_kvcache_202206_1 | ARC | 68.27% [68.27–68.27] |
| meta_kvcache_202206_1 | LFU | 66.87% [66.87–66.87] |
| meta_kvcache_202206_1 | LRU | 66.39% [66.39–66.39] |
| meta_kvcache_202206_1 | Random | 65.18% [65.17–65.19] |
| meta_kvcache_202206_1 | S3-FIFO | 69.05% [69.05–69.05] |
| meta_kvcache_202206_1 | SIEVE | 68.92% [68.92–68.92] |
| meta_kvcache_202206_1 | TTL | 66.39% [66.39–66.39] |
| meta_kvcache_202206_1 | W-TinyLFU | 68.53% [68.31–68.72] |
| msr_hm_0 | 2Q | 17.01% [17.01–17.01] |
| msr_hm_0 | ARC | 16.65% [16.65–16.65] |
| msr_hm_0 | LFU | 14.99% [14.99–14.99] |
| msr_hm_0 | LRU | 11.40% [11.40–11.40] |
| msr_hm_0 | Random | 12.62% [12.60–12.64] |
| msr_hm_0 | S3-FIFO | 15.84% [15.84–15.84] |
| msr_hm_0 | SIEVE | 15.19% [15.19–15.19] |
| msr_hm_0 | TTL | 11.40% [11.40–11.40] |
| msr_hm_0 | W-TinyLFU | 16.05% [15.81–16.33] |
| msr_prn_0 | 2Q | 1.04% [1.04–1.04] |
| msr_prn_0 | ARC | 1.02% [1.02–1.02] |
| msr_prn_0 | LFU | 1.06% [1.06–1.06] |
| msr_prn_0 | LRU | 1.00% [1.00–1.00] |
| msr_prn_0 | Random | 0.94% [0.94–0.95] |
| msr_prn_0 | S3-FIFO | 1.01% [1.01–1.01] |
| msr_prn_0 | SIEVE | 1.06% [1.06–1.06] |
| msr_prn_0 | TTL | 1.00% [1.00–1.00] |
| msr_prn_0 | W-TinyLFU | 0.75% [0.67–0.93] |
| msr_proj_0 | 2Q | 5.38% [5.38–5.38] |
| msr_proj_0 | ARC | 5.38% [5.38–5.38] |
| msr_proj_0 | LFU | 4.73% [4.73–4.73] |
| msr_proj_0 | LRU | 5.35% [5.35–5.35] |
| msr_proj_0 | Random | 5.20% [5.20–5.22] |
| msr_proj_0 | S3-FIFO | 5.79% [5.79–5.79] |
| msr_proj_0 | SIEVE | 4.73% [4.73–4.73] |
| msr_proj_0 | TTL | 5.35% [5.35–5.35] |
| msr_proj_0 | W-TinyLFU | 4.26% [4.11–5.06] |
| msr_src1_2 | 2Q | 1.77% [1.77–1.77] |
| msr_src1_2 | ARC | 1.73% [1.73–1.73] |
| msr_src1_2 | LFU | 1.42% [1.42–1.42] |
| msr_src1_2 | LRU | 1.70% [1.70–1.70] |
| msr_src1_2 | Random | 1.63% [1.63–1.64] |
| msr_src1_2 | S3-FIFO | 1.70% [1.70–1.70] |
| msr_src1_2 | SIEVE | 1.42% [1.42–1.42] |
| msr_src1_2 | TTL | 1.70% [1.70–1.70] |
| msr_src1_2 | W-TinyLFU | 1.15% [0.91–1.22] |
| msr_usr_0 | 2Q | 3.57% [3.57–3.57] |
| msr_usr_0 | ARC | 3.55% [3.55–3.55] |
| msr_usr_0 | LFU | 1.46% [1.46–1.46] |
| msr_usr_0 | LRU | 3.57% [3.57–3.57] |
| msr_usr_0 | Random | 3.49% [3.49–3.50] |
| msr_usr_0 | S3-FIFO | 3.58% [3.58–3.58] |
| msr_usr_0 | SIEVE | 1.46% [1.46–1.46] |
| msr_usr_0 | TTL | 3.57% [3.57–3.57] |
| msr_usr_0 | W-TinyLFU | 2.86% [2.23–3.00] |
| msr_web_0 | 2Q | 2.61% [2.61–2.61] |
| msr_web_0 | ARC | 2.60% [2.60–2.60] |
| msr_web_0 | LFU | 2.61% [2.61–2.61] |
| msr_web_0 | LRU | 2.30% [2.30–2.30] |
| msr_web_0 | Random | 2.56% [2.55–2.57] |
| msr_web_0 | S3-FIFO | 2.49% [2.49–2.49] |
| msr_web_0 | SIEVE | 2.61% [2.61–2.61] |
| msr_web_0 | TTL | 2.30% [2.30–2.30] |
| msr_web_0 | W-TinyLFU | 2.28% [2.24–2.32] |

## ObserveOnly

LRU remains active, all nine arms are measured, 20 request epochs per replay, the same sample/floor settings.
Every run asserts serving hits equal standalone LRU. Advice rankings measure sampled shadows; they are not full-cache forecasts.
The table counts the final Advice.Best choices across 15 runs; all per-arm hit/miss reports are in traces.json.

| Trace | Serving hit rate | Recommended policies (count) |
| --- | --- | --- |
| twitter_cluster052.csv | 58.39% [58.39–58.39] | 2Q: 1, S3-FIFO: 1, SIEVE: 4, W-TinyLFU: 9 |
| lirs_loop.trace | 0.00% [0.00–0.00] | W-TinyLFU: 15 |
| lirs_2_pools.trace | 54.41% [54.41–54.41] | LFU: 2, LRU: 4, S3-FIFO: 2, TTL: 1, W-TinyLFU: 6 |
| arc_p3 | 1.87% [1.87–1.87] | W-TinyLFU: 15 |
| arc_oltp | 67.06% [67.06–67.06] | 2Q: 14, ARC: 1 |
| meta_kvcache_202206_1 | 66.39% [66.39–66.39] | W-TinyLFU: 15 |
| msr_hm_0 | 11.40% [11.40–11.40] | 2Q: 9, W-TinyLFU: 6 |
| msr_prn_0 | 1.00% [1.00–1.00] | LFU: 15 |
| msr_proj_0 | 5.35% [5.35–5.35] | S3-FIFO: 7, W-TinyLFU: 8 |
| msr_src1_2 | 1.70% [1.70–1.70] | 2Q: 15 |
| msr_usr_0 | 3.57% [3.57–3.57] | 2Q: 3, LRU: 2, S3-FIFO: 9, TTL: 1 |
| msr_web_0 | 2.30% [2.30–2.30] | 2Q: 9, ARC: 1, LFU: 5 |

Retrospective modal agreement: 8/12 traces. Each trace counts once, and all tied modal choices must belong to the tied best-fixed set to count as agreement.
Individual final recommendations in the best-fixed set: 114/180 (63.33%). Every run counts once; any tied best-fixed arm counts as agreement.
These compare final sampled Advice choices with standalone full-cache medians in this dataset, not forecast accuracy or a causal cost of following Advice. Serving remained LRU.

Modal mismatches (each tied nonwinning mode has its own row):

| Trace | Modal recommendation: standalone median [min–max] | Best fixed median [min–max] | Standalone median difference |
| --- | --- | --- | --- |
| twitter_cluster052.csv | W-TinyLFU 57.61% [54.48–58.76] | SIEVE 59.78% [59.78–59.78] | -2.1785 pp |
| meta_kvcache_202206_1 | W-TinyLFU 68.53% [68.31–68.72] | S3-FIFO 69.05% [69.05–69.05] | -0.5257 pp |
| msr_proj_0 | W-TinyLFU 4.26% [4.11–5.06] | S3-FIFO 5.79% [5.79–5.79] | -1.5390 pp |
| msr_web_0 | 2Q 2.61% [2.61–2.61] | LFU 2.61% [2.61–2.61]<br>SIEVE 2.61% [2.61–2.61] | -0.0002 pp |

For example, on twitter_cluster052.csv the modal W-TinyLFU recommendation has a standalone median -2.1785 points relative to the best fixed median. This subtraction compares separate full-cache replays; the sampled ObserveOnly cache did not switch to W-TinyLFU or measure that difference as a serving loss.

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
| 10 | cold | False | 12.10% [11.74–12.39] |
| 10 | cold | True | 7.52% [4.33–7.75] |
| 10 | warm | False | 12.46% [12.09–12.56] |
| 10 | warm | True | 7.93% [7.44–8.27] |
| 20 | cold | False | 13.25% [11.10–13.74] |
| 20 | cold | True | 9.04% [7.35–10.45] |
| 20 | warm | False | 12.83% [12.29–13.12] |
| 20 | warm | True | 9.30% [8.98–10.52] |
| 50 | cold | False | 11.87% [3.71–12.27] |
| 50 | cold | True | 10.03% [7.79–12.26] |
| 50 | warm | False | 12.23% [8.40–13.26] |
| 50 | warm | True | 10.72% [8.31–12.39] |

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
