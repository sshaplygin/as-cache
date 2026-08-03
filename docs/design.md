# Design

How as-cache works, and what it deliberately does not do.

## Problem

Choosing the right cache replacement algorithm for a workload is a separate
research task. This library sidesteps that decision by running candidate
policies in parallel (shadow caching), measuring hit/miss rates per epoch, and
using Thompson Sampling to pick the winner dynamically.

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
