# Evidence

The [current results](../bench/results/current/README.md) are generated from
three consecutive full evidence runs on one clean commit. They include all
observations, input and output hashes, commands, tool versions and exit codes.
The [manifest](../bench/results/current/manifest.json) identifies the measured
source. New validated measurements replace this dataset.

## What the numbers say

- **Policy rankings depend on the workload and configuration.** Read the
  margins and ties alongside the winning names. Several MSR rankings differ
  by less than 0.04 percentage points; LFU and SIEVE tie on two volumes.
- **Adaptive selection has no demonstrated universal advantage or floor.**
  The matrix retains outcomes below fixed baselines. Relative performance is
  reported, not used to accept or reject a trace-matrix result.
- **Sampling and asynchronous maintenance introduce uncertainty.** A median
  and observed range summarize this dataset; neither bounds future behavior.
  A short W-TinyLFU batch can miss another performance mode on LIRS loop.
- **Shadows still require work and storage.** The library measures multiple
  policies. Timings and memory depend on the host, payload and workload; raw
  diagnostics are retained without presenting single-run costs as stable claims.

This is an experimental tool for studying selection and measurement. Evaluate
fixed-cache alternatives on your traffic before enabling automatic switching.
The trace experiments are not a production-service validation.

## Fixed policies on synthetic workloads

`TestFixedPolicyEvidence` replays the generators in
[bench/workload.go](../bench/workload.go). Its output is retained in the three
current evidence logs. Deterministic arms can repeat exactly; Random and
W-TinyLFU cannot. The trace matrix reports their repeated results explicitly.

## Memory and per-operation cost

`TestMemoryMultiplier` compares a full LRU with eight-policy adaptive caches,
with and without sampling, using 50,000 entries of 256-byte payloads.
`TestAllocationsPerOperation` measures a warm-cache Get. These are diagnostic
experiments, separate from the nine-arm trace matrix; run them on your target
machine to measure overhead. Their raw outputs remain in
[evidence-1.log](../bench/results/current/evidence-1.log),
[evidence-2.log](../bench/results/current/evidence-2.log) and
[evidence-3.log](../bench/results/current/evidence-3.log).

Shadows omit payload values but retain keys and eviction metadata. FIFO adapters
also maintain a key index; S3-FIFO retains ghost keys. This is not an equal-memory
comparison between algorithms. Sampling reduces fan-out work but does not remove
its dependence on the number of arms.

## How does it compare with other Go cache libraries?

`TestAgainstOtherLibraries` compares caller-visible behavior at nominal capacity
500. Its adaptive subject uses 2,000-request epochs, warm migration, all nine
arms and no sampling. Competing implementations have different admission and
maintenance behavior, so equal nominal capacity does not establish equal memory.

The harness calls otter's `CleanUp` to enforce capacity, and that work contributes
to its timing. Theine and ristretto may retain counts different from the nominal
capacity; ristretto's asynchronous, admission-gated Set need not retain a new
value. Consult all three current logs, not a single table of stable timings or
retained-entry counts. Versions and adapters are pinned in
[bench/go.mod](../bench/go.mod) and [bench/competitors.go](../bench/competitors.go).

## Does adaptive selection beat picking one policy?

The [request-counted matrix](../bench/results/current/README.md#trace-matrix)
compares each adaptive setting with the best fixed median on the same trace.
It reports all three epoch settings, not just whichever looks best. Its generated
summary counts how many traces trail the best fixed median at each setting and
across all settings; those counts are observations from the current dataset.
No maximum
future deficit, statistical significance or guaranteed improvement follows
from these observed ranges.

The suite also prints exploratory wall-clock experiments. Their scheduling and
policy-residency output are raw diagnostics, not the basis of the conclusions
here or a retained historical visualization.

## Real traces

The [raw JSON](../bench/results/current/traces.json) contains twelve traces,
all nine fixed policies and adaptive runs at 10/20/50 request epochs.
Each nondeterministic subject has fifteen observations from three batches of
five; deterministic fixed arms run once per batch. Adaptive settings use
warm migration, requested sampling 0.05 and a minimum shadow capacity of 64.
The floor is an explicit experimental choice, below the library default 256.
It permits smaller shadows closer to the requested 5% rate on the small LIRS
caches and must not be described as the default production configuration.

The [context table](../bench/results/current/README.md#workload-context) reports
requests, distinct keys, entry capacity, capacity/keyspace, effective sampling,
compulsory-miss ceilings and fixed-policy margins. Effective sampling is 12.8%
on LIRS loop and 6.4% on 2_pools; it is 5% on the other traces. With the default
floor 256, the two LIRS rates would instead be 51.2% and 25.6%.

Most MSR prefixes have roughly one cache entry per hundred distinct keys. Their
compulsory misses leave limited headroom: the cold-cache hit ceiling is
`1 - distinct_keys / requests`. MSR prn/web have exact leading LFU/SIEVE ties,
and src1_2/usr have best-versus-runner-up margins below 0.04 points. Such rankings
are not evidence of a practically significant policy advantage.

### Limits of this comparison

These are bounded request prefixes with empty initial caches and entry-count
capacities, not whole-file replays or equal-memory measurements. TTL is one hour,
longer than a replay, so its trace result tests LRU-like retention rather than
expiry under production arrival times. Random has an uncontrolled seed; W-TinyLFU
maintains its cache asynchronously and may exceed nominal capacity.

The [Meta size experiment](../bench/results/current/README.md#meta-object-count-versus-byte-capacity)
replays the same GET prefix through libCacheSim LRU with object sizes ignored
and accounted for. The [CacheLib reader](https://github.com/facebook/CacheLib/blob/main/cachelib/cachebench/workload/KVReplayGenerator.h)
parses payload size and original key size separately. Our experiment records
sizes as `size + key_size` and gives an explicit conversion from object capacity to byte budget. The export preserves each
request's size, but this simulator charges the size at insertion until eviction;
size changes on hits do not resize a resident object. Its deltas are **request miss
ratios**, not byte-weighted miss ratios; metadata overhead remains excluded.
That experiment does not turn the nine-policy table into a byte-budget comparison.

Reference calibration pins libCacheSim to
`1d7415569978330ea95c9cff06a260630406f7e3`. It compares independent expansions of
the same documented trace interpretation, then independent LRU counting at five
capacities. A shared interpretation error can survive this check. Every trace
read by the suite must have coverage, request counts must match, and the miss
ratio tolerance is 0.0051 percentage points. Reference ratios must be finite,
in [0, 1], with exactly four decimals; coarser input cannot widen the tolerance.

<a id="one-loop-row-two-answers-one-run"></a>

### One `loop` row, different answers

W-TinyLFU's asynchronous behavior makes short batches sensitive to which mode
is sampled. The full current observations are retained; no range from an older
iteration is mixed into them. In particular, the largest gap in one batch is
not a general maximum deficit for adaptive selection.

<a id="the-two-fifo-policies-near-identical-on-key-value-traffic-far-apart-elsewhere"></a>

### The two FIFO policies

S3-FIFO and SIEVE remain experimental adapters planned for v0.5, outside the
v0.4 module release. Their current trace results appear alongside every other
arm; their costs and semantics are documented in [policies](policies.md).

LFU and SIEVE have identical hit/miss decisions on eight selected prefixes.
The [diagnostic table](../bench/results/current/README.md#lfusieve-diagnostic)
checks SIEVE against an independent visited-bit/hand model and also replays a
plain FIFO control. The FIFO totals differ on seven of those eight prefixes,
so the equality cannot be explained by a general reduction of both to FIFO.
Their different eviction mechanisms happen not to change hit/miss decisions
on those streams at those capacities; this does not imply equal eviction state.

`TestSieveLFUDistinction` separates the mechanisms with worked examples. At
capacity two, `a,a,b,b,c,d,a,b` gives LFU three hits and SIEVE two. On
`a,b,a,c,a`, SIEVE gets two hits and FIFO one. A cold scan gives all three zero.
The trace-by-trace reference check rejects an adapter that stops following
the visited-bit model even if its aggregate hit count happens to match LFU.

### What the library adapter costs

FIFO adapters maintain an extra key index, and rebuild on resize. Rebuilds lose
learned eviction state; rewriting existing values counts as access in the
upstream library. These are adapter limitations, not properties established
by the original algorithms' papers.

## Does sampling distort the comparison?

Yes, it can change both absolute rates and rankings. Sharing a sampled keyspace
and keeping counts unscaled prevents inflated sample counts; it does not make
miniature shadows unbiased models of full caches. The keyspace sampling rate
also differs from the fraction of requests sampled under skewed traffic.

The [ObserveOnly sweep](../bench/results/current/README.md#observeonly) holds LRU
active on all twelve traces, measures all nine policies and records every final
Advice report. Serving hits must exactly match standalone LRU. Advice measures
sampled shadow behavior, not the hit rate guaranteed after a policy switch.
The generated table also compares final recommendations retrospectively with
the best standalone full-cache medians, retaining ties and every run. Its modal
mismatches include worked median differences; these are not measured serving
losses from following advice. This offline sweep is separate from the still-pending
real-service trial.

## What does a switch cost right after it?

`TestSwitchWarmupCost` uses a scripted switch and counts hits in successive
request windows. Its output is in the three current logs. Cold migration
requires refill; warm migration transfers values but not all eviction history;
gradual migration keeps a source alive until the window closes. None guarantees
zero extra misses on arbitrary traffic.

The [P3 tuning table](../bench/results/current/README.md#p3-tuning) compares
cold/warm migration with gates on/off at 10/20/50 request epochs, with fifteen
observations per cell. It is one workload's configuration example, not a
universal production recommendation. See [configuration](configuration.md#tuning-measured).
