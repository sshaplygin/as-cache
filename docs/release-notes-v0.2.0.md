## as-cache v0.2.0

A cache that measures eviction policies against your real traffic instead of
asking you to guess which one to use.

This release is for the case where a single replica cannot measure anything.
Split your traffic across fifty replicas and each cache sees a fiftieth of the
evidence; arms within noise of each other reorder run to run, and the cache
picks close to at random. The fleet has the evidence between them, so replicas
can now pool their per-epoch counts through Valkey or Redis and decide
together. It pays off in that regime and **costs hit rate outside it** — the
numbers below say where the line falls.

### Install

```bash
go get github.com/sshaplygin/as-cache
go get github.com/sshaplygin/as-cache/bandit         # ready-made bandits
go get github.com/sshaplygin/as-cache/bandit/redis   # only for a fleet
```

Requires Go 1.25 or later.

**Every module is now published.** `policies`, `lfu`, `metrics`,
`policies/arc` and `policies/tinylfu` had never been tagged at any version, so
installing them failed with `unknown revision v0.0.0`. If you tried this
library before and could not get it, that was why.

### You no longer have to write a bandit

The `Bandit` interface was the fiddliest part of adopting this library and the
root module shipped no implementation. Two now live in `bandit`:

```go
b := bandit.NewThompson(0.9, seed) // discounted Beta posteriors
cache, err := ascache.NewAdaptiveCache(arms, b, settings)
```

`NewGreedy` is there as a control, for measuring what the sampling buys you.

### Pooling evidence across a fleet

Each replica publishes its per-epoch counts; one replica per coordination epoch
reads the fleet's aggregate, chooses, and publishes the choice for the rest.

```go
b, err := bandit.NewDistributed(bandit.Config{
    Store:             store, // redisstore.New(redisstore.Options{Client: client})
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

**Nothing touches the network on the cache's path.** The cache calls its bandit
while holding the write lock, and Go's `RWMutex` queues new readers behind a
waiting writer — so a store timeout there is not slow, it is an outage for
every `Get` in the process. `RecordEpoch` buffers, `SelectPolicy` is an atomic
load, and all I/O runs on the bandit's own goroutine.

**Two clocks, not one.** `EpochDuration` is how often a cache measures;
`CoordinationEpoch` is how often the fleet decides. Cache epochs are tuned in
tens of milliseconds, below both a round trip and any clock agreement a fleet
can be assumed to have. A second is a sensible start for the slow clock.

Time buckets come from the store's own clock inside a Lua script, so no
replica's clock is consulted and a skewed machine cannot poison a window. Only
integers cross the wire — per-policy counts, a node id, a policy name — never
keys or values. If the store is unreachable, each replica falls back to a local
Thompson bandit on its own evidence and nothing blocks; `Snapshot().Fallback`
is the field to alert on, because the cache looks healthy either way.

### What the measurements say

Eight replicas, capacity 300 to 500, reproducible with `make evidence`.

**Pooling wins when replicas are starved.** Paced to roughly 8 requests per
cache epoch per replica:

| Setup | Hit rate | Policies in use at the end |
| --- | --- | --- |
| best fixed (ARC) | 62.8% | 1 |
| pooled, leader-elected | 58.3-59.5% | 1-2 |
| each replica deciding alone | 55.5-55.9% | 5 |

**2.3 to 3.9 points** over independent replicas across four runs. The last
column is the mechanism: a replica seeing eight requests an epoch cannot tell
its arms apart, so the fleet scatters across five policies, several of them
poor. Pooled, it has 64 requests an epoch of evidence and holds one.

**Pooling loses when they are not.** Unpaced, it costs 1 to 2 points on uniform
traffic (68.2% vs 70.4% on split zipf) and **5.1 points** on a fleet whose
replicas serve different workloads (36.6% vs 41.7%) — where a fleet-wide
decision is a compromise nobody wanted. Full tables in the README.

**The rule:** pool when your replicas are individually starved, run the same
workload shape, and are numerous enough for the pooled evidence to be
meaningfully thicker. Otherwise let each decide alone — simpler, no store, and
on this evidence better. `Advice()` in observe-only mode tells you which case
you are in before you deploy anything.

### Two bugs worth reading about

Both were reachable in normal operation, and neither was caught by a test that
existed at the time.

- **An unrecognised bandit selection panicked the process.** The epoch loop
  switched to whatever `SelectPolicy` returned without checking the cache held
  it, then dereferenced a nil interface during migration. `Undefined` is the
  natural return from a bandit that has not formed an opinion — which a
  distributed one does for every epoch before its first sync. An unrecognised
  selection now means no change.
- **The default migration strategy stopped purging shadow zeros.** When
  `MigrationStrategy` was renumbered to `iota + 1`, its zero value — the
  documented default — matched no case in the switch, so a policy switch served
  the incoming policy's zero-value shadow entries to callers as real data. That
  is the one invariant this library is built on. Cold is now the `default:`
  arm, so no strategy value can skip the step every strategy must take.

### Also in this release

- **`EpochBandit`**, an optional extension of `Bandit`, delivering a whole
  reporting epoch per call. A bandit that publishes evidence elsewhere needs
  the epoch boundary, an epoch id and which arm was active; the per-arm
  `RecordStats` stream carries none of those.
- **The non-blocking rule is documented on the interface itself**, with a test
  asserting a bandit never waits on its store.
- **`bandit/redis` is tested against real servers**, not only miniredis.
  `make redis-test` and CI both run the suite against Valkey 8 and Redis 7,
  because the adapter leans on three things a fake can be too permissive about:
  `TIME` inside a Lua script, `SET NX PX`, and `HINCRBY` on a key the script
  names itself rather than declaring in `KEYS`.
- Epoch reports arrive in a stable sorted order, so evidence is reproducible
  for anything that hashes, serialises or logs it.

### Known limitations

- **Coordination is free in these measurements and will not be in yours.** The
  fleet replays use an in-process store, so the coordination epoch that looks
  best there is the one a real round trip makes most expensive.
- **Verified against Valkey 8.1.9 and Redis 7.4.10.** Redis Cluster, failover
  and Redis 6 are not covered; `bandit/redis/TESTING.md` records what was
  tested and what was not. Redis 7 / Valkey 7.2 is the floor, because deriving
  a bucket from the server's clock inside a script needs effects replication.
- **Reads still take a lock.** The lock-free path remains deferred: it needs a
  retry protocol across every read delegation, retries are not free of side
  effects, and gradual migration cannot go lock-free at all because promotion
  mutates from inside `Get`.
- **It will not beat a policy you have already measured.** Unchanged from
  v0.1.0, and still the most important sentence here.

### Modules

| Module | Contents | External dependencies |
| --- | --- | --- |
| `as-cache` | core cache, bandit interface | none |
| `as-cache/lfu` | O(1) LFU implementation | none |
| `as-cache/policies` | LRU, LFU, 2Q, Random, TTL adapters | `hashicorp/golang-lru/v2` |
| `as-cache/policies/arc` | ARC (patented by IBM — separate on purpose) | `hashicorp/golang-lru/arc/v2` |
| `as-cache/policies/tinylfu` | W-TinyLFU | `maypok86/otter/v2` |
| `as-cache/metrics` | expvar export | none |
| `as-cache/bandit` | Thompson, Greedy, distributed bandit | none |
| `as-cache/bandit/redis` | Valkey/Redis store for the above | `redis/go-redis/v9` |

### Licence

[Mozilla Public License 2.0](../LICENSE). File-level copyleft: use it in closed
source freely; modifications to these files, if distributed, are shared back.
Every published module carries its own copy, because a Go module zip contains
only its own directory.
