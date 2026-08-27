# Evidence

Every measured claim in this repository comes from here. `make evidence`
replays a suite of deterministic workloads against every policy and against the
adaptive cache. The numbers below are from an M1 Max, cache capacity 500, 200k
requests per workload. Reproduce with `make evidence`; the generators are in
[bench/workload.go](../bench/workload.go).

## What the numbers say

Four findings, each with its own section below.

1. **No single policy wins everywhere.** Across six published traces the best
   fixed policy is a different one four times over, and the strongest
   general-purpose baseline lands near the bottom on one of them —
   [real traces](#real-traces).
2. **Adaptive selection roughly matches the best fixed policy without being
   told which it is**: it beats it on two of the six traces and lands within
   1.4 points on the other four. On synthetic workloads it does not manage
   that — [against fixed policies](#does-adaptive-selection-beat-picking-one-policy).
3. **Memory does not multiply by the number of arms.** Shadows hold keys and
   eviction bookkeeping but never values: eight policies cost 3.92x a single
   LRU, or 1.40x with sampling on —
   [memory and per-operation cost](#memory-and-per-operation-cost).
4. **The hot path is not free.** 32 ns/op for a bare LRU against 90 sampled and
   856 unsampled, which is the price of the measurement.

Configuration moves these numbers more than the choice of arms does; see
[tuning](configuration.md#tuning-measured) before drawing conclusions from your
own run.

Hit rate by policy and workload:

| Workload | LRU / TTL | LFU | 2Q | ARC | Random | W-TinyLFU | S3-FIFO | SIEVE |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| zipf (skewed popularity) | 66.9% | 73.5% | 72.0% | 73.2% | 62.5% | 72.7% | 73.5% | **73.6%** |
| uniform (no structure) | 10.0% | 10.0% | 10.0% | 10.0% | 10.0% | **12.3%** | 10.0% | 10.0% |
| loop (cycle just over capacity) | 0.0% | 0.0% | 68.6% | 0.1% | 82.2% | **92.8%** | 79.7% | 0.0% |
| scan (hot set + sweeps) | 30.0% | **40.0%** | **40.0%** | **40.0%** | 32.0% | 39.8% | **40.0%** | **40.0%** |
| phase-shift (alternating regimes) | 34.5% | 69.7% | 61.5% | 39.9% | 68.2% | **82.4%** | 71.6% | 69.7% |

TTL shares a column with LRU because these workloads carry no notion of
staleness and its TTL is longer than any run, so it measures its LRU behaviour
exactly -- identically, to the hundredth of a point, on all five.

Every arm here reproduces to the hundredth of a point between runs except two.
W-TinyLFU does not: on `loop` it has measured 88.7% and 94.5% within a single
process. Neither does `Random`, which seeds itself from the global source —
that is what a control arm is for, but it means its column moves too. Read its column, and any delta computed against it, with that
in mind — the cause is in [reproducible replays](benchmarking.md).

Two things stand out. LRU and LFU both score **exactly zero** on `loop`, where a
cyclic scan just over capacity evicts every key immediately before it is needed
again -- that is the textbook pathology, and it is worth knowing your workload
is not that shape. **SIEVE joins them at exactly zero** for a different reason:
it has no ghost queue, so a key evicted on the hand's first pass leaves no
trace at all, and a cyclic workload never gets a second chance. S3-FIFO, whose
ghost queue does give one, serves 79.7% on the same workload. That is the
clearest single difference between the two FIFO policies in this repository.

Otherwise W-TinyLFU wins or ties nearly everywhere here, with the two FIFO
policies close behind and SIEVE ahead of everything on `zipf`. These synthetic
workloads understate both; the real traces below correct that, which is the
same lesson LFU teaches in the opposite direction.

## Memory and per-operation cost

Running N policies in parallel does not multiply memory by N, because shadow
policies hold keys and eviction bookkeeping but never real values. Measured
with eight policies over 50k entries of 256-byte values:

| Configuration | Memory | Multiplier |
| --- | --- | --- |
| single LRU | 18.5 MiB | 1.00x |
| adaptive, 8 policies | 72.3 MiB | 3.92x |
| adaptive, 8 policies, `ShadowSampleRate: 0.05` | 25.8 MiB | 1.40x |

Per-operation cost on a warm cache, same configurations (`Get`, 0 allocs/op
throughout):

| Configuration | ns/op | allocs/op |
| --- | --- | --- |
| single LRU | 32 | 0 |
| adaptive, 8 policies | 856 | 0 |
| adaptive, 8 policies, sampled | 90 | 0 |

**What an arm costs, measured.** The same test at six policies -- the set
before the FIFO arms joined it -- reported 48.9 MiB (2.65x) and 24.5 MiB
(1.33x) sampled. The two FIFO arms added 23.4 MiB between them, against an
average of 6.1 MiB for the five shadows already there: **each is close to
double an ordinary arm**. Two things account for it, and both are consequences
of wrapping a library rather than of the algorithms. The adapter keeps its own
copy of the key set, because `golang-fifo` cannot enumerate its own contents,
and S3-FIFO's ghost queue remembers roughly a further cache's worth of keys.
Values are never duplicated by either.

The property that actually matters is per-shadow, and it holds: each shadow
costs 7.7 MiB against the 18.5 MiB a full cache of the same entries costs --
0.42x. That is what "shadows hold keys and bookkeeping but never values" buys,
and it is what the test asserts, rather than a total multiplier that would
simply move every time an arm was added.

That is less than S3-FIFO's key count suggests. It tracks up to two keys per
entry of capacity, because the library sizes its ghost queue at the whole cache
capacity -- and a third, because this adapter keeps its own key index to supply
the methods upstream lacks. But a ghost entry is a *reference* to a key the caller already
allocated plus a pair of list pointers, never a copy of the key and never a
value. Counting ghost keys as though they cost what cached entries cost would
overstate this arm substantially.

The shadow fan-out is broken down further in
[configuration](configuration.md#reducing-shadow-overhead).

## How does it compare with other Go cache libraries?

The tables elsewhere in this document compare this repository's policies with
each other, which is the wrong comparison for anyone choosing a package. Here
is the other one: the same workloads replayed through the caches a Go user
would actually reach for, at capacity 500, `make evidence`.

| Workload | otter v2 | theine | ristretto | sturdyc | as-cache |
| --- | --- | --- | --- | --- | --- |
| zipf | **73.25%** | 72.84% | 69.47% | 62.01% | 67.88% |
| uniform | 10.01% | **10.53%** | 9.94% | 9.50% | 10.00% |
| loop | 86.73% | 88.56% | **88.85%** | 44.94% | 86.61% |
| scan | 39.85% | **39.88%** | 39.20% | 30.01% | 39.44% |
| phase-shift | **78.62%** | 77.67% | 72.27% | 53.18% | 77.70% |

**Adaptive selection does not win here, but it is now competitive.** It is
within half a point of the best library on `uniform` and `scan`, within 2.3 on
`loop`, and takes **second place on `phase-shift`** — ahead of theine and
ristretto, 0.9 behind otter. It loses `zipf` by 5.4. It is also 4 to 23 times
slower per operation, as the cost table above describes. If you are choosing a
cache library and have no particular reason to expect your traffic to change
shape, otter or theine is still the better answer, and this repository is the
wrong place to pretend otherwise.

Two of these numbers moved a long way when the shadow-insert defect described
under [real traces](#real-traces) was fixed: `loop` from 64.10% to 86.61% and
phase-shift from 71.51% to 77.70%. Both are workloads where the arms differ
sharply, which is exactly where feeding the shadows from the incumbent's miss
stream did the most damage.

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
| zipf | 66.2% | SIEVE 73.6% | 62.6% | -7.4 pts |
| uniform | 10.0% | W-TinyLFU 12.3% | 10.0% | -2.3 pts |
| loop | 87.1% | W-TinyLFU 93.2% | 0.0% | -6.1 pts |
| scan | 35.5% | LFU 40.0% | 30.0% | -4.4 pts |
| phase-shift | 71.8% | W-TinyLFU 82.6% | 34.5% | -10.9 pts |

Adaptive selection reliably beats the *worst* fixed choice, sometimes hugely
(87.1% against LRU's 0.0% on `loop`). It never meaningfully beats the *best*
one. Even on `phase-shift` -- the workload built specifically to need adaptation
-- a fixed W-TinyLFU wins by 10.9 points.

**Arms are not free**, and that is worth sitting with. Every arm added thins
the evidence each of the others gets per epoch, and the exploration is charged
against the hit rate. The real-trace figures below are far tighter than this
table, because those replays use a tuned 50ms epoch rather than the 2ms one
held fixed across every workload here.

The timeline says why, and it is not the answer this section used to give.
Replaying `phase-shift` and sampling `ActivePolicy()` throughout:

```text
share of time active: LRU 6%, LFU 4%, TwoQueue 15%, ARC 14%, TTL 4%,
                      TinyLFU 27%, S3FIFO 15%, SIEVE 16%
hit rate 63.77%
```

**The bandit does not settle.** Eight of the nine arms take a turn, the best of
them holds only 27% of the run, and the cache spends the rest of it changing
its mind. That is not a defect in the bandit; it is what honest evidence looks
like on this workload. `phase-shift` alternates between two regimes every
20,000 requests, several arms sit within a couple of points of each other in
both, and Thompson sampling explores exactly as it should when the posteriors
overlap. The cost of that exploration is the gap between 63.77% here and a
fixed W-TinyLFU's 82.6%.

Two things are worth saying plainly about this block. Earlier versions of this
document showed W-TinyLFU holding 82-90% of the same run and concluded that the
bandit "identifies W-TinyLFU and holds it"; that was measured while the shadow
mechanism was reporting rivals at rates they could not achieve, and it does not
reproduce. And the run still uses a wall-clock epoch, so the number of epochs
varies with machine load — read the shape (no arm dominates) as the finding and
the individual percentages as one draw.

So the case for this library is not "it beats the best policy." It is:

- **You do not know which policy is best for your traffic**, and the cost of
  guessing wrong is large (0.0% vs 94.0% on `loop`). Adaptive selection bounds
  that downside without requiring you to know.
- **It tells you what to use.** The most valuable output may be the measurement
  rather than the switching -- see [advisor mode](advisor-mode.md).

For a workload that genuinely crosses over, the picture could differ. These are
synthetic, and the section below shows real traces overturning the conclusion.

## Real traces

`./scripts/fetch-traces.sh` downloads published traces (nothing is committed),
then `AS_CACHE_TRACES=... make evidence` replays them. Adaptive here runs a 50ms
epoch with warm migration and `ShadowSampleRate: 0.05`:

| Trace | Requests | Best fixed | Worst fixed | Adaptive | Delta |
| --- | --- | --- | --- | --- | --- |
| Twitter Twemcache cluster052 | 1.0M | SIEVE 59.8% | LFU 41.4% | 58.6% | -1.15 pts |
| Meta kvcache 202206 | 2.0M | S3-FIFO 69.1% | Random 65.2% | 67.7% | -1.35 pts |
| ARC OLTP (FAST '03) | 0.9M | 2Q 68.3% | LFU 45.4% | 67.7% | -0.51 pts |
| ARC P3 (FAST '03) | 2.0M | W-TinyLFU 11.4% | LRU 1.9% | **11.4%** | **+0.05 pts** |
| LIRS 2_pools | 100k | W-TinyLFU 54.8% | Random 50.0% | 54.4% | -0.35 pts |
| LIRS loop | 505k | W-TinyLFU 45.1%* | seven arms at 0.0% | **45.2%** | **+0.12 pts** |

\* `loop` is the one row measured at a 2ms epoch. It is short and changes
character quickly, so the tuned 50ms setting gives the bandit too few chances
to react and it drops to 38.1%. Read the W-TinyLFU figure on this row with care
besides: it is the one arm here whose result is not reproducible, and on this
trace it has measured anywhere from 43.1% to 46.2%.

**Adaptive selection beats the best fixed policy on two of the six traces**,
by small margins, and lands within 1.4 points on the other four.

These numbers replace an earlier set measured with a defect in the shadow
mechanism: a shadow policy could only ever acquire a key the *active* policy
had missed, so behind a strong incumbent the shadows went static and reported
policies that serve nothing as though they served everything. The bandit was
choosing on inverted evidence. See [design](design.md) for the mechanism.

Be precise about what changed, because it is less dramatic than it sounds. This
document already reported adaptive selection beating the best fixed policy on
P3; that has not been overturned, though the margin shrank from +1.13 to +0.05.
What changed is `loop`, which went from **-7.45 to +0.12** — from the worst
result in the table to the second win. The overall picture is one trace better
than it was, and the *synthetic* conclusion below is unchanged: on those five
workloads adaptive selection still never beats the best fixed policy.

Note also that the best fixed policy is **not the same policy across traces**:
SIEVE on Twitter, S3-FIFO on Meta, 2Q on OLTP, W-TinyLFU on P3 and the LIRS
traces. Four different winners across six traces. On OLTP, W-TinyLFU -- the
strongest general-purpose baseline -- comes near the bottom. That is the case
for not committing to a policy in advance, and it does not show up on synthetic
workloads, where W-TinyLFU wins nearly everything.

### One `loop` row, two answers, one run

The clearest demonstration in this repository of why an arm has to be
reproducible. Replaying LIRS `loop` at capacity 500 against a bare W-TinyLFU
policy, **twice in the same `go test` invocation**, gave 45.24% and 46.16%.
Same trace, same capacity, same process, no bandit involved. otter admits on the calling goroutine and evicts on a maintenance
pass, so what it retains depends on how the run was scheduled, and on a cyclic
workload sitting exactly at the capacity boundary that decides almost every
request. Across runs the spread on this trace is wider still: an earlier run of
the same suite reported 43.13% and 94.94%.

Every other arm here replays identically. This is why `benchclient.DefaultArms`
excludes W-TinyLFU, why `ArmsWithWindowTinyLFU` makes including it an explicit
choice, and why S3-FIFO -- which is deterministic -- is in the default set.
(`Random` in that set is not deterministic either; see
[benchmarking](benchmarking.md).)

### The two FIFO policies: near-identical on key-value traffic, far apart elsewhere

S3-FIFO and SIEVE finish within 0.15 points of each other on four of the six
traces -- and SIEVE does it at roughly **half the per-operation cost**, because
it maintains one queue and a visited bit where S3-FIFO maintains three queues
and a counter:

| Trace | S3-FIFO | ns/op | SIEVE | ns/op |
| --- | --- | --- | --- | --- |
| Twitter | 59.73% | 500 | **59.78%** | 284 |
| Meta kvcache | **69.05%** | 371 | 68.92% | 204 |
| ARC OLTP | **67.79%** | 414 | 67.72% | 238 |
| LIRS 2_pools | **54.37%** | 371 | 54.36% | 216 |
| ARC P3 | **10.75%** | 770 | 4.82% | 496 |
| LIRS loop | 0.00% | 550 | 0.00% | 340 |

Then P3 separates them by six points, and the synthetic `loop` separates them
by eighty. Both gaps have the same cause: **S3-FIFO has a ghost queue and SIEVE
does not.** A key SIEVE evicts leaves no trace, so a workload whose reuse
arrives after eviction is invisible to it; S3-FIFO gets one more chance to
notice, within the window its ghost queue spans.

So they are not redundant, and neither dominates. On production key-value
traffic SIEVE is the better buy -- the same hit rate for half the work. On
block-I/O traces S3-FIFO is worth its extra bookkeeping. That is the argument
for measuring rather than choosing, made between two policies from the same
paper family.

Then there is `loop`, where it serves **0.00%** -- tied with LRU, LFU, 2Q, TTL,
ARC and SIEVE, and beaten by random eviction. That is the algorithm behaving
exactly as designed. `loop` cycles through 1011 keys with a 500-entry cache, so
every reuse distance is 1011 requests. A key has to be requested again while it
is still resident or still in the ghost queue to be promoted. The library sizes
that ghost queue at the *whole* cache capacity -- 500 here -- so the horizon is
roughly 1000 requests wide, and a reuse distance of 1011 falls just outside it.
Nothing is ever promoted to the main queue, so the small queue holds the whole
cache and every key cycles through it forever. (`size/10` is not a cap on that
queue: upstream uses it only to decide which of the two queues an eviction
comes from.) Only two arms survive the
trace at all: W-TinyLFU's sketch, which ages rather than expiring, and random
eviction, which has no order to defeat.

The lesson is not that S3-FIFO is fragile. It is that **its ghost window is a
hard horizon**: reuse further away than that window is invisible to it. On the
synthetic `loop`, whose cycle is 550 keys against the same 500-entry cache, it
serves 79.7%. The difference between those two numbers is entirely the reuse
distance.

### What the library adapter costs

These arms wrap [scalalang2/golang-fifo](https://github.com/scalalang2/golang-fifo)
rather than implementing the algorithms here, and the wrapper is not free. The
adapter has to supply `Keys`, `Values`, `Resize` and `Cap`, none of which exist
upstream, which means a second copy of the key set maintained through the
library's eviction callback and a full rebuild on every resize. The per-operation
columns in the table above are what that costs: 371 to 770 ns/op for S3-FIFO
against 80 to 130 for a bare LRU on the same traces.

The trade bought is not owning an eviction algorithm, and that is worth
something. An earlier from-scratch S3-FIFO in this repository shipped with a
real bug in its ghost queue -- the paper's virtual-timestamp approximation
leaves a dead slot behind whenever an entry is removed early, so the queue
steadily held fewer keys than its capacity claimed and dropped them just before
they came back. Every property test passed; only a differential run against a
second implementation found it. That implementation is gone, so its numbers are
not quoted here: nothing in `make evidence` reproduces them and no test guards
them.

## Does sampling distort the comparison?

Sampled shadows are only sound if a miniature ranks policies the way full-size
shadows would. Measured directly across four sample rates, against full-size
shadows as ground truth:

```text
zipf   full-size  ARC=81.6% 2Q=81.3% SIEVE=81.2% LFU=81.2% W-TinyLFU=81.2% S3-FIFO=81.1% TTL=79.2% LRU=79.2% Random=76.6%
       rate 0.05  ARC=63.5% W-TinyLFU=63.3% 2Q=63.2% SIEVE=63.0% LFU=63.0% S3-FIFO=62.4% TTL=59.2% LRU=59.2% Random=54.5%
       rate 0.10  ARC=67.5% 2Q=67.0% W-TinyLFU=67.0% S3-FIFO=66.9% SIEVE=66.8% LFU=66.8% LRU=63.8% TTL=63.3% Random=58.9%
       rate 0.30  ARC=79.0% W-TinyLFU=78.8% 2Q=78.6% LFU=78.4% SIEVE=78.4% S3-FIFO=78.4% LRU=76.2% TTL=76.2% Random=73.2%
       rate 0.50  ARC=84.7% 2Q=84.4% LFU=84.3% SIEVE=84.3% W-TinyLFU=84.3% S3-FIFO=84.3% LRU=82.7% TTL=82.7% Random=80.5%

scan   full-size  2Q=28.3% ARC=28.3% S3-FIFO=28.3% LFU=28.3% SIEVE=28.3% W-TinyLFU=28.1% LRU=21.4% TTL=21.4% Random=18.9%
       rate 0.05  LFU=28.4% 2Q=28.4% ARC=28.4% S3-FIFO=28.4% SIEVE=28.4% W-TinyLFU=28.3% TTL=21.5% LRU=21.5% Random=19.0%
       rate 0.10  S3-FIFO=26.0% LFU=26.0% ARC=26.0% SIEVE=26.0% 2Q=26.0% W-TinyLFU=25.3% LRU=19.6% TTL=19.6% Random=17.7%
       rate 0.30  S3-FIFO=28.5% 2Q=28.5% SIEVE=28.5% LFU=28.5% ARC=28.5% W-TinyLFU=28.3% TTL=21.6% LRU=21.6% Random=19.0%
       rate 0.50  LFU=28.6% SIEVE=28.6% 2Q=28.6% ARC=28.6% S3-FIFO=28.6% W-TinyLFU=28.4% LRU=21.6% TTL=21.6% Random=19.0%
```

**Sampling costs zero regret at every rate**, on both workloads, including at
the aggressive 5%. Note that "picks the same arm" is the wrong way to say this:
on `scan` five arms tie to the hundredth of a point, so which one is nominally
best is decided by map iteration order and moves run to run. What is stable is
that the arm sampling picks is never actually worse — the regret column is 0.00
throughout.

The ordering check runs on `loop` and `scan`: 0 inversions out of 8 clearly
separated pairs on each, at all four rates. `zipf` is deliberately not in that
check any more. It used to supply separated pairs, but only because the shadow
defect described above held Random at 32.8% there when it truly serves 76.6% —
a 44-point artifact. With shadows measuring honestly, the nine arms on `zipf`
land within 5.0 points of each other, so the workload separates nothing and can
prove nothing about ordering. Both FIFO policies hold their rank under
sampling like the rest, which was not a foregone conclusion: S3-FIFO's ghost
queue is sized in absolute terms, so a miniature shrinks the window it can see
reuse through, and their shared adapter rebuilds the cache on every resize.

What sampling does *not* give you is an estimate of the absolute hit rate. Read
the zipf rows down the rate column: ARC measures 63% at rate 0.05 and 85% at
rate 0.50, against 82% full-size. The estimate depends on which slice of the
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
