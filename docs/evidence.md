# Evidence

Every claim in the README comes from here. `make evidence` replays a suite of
deterministic workloads against every policy and against the adaptive cache.
The numbers below are from an M1 Max, cache capacity 500, 200k requests per
workload. Reproduce with `make evidence`; the generators are in
[bench/workload.go](../bench/workload.go).

Hit rate by policy and workload:

| Workload | LRU | LFU | 2Q | ARC | Random | W-TinyLFU |
| --- | --- | --- | --- | --- | --- | --- |
| zipf (skewed popularity) | 66.9% | **73.5%** | 72.0% | 73.2% | 62.6% | 73.3% |
| uniform (no structure) | 10.0% | 10.0% | 10.0% | 10.0% | 10.1% | **12.3%** |
| loop (cycle just over capacity) | 0.0% | 0.0% | 68.6% | 0.1% | 82.1% | **94.0%** |
| scan (hot set + sweeps) | 30.0% | **40.0%** | **40.0%** | **40.0%** | 32.0% | 39.7% |
| phase-shift (alternating regimes) | 34.5% | 69.7% | 61.5% | 39.9% | 68.2% | **82.1%** |

Two things stand out. LRU and LFU both score **exactly zero** on `loop`, where a
cyclic scan just over capacity evicts every key immediately before it is needed
again -- that is the textbook pathology, and it is worth knowing your workload
is not that shape. And W-TinyLFU wins or ties nearly everywhere here.

## Memory and per-operation cost

Running N policies in parallel does not multiply memory by N, because shadow
policies hold keys and eviction bookkeeping but never real values. Measured
with six policies over 50k entries of 256-byte values:

| Configuration | Memory | Multiplier |
| --- | --- | --- |
| single LRU | 18.5 MiB | 1.00x |
| adaptive, 6 policies | 48.9 MiB | 2.65x |
| adaptive, 6 policies, `ShadowSampleRate: 0.05` | 24.5 MiB | 1.32x |

Per-operation cost on a warm cache, same configurations (`Get`, 0 allocs/op
throughout):

| Configuration | ns/op | allocs/op |
| --- | --- | --- |
| single LRU | 32 | 0 |
| adaptive, 6 policies | 618 | 0 |
| adaptive, 6 policies, sampled | 82 | 0 |

The shadow fan-out is broken down further in
[configuration](configuration.md#reducing-shadow-overhead).

## How does it compare with other Go cache libraries?

The tables elsewhere in this document compare this repository's policies with
each other, which is the wrong comparison for anyone choosing a package. Here
is the other one: the same workloads replayed through the caches a Go user
would actually reach for, at capacity 500, `make evidence`.

| Workload | otter v2 | theine | ristretto | sturdyc | as-cache |
| --- | --- | --- | --- | --- | --- |
| zipf | **73.19%** | 72.38% | 69.54% | 62.00% | 71.92% |
| uniform | 9.99% | **10.48%** | 9.97% | 9.52% | 10.23% |
| loop | 87.06% | 88.48% | **88.62%** | 45.42% | 68.78% |
| scan | **39.88%** | **39.88%** | 39.45% | 30.01% | 39.24% |
| phase-shift | 78.34% | **78.46%** | 72.53% | 53.19% | 75.06% |

**Adaptive selection does not win here.** It is within a point of the best
library on three of five workloads, loses phase-shift by 3.4 points, and loses
`loop` by nearly 20. It is also 4 to 15 times slower per operation, as the cost
table above describes. If you are choosing a cache library and have no
particular reason to expect your traffic to change shape, otter or theine is
the better answer, and this repository is the wrong place to pretend otherwise.

What the comparison does not show is any workload where a fixed library is
catastrophic, because these five are kind: `loop` is the one designed to defeat
LRU, and W-TinyLFU-derived caches handle it well. The case for measuring your
own traffic rests on real traces, where [the best policy changes by
trace](#real-traces).

**Two methodology notes**, because both would otherwise flatter someone.

otter admits on the caller's goroutine and evicts on a maintenance pass, so a
replay writing flat out leaves it far over capacity: 5000 keys written into a
cache built for 500 left 1916 retrievable. Uncorrected, that made otter look
like it served 44% on uniform traffic where every other cache served 10% - a
decisive-looking win that was purely the extra capacity. The harness calls
`CleanUp` so the comparison happens at the stated size, at some cost to otter's
timing column, and a test fails if any cache drifts far over its capacity
again.

ristretto's `Set` is asynchronous and admission-gated: it can return having
queued nothing, so keys written into an almost-empty cache are not all there
afterwards. Its hit rate is what a caller experiences, which is the honest
thing to measure, but it is not purely an eviction-policy comparison.

## Does adaptive selection beat picking one policy?

On these workloads: **no, and this is the honest result.**

| Workload | Adaptive | Best fixed | Worst fixed | Adaptive vs best |
| --- | --- | --- | --- | --- |
| zipf | 73.3% | LFU 73.5% | 62.6% | -0.2 pts |
| uniform | 10.0% | W-TinyLFU 12.3% | 10.0% | -2.3 pts |
| loop | 77.5% | W-TinyLFU 94.0% | 0.0% | -16.5 pts |
| scan | 38.9% | LFU/2Q/ARC 40.0% | 30.0% | -1.1 pts |
| phase-shift | 78.8% | W-TinyLFU 82.1% | 34.5% | -3.3 pts |

Adaptive selection reliably beats the *worst* fixed choice, sometimes hugely
(77.5% against LRU's 0.0% on `loop`). It never meaningfully beats the *best*
one. Even on `phase-shift` -- the workload built specifically to need adaptation
-- a fixed W-TinyLFU wins by 3.8 points.

The timeline explains why. Replaying `phase-shift` and sampling `ActivePolicy()`
throughout:

```text
phase      Z------L------Z------L------Z------L------Z------L------   (Z = zipf, L = loop)
LRU        ###
TwoQueue      #######
ARC                  ##
TinyLFU          #########################################################

share of time active: LRU 2%, TwoQueue 6%, ARC 1%, TinyLFU 90%
```

The bandit works exactly as designed: it explores, identifies W-TinyLFU, and
holds it for 90% of the run. It does not oscillate at phase boundaries, because
there is no crossover to exploit -- W-TinyLFU is the best arm in *both* regimes.
The remaining gap is the price of exploring and of migrating between arms.

So the case for this library is not "it beats the best policy." It is:

- **You do not know which policy is best for your traffic**, and the cost of
  guessing wrong is large (0.0% vs 94.0% on `loop`). Adaptive selection bounds
  that downside without requiring you to know.
- **It tells you what to use.** The most valuable output may be the measurement
  rather than the switching -- see [advisor mode](advisor-mode.md).

For a workload that genuinely crosses over, the picture could differ. These are
synthetic, and the section below shows real traces overturning the conclusion.

## Real traces

`./scripts/fetch-traces.sh` downloads five published traces (nothing is
committed), then `AS_CACHE_TRACES=... make evidence` replays them. Adaptive here
runs a 50ms epoch with warm migration and `ShadowSampleRate: 0.05`:

| Trace | Requests | Best fixed | Worst fixed | Adaptive | Delta |
| --- | --- | --- | --- | --- | --- |
| Twitter Twemcache cluster052 | 1.0M | 2Q 59.6% | LFU 41.4% | 59.4% | -0.25 pts |
| ARC OLTP (FAST '03) | 0.9M | 2Q 68.3% | LFU 45.4% | 67.1% | -1.19 pts |
| ARC P3 (FAST '03) | 2.0M | W-TinyLFU 11.7% | LRU 1.9% | **12.7%** | **+0.92 pts** |
| LIRS 2_pools | 100k | W-TinyLFU 54.8% | Random 50.1% | 54.4% | -0.36 pts |
| LIRS loop | 505k | W-TinyLFU 45.9% | LRU/LFU 0.0% | 42.5%* | -3.43 pts |

\* `loop` needs a 2ms epoch: it is short and changes character quickly, so a
50ms epoch gives the bandit too few epochs to react and it drops to 33.3%.

Note that the best fixed policy is **not the same policy across traces**. On
OLTP, W-TinyLFU -- the strongest general-purpose baseline -- comes second to
last at 63.2% while 2Q wins at 68.3%. That is the case for not committing to a
policy in advance, and it does not show up on synthetic workloads, where
W-TinyLFU wins nearly everything.

LFU is the sharpest illustration of why synthetic workloads mislead. It is the
**best** policy on synthetic `zipf` (73.5%) and the **worst** on both large real
traces (41.4% on Twitter, 45.4% on OLTP). Synthetic Zipf holds popularity
*stationary*, which is exactly the assumption classic LFU makes; real traffic
shifts, and an entry that was hot once keeps a frequency count that holds it
resident long after it stops being useful. That is the failure W-TinyLFU's aged
frequency sketch exists to avoid, and it is invisible until you replay real
traffic.

How much configuration moves these numbers is in
[configuration](configuration.md#tuning-measured); the short version is that a
too-short epoch costs up to 7 points and 30x the per-operation time.

## Does sampling distort the comparison?

Sampled shadows are only sound if a miniature ranks policies the way full-size
shadows would. Measured directly across four sample rates, against full-size
shadows as ground truth:

```text
zipf   full-size  ARC=81.4% 2Q=81.2% LFU=81.0% TTL=79.2% LRU=79.2% W-TinyLFU=79.1% Random=22.5%
       rate 0.05  ARC=66.1% 2Q=66.0% LFU=65.6% W-TinyLFU=64.4% LRU=62.5% TTL=61.6% Random=22.2%
       rate 0.10  ARC=85.4% 2Q=85.4% LFU=85.2% W-TinyLFU=84.1% TTL=83.8% LRU=83.7% Random=38.1%
       rate 0.30  ARC=77.7% 2Q=77.5% LFU=77.4% W-TinyLFU=76.5% LRU=75.3% TTL=75.0% Random=20.8%
       rate 0.50  ARC=84.7% 2Q=84.6% LFU=84.4% W-TinyLFU=82.9% LRU=82.9% TTL=82.9% Random=22.3%

scan   full-size  2Q=28.3% ARC=28.3% LFU=28.3% W-TinyLFU=27.2% TTL=21.4% LRU=21.4% Random=17.0%
       rate 0.05  2Q=26.4% ARC=26.4% LFU=26.4% W-TinyLFU=24.0% TTL=19.9% LRU=19.9% Random=16.2%
       rate 0.10  2Q=29.6% ARC=29.6% LFU=29.6% W-TinyLFU=28.9% LRU=22.4% TTL=22.4% Random=17.7%
       rate 0.30  2Q=28.9% ARC=28.9% LFU=28.9% W-TinyLFU=27.8% LRU=21.8% TTL=21.8% Random=17.3%
       rate 0.50  2Q=28.2% ARC=28.2% LFU=28.2% W-TinyLFU=25.8% TTL=21.4% LRU=21.4% Random=17.0%
```

**Sampling picks the same best policy at every rate**, on both workloads --
zero regret, including at the aggressive 5%. Every clearly separated pair of
arms is ranked the same way sampled as full-size: 0 inversions out of 3 pairs
on zipf and 6 on scan, at all four rates.

What sampling does *not* give you is an estimate of the absolute hit rate. Read
the zipf rows down the rate column: ARC measures 66% at rate 0.05 and 85% at
rate 0.10, against 81% full-size. The estimate depends on which slice of the
keyspace the seed happened to select, and a different slice has different
reuse, so a sampled rate can land either side of the true one. Do not read a
shadow's absolute number as a prediction of what that policy would achieve.

That is fine for the purpose, because the bandit only ever needs to know which
arm is better, never by how much in absolute terms. It is not fine if you were
planning to quote a shadow's hit rate as a forecast -- for that, run the policy
for real, or set `ShadowSampleRate` to 0 and pay for full-size shadows.

Higher rates cost more and buy no better ranking here, so 0.05 is a reasonable
default. Raise it if your keyspace is small enough that 5% of it is only a
handful of keys -- `MinShadowCapacity` guards the degenerate end by raising the
effective rate rather than letting a miniature shrink into noise.

## Does pooling across a fleet help?

**Only in the regime it was built for, and it is worth checking you are in that
regime before turning it on.** All figures are 8 replicas, cache capacity 300
to 500, `make evidence`. The mechanism is described in [running a
fleet](fleet.md).

The case it exists for is a replica that sees too little traffic per epoch to
rank its own arms. Reproducing that requires holding each replica to a request
rate — an unpaced replay delivers thousands of requests per epoch however small
the workload is, it just finishes sooner. Paced to roughly 8 requests per cache
epoch per replica:

| Setup | Hit rate | Policies in use at the end |
| --- | --- | --- |
| best fixed (ARC) | 62.8% | 1 |
| pooled, leader-elected | 58.3-59.5% | 1-2 |
| each replica deciding alone | 55.5-55.9% | 5 |

**Pooling gains 2.3 to 3.9 points** over independent replicas, across four
runs. The last column is the mechanism: a replica with eight requests an epoch
cannot tell its arms apart, so the fleet scatters across five different
policies, several of them poor. Pooled, the fleet has 64 requests an epoch of
evidence and stays on one.

Now the same comparison where replicas are *not* starved — the unpaced replays
every other measurement here uses:

| Workload | Pooled | Deciding alone | Best fixed |
| --- | --- | --- | --- |
| zipf, split evenly | 68.2% | 70.4% | 73.0% |
| zipf, sharded by key | 86.2% | 87.4% | LFU 88.2% |
| phase-shift | 82.0% | 82.0% | 2Q 83.0% |
| mixed fleet (half loop, half zipf) | 36.6% | 41.7% | — |

**Pooling loses whenever the replicas could already measure for themselves**,
by 1 to 2 points on uniform traffic and by 5.1 points on a fleet whose replicas
serve different workloads. The mixed-fleet row is the clearest: a fleet-wide
decision is a compromise, and when half your replicas want the policy the other
half are worst served by, forcing agreement costs more than the disagreement
did.

Most of the loss on uniform traffic is the fleet simply getting fewer chances
to change its mind:

```text
local (no coordination):     70.42%
coordination epoch 10ms:     70.01%  (-0.41)
coordination epoch 25ms:     69.75%  (-0.67)
coordination epoch 50ms:     68.20%  (-2.21)
coordination epoch 200ms:    66.95%  (-3.47)
```

The gap closes monotonically as coordination speeds up — but it closes towards
break-even, never past it, and a 10ms coordination epoch is where a real round
trip stops being negligible. Note that these replays coordinate through an
in-process store, so coordination is free in a way it will not be for you: the
setting that looks best here is the one that costs most to run.

**So the rule is:** pool when your replicas are individually starved of
traffic, run the same workload shape as each other, and are numerous enough
that the pooled evidence is meaningfully thicker. Otherwise let each replica
decide alone — it is simpler, it needs no store, and on this evidence it is
also better. `Advice()` in [observe-only mode](advisor-mode.md) will tell you
which case you are in before you deploy anything.
