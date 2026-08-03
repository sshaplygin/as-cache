# Architecture Codemap

**Last Updated:** 2026-07-26
**Root module:** `github.com/sshaplygin/as-cache`
**Go version:** 1.25.2
**Modules:** 9 (6 publishable, 3 internal)

## Overview

as-cache selects a cache eviction policy at runtime instead of asking the caller
to choose one. Candidate policies run side by side against the same key stream;
one serves real data while the rest measure comparative hit rates. A Multi-Armed
Bandit picks the active policy each epoch, or -- in `ObserveOnly` mode -- the
cache never switches and simply reports which policy would have served better.

The core module has no dependencies. Policies and integrations live in companion
modules so a consumer pulls in only what it uses.

## Module graph

```text
                    as-cache  (core, no deps)
                    /    |    \
                   /     |     \
          policies    metrics   lfu
         /   |    \                ^
        /    |     \               |
    arc  tinylfu   (policies also depends on lfu)
                        \
                         bench  (internal: depends on all of the above)
```

| Module | Role | External deps | LOC |
| --- | --- | --- | --- |
| `.` (root) | cache, epochs, sampling, advice | none | 1592 |
| `lfu` | O(1) LFU implementation | none | 682 |
| `policies` | LRU/LFU/2Q/Random/TTL adapters | `hashicorp/golang-lru/v2` | 1101 |
| `policies/arc` | ARC adapter (patent-isolated) | `hashicorp/golang-lru/arc/v2` | 50 |
| `policies/tinylfu` | W-TinyLFU adapter | `maypok86/otter/v2` | 207 |
| `metrics` | expvar export | none | 171 |
| `bench` | workloads, traces, evidence (internal) | all of the above | 808 |
| `examples/*` | runnable demos (internal) | -- | -- |

`policies/arc` is a separate module **solely** because ARC is patented by IBM
(US 6,996,676); importing `policies` must never pull a patented implementation
into a build. Upstream `hashicorp/golang-lru` made the same split.

## Request path

```text
Caller
  |
  v
AdaptiveCache.Get(key)
  |
  |-- sampler.sampled(key)?  ---no--> skip shadows entirely
  |         |yes
  |         v
  |    for each shadow: policy.Get(key)      (measurement only)
  |
  v
activePolicy.Get(key) ------------------> value to caller
  |
  +-- recordActiveSample(hit)  (counts only sampled keys, so every
                                arm is judged on the same substream)
```

`Add` mirrors this: shadows receive `Add(key, zeroValue)`, the active policy
receives the real value. **A shadow never holds a real value**, which is what
makes running seven policies cost 2.65x one rather than 7x.

## Epoch lifecycle

Driven by a background goroutine on `Settings.EpochDuration`.

```text
runEpoch()                              [holds the write lock throughout]
  |
  1. closeMigrationLocked()      end any gradual window; demote its source
  2. selectPolicyLocked()        report every arm to the bandit, reset
  |                              counters, accumulate tenureStats
  3. ObserveOnly? --yes--> stop here; the active policy never changes
  |
  4. allowSwitchLocked()         stability gates (improvement, cooldown,
  |                              minimum requests)
  5. switchLocked(from, to)      promote capacity -> migrate -> activate
  |                              -> demote the outgoing policy
  6. epochID++
```

### The ordering invariant

Documented on `switchLocked` and load-bearing:

> Every mutation of a policy must happen while that policy is not the active
> one.

The incoming policy is resized and migrated into *before* it becomes active; the
outgoing policy has its values dropped only *after* it stops being active.
Reversing either half would let a caller read a policy mid-rewrite and take a
dropped zero for real data. Any future move to lock-free reads rests on this.

## Key mechanisms

**Sampled shadows** (`sampling.go`). One `keySampler` per cache, shared by every
policy so all arms measure the same substream. Shadows shrink to
`ceil(rate * Cap)` so each stays a faithful miniature; `MinShadowCapacity` floors
that by raising the *effective rate*, not just the capacity.

**Migration** (`migration.go`). Cold discards, Warm copies at switch time,
Gradual promotes on read and drains one key per `Add`. A gradual window closes at
the next epoch at the latest.

**Advice** (`advice.go`). `tenureStats` accumulates per policy and is cleared
when a policy changes role, so a comparison never pools a policy's active tenure
(full capacity, all traffic) with its shadow tenure (miniature, sampled).

## Concurrency model

- One `sync.RWMutex` guards the cache. Reads take `RLock`; `Add`, `Remove`,
  `Purge`, `Resize` and the epoch take `Lock`.
- Wrapper hit/miss counters and the active-sample counters are `atomic.Int64`,
  because reads mutate them while holding only a read lock.
- Underlying policies carry their own locks; the cache lock orders policy
  *switching*, not individual policy operations.
- Reads are **not** lock-free. That work is deferred; see `CLAUDE.md`.

## Layering rules

- The root module must stay dependency-free. Anything needing a third-party
  cache goes in a companion module.
- New policies implement `Cacher[K, V]` and are wrapped by `CacheWrapper`; they
  need no change to the core.
- `PolicyType` is a closed enum in the root; adding a policy means extending it
  and regenerating the stringer.

## Related documents

- `codemaps/backend.md` -- file-by-file structure
- `codemaps/data.md` -- types, enums and their invariants
- `CLAUDE.md` -- development rules and the multi-module release procedure
- `README.md` -- landing page: what it is, when to use it, quick start, API
- `docs/` -- design, configuration, policies, advisor mode, benchmarking,
  fleets and the measured evidence behind every claim
