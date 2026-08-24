# Design

How as-cache works, and what it deliberately does not do.

## Problem

Choosing the right cache replacement algorithm for a workload is a separate
research task. This library sidesteps that decision by running candidate
policies in parallel (shadow caching), measuring hit/miss rates per epoch, and
using a multi-armed bandit to pick the winner dynamically.

## When it fits

Use it when:

- You do not know which policy suits your traffic, and cannot easily find out.
- Your traffic changes shape and you would rather not re-tune.
- You want the measurement more than the switching. `ObserveOnly` gives you
  that at no risk to the cache's behaviour — see [advisor mode](advisor-mode.md).

Do not use it when:

- You have already measured your traffic and know which policy wins. Use that
  policy directly; this library's best case is roughly to match it, and it
  [lands within 1.4 points of it on four of six real traces and beats it on
  the other two](evidence.md#real-traces).
- The hot path is latency-critical at single-digit nanoseconds. Even sampled,
  the adaptive layer costs several times a bare LRU per operation — the
  [figures](evidence.md#memory-and-per-operation-cost) are measured.
- You need a hard memory ceiling. The multiplier is well under the number of
  arms, but it is real.
- You cannot give it enough traffic per epoch to measure anything. Arms within
  noise of each other reorder run to run, so a cache seeing a handful of
  requests per epoch picks essentially at random. `Advice()` reports `Epochs`
  so you can tell whether it has seen enough. If the cause is that your traffic
  is spread across replicas rather than genuinely thin, see [running a
  fleet](fleet.md).
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
   were allowed to learn. Shadows hold keys and eviction bookkeeping, never
   data, which is why N policies do not cost N times the memory — and why no
   caller can ever be handed a shadow's zero.

Then once per epoch:

1. Every arm reports its hits and misses — the active one included, measured
   over the same sampled substream, so no arm is judged on more evidence than
   another. Counters reset; the epoch is the unit of evidence.

   Which goroutine runs this depends on how the epoch ends. `EpochDuration`
   ticks on a background goroutine; `EpochRequests` runs it on whichever
   caller made the Nth `Get`, so that one call pays for the switch and any
   migration it triggers.
2. The [bandit](#implementing-the-bandit-interface) receives that evidence and
   names the arm for the next epoch. Beta posteriors updated with each arm's
   hits and misses, drawn from by Thompson sampling, is the usual choice —
   `bandit.NewThompson` is one — but the interface is yours to implement, and
   `bandit.NewDistributed` pools the evidence across a fleet.
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
  |-- epoch driver   (runEpoch -> selectPolicyLocked -> switchLocked -> migrateData,
                      on a background goroutine for EpochDuration, or on the
                      calling goroutine for EpochRequests)
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

Both methods are called under the cache's write lock, so **an implementation
must not block**. Go's `RWMutex` queues new readers behind a waiting writer, so
a slow bandit stalls every `Get` in the process for its duration.

A full Thompson Sampling adapter using `stitchfix/mab` is provided in
[examples/basic/main.go](../examples/basic/main.go). Ready-made bandits live in
the `bandit` module: `bandit.NewThompson` for a single process,
[`bandit.NewDistributed`](fleet.md) for a fleet.

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
  `EpochRequests` takes the clock out of a replay — see
  [benchmarking](benchmarking.md) — but not out of production use.
- **No adaptive sizing.** The cache's capacity is whatever you set. Only the
  choice of policy adapts.
- **Nothing here has run in production** that I know of.
