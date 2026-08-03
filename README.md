# as-cache — Adaptive Selection Cache

[![CI](https://github.com/sshaplygin/as-cache/actions/workflows/ci.yml/badge.svg)](https://github.com/sshaplygin/as-cache/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/sshaplygin/as-cache.svg)](https://pkg.go.dev/github.com/sshaplygin/as-cache)
[![Go Report Card](https://goreportcard.com/badge/github.com/sshaplygin/as-cache)](https://goreportcard.com/report/github.com/sshaplygin/as-cache)
[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](LICENSE)

A Go library that uses a **Multi-Armed Bandit (MAB)** algorithm to select the
cache replacement policy at runtime, measuring candidate policies against your
real traffic instead of asking you to guess.

## Status

Pre-1.0: the API may still change, and nothing here has been run in production
that I know of. What has been done is measurement -- every claim below comes
from a reproducible run against published traces, not from intuition, and the
concurrency has been exercised under the race detector and adversarially
reviewed. Read [Evidence](#evidence) and decide for yourself; the numbers are
there so you do not have to take "experimental" or "production-ready" on
trust.

Three things are worth knowing before adopting it.

**No single policy wins everywhere, and that is the point.** On real traces the
best fixed policy changes: 2Q wins on the Twitter and OLTP traces, W-TinyLFU on
the ARC P3 and LIRS traces -- and on OLTP, W-TinyLFU is second-*worst*. Tuned
sensibly, adaptive selection lands within about a point of the best fixed
policy on most traces and beats it on one, without being told which to pick.

**It is sensitive to configuration.** The same traces with a too-short epoch
lose up to 7 points and cost 30x the per-operation time, because the cache
spends its life migrating rather than serving. See
[Configuring it](#configuring-it) before drawing conclusions from your own
numbers.

**Memory costs less than the obvious guess.** Running N policies in parallel
does not multiply memory by N, because shadow policies hold keys and eviction
bookkeeping but never real values. Measured with six policies over 50k entries
of 256-byte values:

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

### When to use it

- You do not know which policy suits your traffic, and cannot easily find out.
- Your traffic changes shape and you would rather not re-tune.
- You want the measurement more than the switching. `ObserveOnly` mode gives
  you that at zero risk -- see [Advisor mode](#advisor-mode).

### When not to use it

- You have already measured your traffic and know which policy wins. Use that
  policy directly; this library's best case is roughly to match it.
- The hot path is latency-critical at single-digit nanoseconds. Even sampled,
  the adaptive layer costs several times a bare LRU per operation.
- You need a hard memory ceiling. The multiplier is modest but real.
- You cannot give it enough traffic per epoch to measure anything. Arms that
  are within noise of each other reorder run to run, so a cache seeing a
  handful of requests per epoch will pick essentially at random. `Advice()`
  reports `Epochs` so you can tell whether it has seen enough. If the reason
  is that your traffic is spread across many replicas rather than genuinely
  thin, see [Running a fleet](#running-a-fleet).
- Your keyspace is small enough to fit in the cache. Every policy scores the
  same when nothing is ever evicted, and you are paying for shadows that can
  never tell you anything.

## Problem

Choosing the right cache replacement algorithm for a workload is a separate research task. This library sidesteps that decision by running candidate policies in parallel (shadow caching), measuring hit/miss rates per epoch, and using Thompson Sampling to pick the winner dynamically.

## Idea

One policy is **active** and serves every request. The others run as
**shadows**: they see each key, never its value, and answer the question
"would I have had this?" — a hit rate measured on your traffic rather than
guessed from a paper.

On each request:

1. The active policy serves the read or write and counts its own hit or miss.
2. The sampler decides whether the shadows see the key at all. When
   `ShadowSampleRate` is below 1 they track a deterministic fraction of the
   keyspace and shrink to match, so per-operation cost stops scaling with the
   number of policies.
3. Selected keys go to every shadow as `Add(key, zeroValue)`. Shadows hold keys
   and eviction bookkeeping, never data, which is why N policies do not cost N
   times the memory — and why no caller can ever be handed a shadow's zero.

Then once per epoch, on a background goroutine:

1. Every arm reports its hits and misses — the active one included, measured
   over the same sampled substream, so no arm is judged on more evidence than
   another. Counters reset; the epoch is the unit of evidence.
2. The [bandit](#implementing-the-bandit-interface) receives that evidence and
   names the arm for the next epoch. Beta posteriors updated with each arm's
   hits and misses, drawn from by Thompson sampling, is the usual choice —
   `bandit.NewThompson` is one — but the interface is yours to implement, and
   `bandit.NewDistributed` pools the evidence across a fleet.
3. If the named arm is not the active one, [stability
   gates](#keeping-switches-stable) decide whether the improvement is worth a
   switch. On a switch, data moves according to the [migration
   strategy](#migration-strategies), and the outgoing policy rewrites its
   entries to zero values, keeping the keys its eviction bookkeeping needs. It
   is a shadow now, and shrinks to the miniature capacity shadows run at if
   sampling is on.

The measurement is the durable part, and you can have it without the
switching: [`ObserveOnly`](#advisor-mode) runs every arm and reports which
would have served you best, while the cache behaves exactly like the policy you
built it with.

## Usage

See [examples/basic/main.go](examples/basic/main.go) for a complete runnable example with an HTTP server and a Thompson Sampling adapter (via `stitchfix/mab`).

## Supported Cache Methods

| Policy | Status | Notes |
| --- | --- | --- |
| LRU | implemented | via `hashicorp/golang-lru/v2` |
| LFU | implemented | native O(1) implementation in `lfu/`; `policies.NewLFU` |
| 2Q | implemented | `policies.NewTwoQueue` |
| Random | implemented | `policies.NewRandomPolicy` |
| TTL | implemented | `policies.NewTTL` |
| ARC | implemented | `policies/arc` — separate module, patented |
| W-TinyLFU | implemented | `policies/tinylfu` — separate module |

## AdaptiveCache API

All methods are safe for concurrent use.

| Method | Description |
| --- | --- |
| `Add(key, value) bool` | Add or update a key; returns true if an eviction occurred |
| `Get(key) (V, bool)` | Retrieve a value; records a hit or miss |
| `Contains(key) bool` | Check presence without recording a hit |
| `Peek(key) (V, bool)` | Read value without recording a hit |
| `Remove(key) bool` | Delete a key from all policies |
| `Purge()` | Clear all policies and reset migration state |
| `Keys() []K` | Keys in the active policy |
| `Values() []V` | Values in the active policy |
| `Len() int` | Number of entries in the active policy |
| `Resize(size) int` | Resize all policies; returns total eviction count |
| `Stats() GlobalStats` | Cumulative hit/miss counts for the active policy |
| `ActivePolicy() PolicyType` | Which policy is currently serving requests |
| `Close() error` | Stop the background epoch goroutine |

## Settings

```go
type Settings struct {
    // EpochDuration controls how often the bandit re-evaluates policies.
    EpochDuration time.Duration

    // EpochRequests ends an epoch every N Get calls instead of on a clock.
    // Set one of these two, or both. See "Reproducible replays".
    EpochRequests int64

    // EvictPartialCapacityFilling allows switching before the cache is full.
    // When false, the bandit only runs once the active policy reaches capacity.
    EvictPartialCapacityFilling bool

    // MigrationStrategy controls data transfer on policy switch.
    // Default: MigrationCold.
    MigrationStrategy MigrationStrategy

    // ShadowSampleRate has shadows track a fraction of the keyspace.
    // Zero means 1 (no sampling). See "Reducing shadow overhead".
    ShadowSampleRate  float64
    MinShadowCapacity int

    // Switch stability gates; all inactive at zero.
    // See "Keeping switches stable".
    MinHitRateImprovement float64
    SwitchCooldownEpochs  int64
    MinEpochRequests      int64
}
```

## Reproducible replays

`EpochDuration` measures on a wall clock, which is right in production and
wrong for a benchmark: replaying one trace twice re-evaluates a different
number of times on a machine that happens to be busy, so the hit rate moves
between runs and cannot be compared with anything. `EpochRequests` ends an
epoch every N `Get` calls instead, which takes the clock out of the
measurement entirely.

```go
&ascache.Settings{EpochRequests: 10_000} // no EpochDuration: no wall clock at all
```

`Get` is the unit because `Get` is where hits and misses are recorded, so this
counts exactly the requests the bandit is shown. A write-only workload never
ends an epoch, which is correct — there is nothing to compare policies on. The
epoch runs on whichever goroutine makes the Nth `Get`, so that call pays for
the switch and any migration; prefer `EpochDuration` in production, where that
work belongs on the background goroutine. Setting both applies both.

Two things outside the epoch clock also have to hold still, and one of them is
not in your control:

- **Seed the bandit.** `bandit.NewThompson(discount, seed)` takes one.
- **Every arm must be deterministic.** LRU, LFU, 2Q and Random are.
  **W-TinyLFU is not**: otter evicts asynchronously and reports an approximate
  size, so replaying one trace three times against it directly gave three
  different hit counts and left 527, 504 and 545 entries in a cache with a
  capacity of 500. One unstable arm moves which policy the bandit picks, and
  with it the whole replay.

The `benchclient` module packages all of this — see [Benchmark
harnesses](#benchmark-harnesses).

## Migration Strategies

| Strategy | Behaviour | Trade-off |
| --- | --- | --- |
| `MigrationCold` (default) | New active policy starts empty | Simple; causes a temporary miss spike |
| `MigrationWarm` | All key/value pairs copied at switch time | No miss spike; O(n) work at switch |
| `MigrationGradual` | Keys promoted on Get; one key drained per Add | Spreads migration cost; window closes at the next epoch at the latest |

## Architecture

```text
AdaptiveCache
  |-- active policy  (CacheWrapper -> real Cacher impl)
  |-- shadow policy  (CacheWrapper -> real Cacher impl, zero-value adds only,
  |                   optionally a sampled miniature -- see ShadowSampleRate)
  |-- Bandit         (Thompson Sampling via stitchfix/mab)
  |-- background goroutine (epoch ticker -> tryChangePolicy -> migrateData)
```

## Implementing the Bandit Interface

```go
type Bandit interface {
    // RecordStats delivers one policy's hit/miss stats since its last
    // report; every policy reports, the active one included.
    RecordStats(stats ShadowStats)

    // SelectPolicy returns the policy that should become active next epoch.
    SelectPolicy() PolicyType
}
```

A full Thompson Sampling adapter using `stitchfix/mab` is provided in [examples/basic/main.go](examples/basic/main.go).

## Reducing shadow overhead

Running policies in parallel costs something on every operation: each shadow is
another lookup and another lock. Since a shadow exists only to estimate a hit
rate, and a hit rate can be estimated from a sample, `ShadowSampleRate` lets
shadows track a deterministic fraction of the keyspace instead of mirroring
everything.

```go
&ascache.Settings{
    EpochDuration:    time.Minute,
    ShadowSampleRate: 0.05, // shadows track 5% of keys
}
```

Shadows shrink along with the rate, so each remains a faithful miniature of a
full-size cache rather than an undersized one, and every shadow samples the same
keys so their hit rates stay comparable. The active policy still serves every
key -- only the measurement is sampled, and it is sampled for the active policy
too, so no arm is judged on more evidence than another. `Stats()` continues to
report real, unsampled traffic.

The effect is that per-operation cost stops scaling with the number of policies.
Measured with mutex-backed stub policies on an Apple M1 Max at
`-benchtime=300ms`, so the numbers isolate what the adaptive layer adds rather
than what any particular policy costs:

| Benchmark | shadows | sampling off | rate 0.05 |
| --- | --- | --- | --- |
| `Get` | 1 | 97 ns/op | 35 ns/op |
| `Get` | 3 | 148 ns/op | 38 ns/op |
| `GetParallel` | 1 | 271 ns/op | 181 ns/op |
| `GetParallel` | 3 | 405 ns/op | 185 ns/op |
| `Add` | 1 | 110 ns/op | 52 ns/op |
| `Add` | 3 | 193 ns/op | 59 ns/op |
| `MixedParallel` | 1 | 183 ns/op | 87 ns/op |

Read the `Get` rows down the shadow count. Unsampled, a third shadow costs
another 50ns, because every operation visits every policy. Sampled, going from
one shadow to three costs 3ns -- the fan-out happens on 5% of operations, so
adding a policy is close to free. That is what makes carrying seven arms
practical.

Reproduce with `go test -run '^$' -bench . -benchtime=300ms .`

Sampling is off by default. Very small caches disable it automatically, since a
miniature of a handful of entries measures noise rather than a policy.

## Keeping switches stable

By default every bandit selection is applied. On noisy traffic two policies that
perform almost identically can trade places every epoch, and each switch costs a
migration. Three settings damp that, all inactive at their zero value:

```go
&ascache.Settings{
    MinHitRateImprovement: 0.02, // require a 2-point hit-rate win to switch
    SwitchCooldownEpochs:  3,    // and at most one switch every 3 epochs
    MinEpochRequests:      500,  // and ignore epochs with thin evidence
}
```

## Ready-made policies

The core module has no dependencies. Ready-made arms live in a companion
module, so you pull in a cache library only if you use one:

```bash
go get github.com/sshaplygin/as-cache/policies
```

```go
lru, _ := policies.NewLRU[string, int](10000)
twoQ, _ := policies.NewTwoQueue[string, int](10000)

cache, err := ascache.NewAdaptiveCache(
    []ascache.Policy[string, int]{
        lru,
        twoQ,
        policies.NewRandomPolicy[string, int](10000),
        policies.NewTTL[string, int](10000, 5*time.Minute),
    },
    myBandit,
    &ascache.Settings{EpochDuration: time.Minute, ShadowSampleRate: 0.05},
)
```

| Policy | Constructor | Notes |
| --- | --- | --- |
| LRU | `policies.NewLRU` | `hashicorp/golang-lru/v2` |
| LFU | `policies.NewLFU` | this repository's O(1) LFU; strong on stationary popularity, weak when it shifts |
| 2Q | `policies.NewTwoQueue` | scan-resistant; a scan cannot flush the working set |
| Random | `policies.NewRandomPolicy` | no bookkeeping; the control arm worth beating |
| TTL | `policies.NewTTL` | expiry as well as recency |
| ARC | `policies/arc.NewPolicy` | separate module — see below |
| W-TinyLFU | `policies/tinylfu.NewPolicy` | separate module; the strongest baseline |

`Random` is worth keeping in the mix precisely because it assumes nothing: a
policy that cannot beat random on your traffic is not earning its bookkeeping.

### ARC is a separate module

```bash
go get github.com/sshaplygin/as-cache/policies/arc
```

ARC is patented by IBM (US 6,996,676), which is why upstream `hashicorp/golang-lru`
moved it to its own module in v2. This repository keeps that split, so importing
`policies` never pulls a patented implementation into your build and the choice
to use ARC is always explicit. Whether the patent still restricts anything is a
question for you and your counsel.

### Adapting your own cache

Any type satisfying `Cacher[K, V]` can be an arm. If your cache does not report
evictions or cannot be resized — as `2Q` and `ARC` do not — wrap it:

```go
cache, err := policies.Adapt[string, int](size, func(size int) (policies.PartialCacher[string, int], error) {
    return mylib.New[string, int](size)
})
```

Note that `Resize` on an adapted cache rebuilds it, discarding whatever
adaptation the algorithm had learned. `AdaptiveCache` resizes shadow policies
when its own capacity changes, so adapted policies are heavier arms to carry
than natively resizable ones.

### W-TinyLFU

```bash
go get github.com/sshaplygin/as-cache/policies/tinylfu
```

Carried in its own module so otter and its dependencies stay out of builds that
do not use it. This is the arm worth including if the question is whether an
adaptive cache beats the state of the art rather than whether it beats LRU.

Note that otter reports an approximate size, so this policy's `Len()` is
approximate. Set `EvictPartialCapacityFilling: true` when using it, since the
capacity gate compares `Len()` against `Cap()` for exact equality.

**It also runs slightly over the capacity it was given, and that flatters it.**
otter admits on the calling goroutine and evicts on a maintenance pass, so the
cache sits above its limit whenever writes arrive faster than maintenance
drains them. Measured at a nominal 500 entries under read-through replay: 514
on `zipf` (1.03x), 533 on `loop` (1.06x), 611 on `uniform` (1.22x) — the
overshoot tracks the write rate, and `uniform` misses on almost every request.
On that workload it is the whole story: the arm held 611 of a 5000-key
keyspace and served 12.18%, and 611/5000 is 12.2%, so its edge over the other
policies there is capacity rather than eviction. Under a pure write flood the
gap is far wider — 1916 entries retained against a limit of 500 — which is why
the competitor harness calls `CleanUp`. Read its wins on write-heavy workloads
with that in mind; on read-heavy ones the overshoot is a few percent and the
comparison is sound.

## Advisor mode

The safest way to adopt this library is not to let it switch anything. In
`ObserveOnly` mode the cache behaves exactly like the first policy you give it
-- nothing ever migrates, nothing ever switches -- while every other policy is
measured in the background against your real traffic.

```go
cache, err := ascache.NewAdaptiveCache(
    []ascache.Policy[string, int]{lru, twoQ, tinyLFU},
    nil, // observing needs no bandit
    &ascache.Settings{
        EpochDuration:    time.Minute,
        ObserveOnly:      true,
        ShadowSampleRate: 0.05,
    },
)

// ... later, after real traffic ...
fmt.Println(cache.Advice())
```

```text
On this traffic TwoQueue beats LRU by 3.28 points of hit rate, over 240 epochs.
Rates are estimated from 5.0% of the keyspace.

policy      hit rate         hits       misses
 TwoQueue      59.62%       596200       403800
*LRU           56.34%       563400       436600
 Random        54.80%       548000       452000

* currently active
```

That answers a question that is otherwise expensive to ask, at no risk: you
learn whether a different eviction policy would serve your traffic better, and
by how much, without changing what your cache does. Acting on the answer is
then your choice -- switch to that policy directly, or turn `ObserveOnly` off
and let the bandit do it.

`Advice()` is safe to call at any time. Check `Epochs` before believing it: a
handful of epochs is not evidence.

## Observability

A cache that changes its own eviction policy needs to be visible in staging.
The `metrics` module turns the cache's accounting into a scrapeable snapshot
and publishes it via `expvar` (standard library only):

```bash
go get github.com/sshaplygin/as-cache/metrics
```

```go
if err := metrics.Publish("cache", myCache); err != nil {
    log.Panic(err)
}
// snapshot now appears in /debug/vars under "cache"
```

`metrics.Take(cache)` returns the same data as a struct if you would rather
feed it somewhere else. The series worth graphing is `active_policy` over
time; the one worth alerting on is `improvement`, which measures how much hit
rate the cache is currently leaving on the table.

For Prometheus, wrap `metrics.Take` in a collector -- how metrics are named and
labelled belongs to your application, not to a cache library, so this package
does not impose a dependency on it.

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
LFU, 2Q and Random, all of which are deterministic. `ArmsWithWindowTinyLFU`
adds the strongest arm and gives up repeatability to do it — that trade is
yours to make explicitly, which is why it is a second function rather than an
option. ARC is absent for the patent reason described above; a harness that
wants it can supply its own `Arms`.

## Running a fleet

One replica of a service sees one replica's traffic. If you run fifty of them
behind a load balancer, each cache sees a fiftieth of the requests, and the
"you cannot give it enough traffic per epoch to measure anything" caveat above
stops being about your traffic and starts being about how it was divided.

The `bandit` module pools that evidence back together through Valkey or Redis.
Each replica publishes its per-epoch counts; one replica per coordination epoch
reads the fleet's aggregate, chooses, and publishes the choice for the others
to apply.

```go
client := goredis.NewClient(&goredis.Options{Addr: "valkey:6379"})
store, err := redisstore.New(redisstore.Options{Client: client})
if err != nil {
    return err
}
defer store.Close()

b, err := bandit.NewDistributed(bandit.Config{
    Store:             store,
    Namespace:         "sessions",
    CoordinationEpoch: time.Second,
})
if err != nil {
    return err
}
defer b.Close()

cache, err := ascache.NewAdaptiveCache(arms, b, &ascache.Settings{
    EpochDuration: 50 * time.Millisecond,
})
```

Read the fleet evidence below before reaching for it. The answer is yes in one
specific regime -- replicas individually too starved of traffic to rank their
own arms -- and no everywhere else, and which case you are in is measurable in
advance.

**Two clocks, not one.** `EpochDuration` is how often each cache measures;
`CoordinationEpoch` is how often the fleet decides. They are deliberately
different scales. Cache epochs are tuned in tens of milliseconds, which is
below both a round trip to the store and any clock agreement a fleet can be
assumed to have. Measure on the fast clock, coordinate on the slow one; a
second is a sensible starting point.

**No replica's clock is ever consulted.** Buckets are derived from the store's
clock inside a Lua script, so a fleet needs no clock synchronisation at all and
a machine with a skewed clock cannot write its counts into a window nobody
reads.

**Nothing touches the network on the cache's path.** The cache calls its bandit
while holding its write lock, so a round trip there would stall every `Get` in
the process — and Go's `RWMutex` queues readers behind a waiting writer, so a
store that hangs would hang the cache. `RecordEpoch` folds numbers into a
buffer and `SelectPolicy` is an atomic load; all I/O happens on the bandit's
own goroutine, once per coordination epoch.

**When the store is unreachable**, each replica falls back to a local Thompson
bandit fed by its own reports, which is exactly the behaviour of a cache that
was never distributed. Nothing fails and nothing blocks. Counts measured during
the outage are discarded rather than replayed on recovery — evidence that
arrives in the wrong window is worse than no evidence. `Snapshot().Fallback` is
the field to alert on: the cache looks entirely healthy either way.

**Only integers cross the wire.** Per-policy hit and miss counts, a node id and
a policy name. No cache keys and no cache values ever leave the process.
Everything written carries a TTL, so a fleet that stops running leaves nothing
behind.

Requires Redis 7.0 or Valkey 7.2 and above. `docker-compose.yml` brings up both
for local testing; `make redis-test` runs the store suite against each.

### Which replicas pool with which

Pooling is only meaningful between caches measuring the same thing. A hit rate
from a 1000-entry cache says nothing about a 100-entry one, and averaging them
describes neither — with nothing in the numbers to show it happened.

So `Namespace` is not the whole key. A fingerprint of each cache's measurement
regime — its arms, its capacity and its sample rate — is appended to it, and
replicas that share a name without sharing a regime pool separately rather than
pooling wrongly. `Snapshot().Namespace` and `Snapshot().Regime` are where to
look when a fleet has unexpectedly split in two. Epoch duration deliberately is
not part of it: a replica reporting twice as often contributes twice the
counts at the same rate, and rates are what the comparison is made on.

### The two modes

`ModeLeader` (the default) elects one replica per coordination epoch to decide
for everyone, so the fleet runs one policy at a time. `ModeSharedPosterior`
has every replica draw its own selection from the pooled evidence, so no
election happens and replicas may run different policies indefinitely.

Leader election is the default for a reason that is not obvious. The active
arm on a replica is measured at full capacity while every shadow runs on a
miniature, and shadows measure a point or two pessimistic. Under leader
election every replica has the *same* arm in the flattering role, so the bias
applies uniformly and largely cancels when the counts are summed. Under
shared-posterior selection it does not: an arm active on most of the fleet is
mostly measured in the flattering role, so it accumulates an advantage in
proportion to how widely it is already deployed. `EvidenceShadowOnly` removes
that feedback by discarding active-role counts, which is why it is available
under shared-posterior selection and rejected under leader election — where
the fleet-wide active policy is nobody's shadow and would have no evidence at
all.

### Pooling changes how much evidence a posterior sees

A Beta posterior narrows with the square root of what it has seen, and a fleet
supplies evidence in proportion to its size. A thousand replicas produce
posteriors sharp enough that every Thompson draw returns the same arm — the
bandit stops exploring precisely at the scale where missing a workload change
is most expensive. `MaxEvidence` caps the effective sample size, keeping the
measured rate and discarding the surplus certainty. The default puts an arm's
posterior standard deviation at about a sixth of a percentage point.

Decay works the same way for the same reason: a shared counter cannot be
decayed in place, because every replica applying the multiplication would
compound it once per replica and a fleet of fifty would forget fifty times
faster than a fleet of one. The store holds plain per-bucket sums and the
weighting happens on read, so the arithmetic is identical at any fleet size.

## Evidence

`make evidence` replays a suite of deterministic workloads against every policy
and against the adaptive cache. The numbers below are from an M1 Max, cache
capacity 500, 200k requests per workload. Reproduce with `make evidence`; the
generators are in [bench/workload.go](bench/workload.go).

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

### How does it compare with other Go cache libraries?

The tables elsewhere in this section compare this repository's policies with
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
`loop` by nearly 20. It is also 4 to 15 times slower per operation, which the
[cost tables](#status) already describe. If you are choosing a cache library
and have no particular reason to expect your traffic to change shape, otter or
theine is the better answer, and this README is the wrong place to pretend
otherwise.

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

### Does adaptive selection beat picking one policy?

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
  rather than the switching -- see [advisor mode](#advisor-mode).

For a workload that genuinely crosses over, the picture could differ. These are
synthetic, and the section below shows real traces overturning the conclusion.

### Real traces

`./scripts/fetch-traces.sh` downloads five published traces (nothing is
committed -- see [docs on traces](#real-traces)), then
`AS_CACHE_TRACES=... make evidence` replays them. Adaptive here runs a 50ms
epoch with warm migration and `ShadowSampleRate: 0.05`:

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

### Configuring it

The epoch duration is the setting that matters most, and the failure mode is
not subtle. Measured on the ARC P3 trace with a 20k-entry cache:

| Configuration | Hit rate | ns/op |
| --- | --- | --- |
| 50ms epoch, warm migration | 12.2% | 540 |
| 2ms epoch, warm migration | 4.8% | 13,476 |
| 2ms epoch, cold migration | 0.9% | 580 |

An epoch short enough to trigger frequent switches makes the cache copy its
entire contents on every switch, so it spends its time migrating rather than
serving. Cold migration is worse: it discards the cache at each switch, which
on the OLTP trace costs 28 points.

Rules of thumb:

- Make the epoch long enough that migrating the cache is a small fraction of
  the work done in it, and short enough that the workload sees many epochs.
- Prefer `MigrationWarm`. `MigrationCold` is only reasonable if switches are
  rare.
- The stability gates help on steady traffic and hurt on fast-changing traffic
  -- they cost 37 points on `loop`, which needs to re-adapt constantly.

### Does sampling distort the comparison?

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

### Does pooling across a fleet help?

**Only in the regime it was built for, and it is worth checking you are in that
regime before turning it on.** All figures are 8 replicas, cache capacity 300
to 500, `make evidence`.

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
also better. `Advice()` in observe-only mode will tell you which case you are
in before you deploy anything.

## What is not done

- **Reads take a lock.** Every read delegates to the active policy under the
  cache's `RWMutex`. Serving them from an `atomic.Pointer` instead would remove
  the cache's own share of the per-operation cost, but a retry protocol has to
  wrap all six read delegations, retries are not free of side effects (a
  retried read double-counts its own hit and double-bumps recency), and
  `MigrationGradual` cannot go lock-free at all, because promotion mutates from
  inside `Get`. Deferred as its own change rather than smuggled into another.
- **Epochs are wall-clock driven** and cannot be stepped, so every measurement
  of the bandit is timing-sensitive. This is why the evidence suite is excluded
  from `-race`, and it makes the bandit awkward to test deterministically.
- **No adaptive sizing.** The cache's capacity is whatever you set. Only the
  choice of policy adapts.
- **Nothing here has run in production** that I know of.

## References

- [Cache replacement policies — Wikipedia](https://en.wikipedia.org/wiki/Cache_replacement_policies)
- [Introducing Ristretto — hypermode.com](https://hypermode.com/blog/introducing-ristretto-high-perf-go-cache)
- [Ristretto (dgraph-io)](https://github.com/dgraph-io/ristretto) — inspiration for adaptive selection
- [hashicorp/golang-lru](https://github.com/hashicorp/golang-lru) — LRU/2Q/ARC implementations
- [stitchfix/mab](https://github.com/stitchfix/mab) — Multi-Armed Bandit (Thompson Sampling)
- [redis/go-redis](https://github.com/redis/go-redis) — the client behind the Valkey/Redis store
- [Valkey](https://valkey.io/) — the store the distributed bandit was built against

## License

[Mozilla Public License 2.0](LICENSE).

MPL 2.0 is file-level copyleft. You may use this library in a closed-source
application without opening your own code; if you modify one of *these* files
and distribute the result, that file's source must be made available under the
same licence. Each publishable module carries its own copy of the licence,
because a Go module zip contains only its own directory.
