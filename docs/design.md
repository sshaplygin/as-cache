# Design

How this experimental library measures and studies adaptive policy selection.

## Problem

Choosing the right cache replacement algorithm for a workload is a separate
research task. This library sidesteps that decision by running candidate
policies in parallel (shadow caching), measuring hit/miss rates per epoch, and
using a multi-armed bandit to pick the winner dynamically.

## When it fits

Use it to investigate policy selection, switching and sampling on a workload.
For a general-purpose production cache, start with otter or theine and read the
[comparison](evidence.md#how-does-it-compare-with-other-go-cache-libraries).
`ObserveOnly` keeps the configured policy active while gathering advice;
it still adds measurement overhead — see [advisor mode](advisor-mode.md).

Do not use it when:

- You have already measured your traffic and know which policy wins. Use that
  policy directly. The measured adaptive medians trail the best fixed choice
  on eleven of twelve traces at every tested epoch length; see
  [the full matrix and its limits](evidence.md#real-traces).
- The hot path is latency-critical at single-digit nanoseconds. Even sampled,
  the adaptive layer adds work to a bare LRU operation — the
  [figures](evidence.md#memory-and-per-operation-cost) are measured.
- You need a hard memory ceiling. The multiplier is well under the number of
  arms, but it is real.
- You cannot give it enough traffic per epoch to measure anything. Arms within
  noise of each other reorder run to run, so a cache seeing a handful of
  requests per epoch picks essentially at random. `Advice()` reports `Epochs`
  so you can tell whether it has seen enough. Traffic spread thin across many
  replicas is the same problem seen from further away, and this library does
  not solve it: each replica measures only what it serves.
- Your keyspace fits in the cache. Every policy scores the same when nothing is
  ever evicted, and you are paying for shadows that can never tell you anything.

## Idea

One policy is **active** and serves every request. The others run as
**shadows**: they see each key, never its value, and answer the question
"would I have had this?" — a hit rate measured on your traffic rather than
guessed from a paper.

On each request:

1. The active policy serves the read or write. A read counts as its hit or
   miss; a write counts as neither, since nobody asked the cache a question.
2. The sampler decides whether the shadows see the key at all. When
   `ShadowSampleRate` is below 1 they track a deterministic fraction of the
   keyspace and shrink to match, so per-operation cost stops scaling with the
   number of policies.
3. Each shadow answers the same lookup, and a shadow that **misses fills
   itself** with `Add(key, zeroValue)` — exactly as the caller would fill a
   read-through cache that missed. That fill is what makes the measurement mean
   anything: a read-through caller only calls `Add` when the *active* policy
   missed, so without it a shadow could never acquire a key the incumbent was
   already serving, and the better the incumbent performed the less its rivals
   were allowed to learn. The drift that causes is not a small bias: measured
   on a cyclic workload behind a 94%-hit incumbent, arms that truly serve 0.00%
   reported over 90%, because a starved shadow's contents go static and a
   static cache covering most of a small keyspace looks excellent. Its sign
   depends on which arm is incumbent, so it does not cancel — `Advice()`
   recommended switching from the best arm to the worst. Shadows hold keys and
   eviction bookkeeping, never data, which is why N policies do not cost N
   times the memory — and why no caller can ever be handed a shadow's zero.

Then once per epoch:

1. Every arm reports its hits and misses — the active one included, measured
   over the same sampled substream, so no arm is judged on more evidence than
   another. Counters reset; the epoch is the unit of evidence. The counts are
   what was measured and are never scaled back up by `1/rate`: scaling would
   restore the magnitude while inventing confidence, handing a Beta posterior
   many times the evidence actually collected. `Stats()` still reports every
   request the cache served; only the bandit sees the sample.

   Which goroutine runs this depends on how the epoch ends. `EpochDuration`
   ticks on a background goroutine; `EpochRequests` runs it on whichever
   caller made the Nth `Get`, so that one call pays for the switch and any
   migration it triggers.
2. The [bandit](#implementing-the-bandit-interface) receives that evidence and
   names the arm for the next epoch. Beta posteriors updated with each arm's
   hits and misses, drawn from by Thompson sampling, is the usual choice —
   `bandit.NewThompson` is one — but the interface is yours to implement.
3. If the named arm is not the active one, [stability
   gates](configuration.md#keeping-switches-stable) decide whether the
   improvement is worth a switch. On a switch, data moves according to the
   [migration strategy](configuration.md#migration-strategies), and the outgoing
   policy rewrites its entries to zero values, keeping the keys its eviction
   bookkeeping needs. It is a shadow now, and shrinks to the miniature capacity
   shadows run at if sampling is on.

The measurement is the durable part, and you can have it without the
switching: [`ObserveOnly`](advisor-mode.md) runs every arm and reports which
would have served you best, while the cache behaves exactly like the policy you
built it with.

## Architecture

```text
AdaptiveCache
  |-- active policy  (CacheWrapper -> real Cacher impl)
  |-- shadow policy  (CacheWrapper -> real Cacher impl, zero-value adds only,
  |                   optionally a sampled miniature -- see ShadowSampleRate)
  |-- Bandit         (an interface; ready-made ones in the bandit module)
  |-- epoch driver   (runEpoch: collectEpoch -> consultBandit -> applySelection
                      -> switchLocked -> migrateData, on a background goroutine
                      for EpochDuration, or on the calling goroutine for
                      EpochRequests)
```

### Two rules the implementation keeps

**Every mutation of a policy happens while that policy is not the active one.**
A switch resizes and migrates into the incoming policy before making it active,
and drops the outgoing policy's values only after it has stopped being active.
Reverse either half and a caller can read a policy mid-rewrite -- most
damagingly, taking a dropped value's zero for real data. Lock-free reads, listed
under [what is not done](#what-is-not-done), would rest on this rule: a reader
could only ever hold a policy nobody is mutating.

**During a gradual migration window a policy can hold one of three roles, not
two.** Besides the active policy and the shadows there is the source the window
promotes out of. It is not active, and it is not a shadow either: it holds the
only copy of every value not yet promoted, so it keeps its full capacity, is not
filled with zero values when a read misses, and is demoted only when the window
closes. Code that ranges over the policies skipping only the active one has to
decide what it does to the source. Treating it as a shadow once filled it with
zeros that the window then promoted and served to callers as hits.

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

Both methods are called with **no cache lock held**. A bandit that takes its
time delays the switch it is deciding, and the epoch after it, but not the
cache's own operations — concurrent `Get` and `Add` are unaffected. The epoch
snapshots every arm's counters under the write lock, releases it, consults the
bandit, and takes the lock again to apply the result; a selection superseded by
a later epoch in the meantime is dropped rather than applied late.

Two consequences worth knowing. Under `EpochRequests` the `Get` that completes
an epoch runs it, so that one caller does wait for the bandit — under
`EpochDuration` nobody does. And calls are serialised: no implementation is
entered from two goroutines at once.

A selection naming `Undefined`, or any policy the cache does not hold, means no
change. That is the natural answer from a bandit that has not formed an opinion
yet, and the cache treats it as one rather than looking the policy up.

Ready-made bandits live in the `bandit` module: `bandit.NewThompson`, and
`bandit.NewGreedy` as a control. Both examples use the first.

### Plugging in a third-party bandit

The interface is two methods, so wrapping an outside implementation is an
adapter of about this size. Illustration only -- it names no real library and
is not compiled:

```go
type adapter struct {
    arms []ascache.PolicyType
    ext  *externalBandit // your library's type
}

func (a *adapter) RecordStats(s ascache.ShadowStats) {
    // Deliver one arm's epoch result. Called once per arm per epoch.
    a.ext.Observe(s.Policy, s.Hits, s.Misses)
}

func (a *adapter) SelectPolicy() ascache.PolicyType {
    // No cache lock is held here; taking time delays the switch, not Get.
    // Returning Undefined -- or any policy the cache does not hold --
    // means "no change", which is the right answer before the first epoch.
    return a.ext.Choose(a.arms)
}
```

Two things catch people out. Ranging a map while drawing random numbers makes
the result depend on map iteration order, so a seeded run stops being
reproducible -- keep arms in a slice. And an arm that saw no requests in an
epoch is not an arm that scored zero; decide deliberately which one your
implementation reports.

## What is not done

- **Reads take a lock.** Every read delegates to the active policy under the
  cache's `RWMutex`. Serving them from an `atomic.Pointer` instead would remove
  the cache's own share of the per-operation cost, but a retry protocol has to
  wrap all six read delegations, retries are not free of side effects (a
  retried read double-counts its own hit and double-bumps recency), and
  `MigrationGradual` cannot go lock-free at all, because promotion mutates from
  inside `Get`. Deferred as its own change rather than smuggled into another.
- **Not every replay is deterministic.** `EpochRequests` fixes the request
  boundaries, but sampled key selection, Random and asynchronous W-TinyLFU
  still vary. Wall-clock epochs and TTL add timing dependencies. See
  [benchmarking](benchmarking.md) for the repeatability requirements.
- **No adaptive sizing.** The cache's capacity is whatever you set. Only the
  choice of policy adapts.
- **Nothing here has run in production** that I know of.
