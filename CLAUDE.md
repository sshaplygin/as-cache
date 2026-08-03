# CLAUDE.md - as-cache Project Guidelines

## Project Overview

**as-cache** (Adaptive Selection Cache) is a Go library that uses a Multi-Armed Bandit (MAB) statistical approach to select the cache replacement policy at runtime, measuring candidates against real traffic rather than asking the caller to guess.

Instead of forcing users to choose a fixed eviction algorithm upfront, as-cache runs multiple policies in parallel (shadow caching), measures their hit/miss rates per epoch, and uses Thompson Sampling to select the best-performing policy dynamically.

**Module:** `github.com/sshaplygin/as-cache`
**Go version:** 1.25+
**Status:** Pre-1.0. API may change; measured against published traces (see
README "Evidence"); not yet tagged or published -- `make release-check`.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      AdaptiveCache                          │
│   (Manages active policy + shadow policies, runs Bandit)    │
└─────────────────────────────────────────────────────────────┘
          │ active          │ shadows
          ▼                 ▼
   ┌─────────────┐   ┌─────────────┐   ┌─────────────┐
   │ CacheWrapper│   │ CacheWrapper│   │ CacheWrapper │
   │   (LRU)     │   │   (LFU)     │   │  (future...) │
   └─────────────┘   └─────────────┘   └─────────────┘
          │                 │
          ▼                 ▼
   ┌─────────────┐   ┌─────────────┐
   │  hashicorp  │   │  lfu/simple │
   │    LRU      │   │    LFU      │
   └─────────────┘   └─────────────┘
          │
          ▼
   ┌─────────────┐
   │   Bandit    │ (Thompson Sampling via stitchfix/mab)
   │  SelectPolicy│
   └─────────────┘
```

**Key mechanism:**
1. Every epoch (configurable duration), the background goroutine calls `bandit.SelectPolicy()`
2. Each policy's hit/miss stats feed Beta distribution parameters
3. Bandit samples from distributions, picks the winner, switches active policy
4. Shadow caches receive dummy adds (no real data) to track comparative metrics

---

## File Structure

```
as-cache/
├── CLAUDE.md                    # This file
├── README.md                    # Landing page: pitch, quick start, API, doc index
├── docs/                        # The long-form documentation the README links to
│   ├── design.md                # mechanism, Bandit interface, what is not done
│   ├── configuration.md         # Settings, migration, sampling, stability, tuning
│   ├── policies.md              # ready-made arms and their caveats
│   ├── advisor-mode.md          # ObserveOnly, Advice(), metrics module
│   ├── benchmarking.md          # reproducible replays, benchclient, make evidence
│   ├── fleet.md                 # distributed bandit over Valkey/Redis
│   └── evidence.md              # every measured claim and its table
├── go.mod / go.sum              # Root module dependencies
├── generate.go                  # go:generate stringer directive
│
├── interfaces.go                # Core interface definitions
├── models.go                    # PolicyType, PolicyStats, ShadowStats, GlobalStats
├── errors.go                    # Sentinel errors returned by NewAdaptiveCache
├── settings.go                  # Settings + NewAdaptiveCache validation
├── cache.go                     # AdaptiveCache struct + public cache API
├── epoch.go                     # Epoch loop, bandit reporting, policy selection
├── migration.go                 # Migration strategies (cold/warm/gradual)
├── advice.go                    # ObserveOnly reporting: Advice, PolicyReport
├── shadow.go                    # Promotion/demotion, shadow duty, value dropping
├── sampling.go                  # keySampler + miniature capacity maths
├── stability.go                 # Switch gates (cool-down, min improvement)
├── wrapper.go                   # CacheWrapper (wraps any Cacher, adds stats)
├── policytype_string.go         # Generated: PolicyType.String() via stringer
│
├── policies/                    # Separate module: ready-made policy adapters
│   ├── go.mod / go.sum          # depends on root + hashicorp/golang-lru v2.0.6
│   ├── adapters.go              # NewLRU / NewTwoQueue / NewTTL / NewRandomPolicy
│   ├── adapt.go                 # PartialCacher + AdaptedCache (Resize-by-rebuild)
│   ├── random.go                # RandomCache, implemented from scratch
│   ├── ttl.go                   # TTLCache: own expiry over plain LRU (see below)
│   └── conformance_test.go      # shared Cacher/Policy contract suite
│
├── policies/arc/                # Separate module, SOLELY for patent isolation
│   ├── go.mod / go.sum          # depends on hashicorp/golang-lru/arc/v2
│   └── arc.go                   # ARC adapter via policies.Adapt
│
├── policies/tinylfu/            # Separate module: keeps otter's deps isolated
│   ├── go.mod / go.sum          # depends on maypok86/otter/v2
│   └── tinylfu.go               # W-TinyLFU adapter (natively resizable)
│
├── metrics/                     # Separate module: expvar export (stdlib only)
│   ├── go.mod / go.sum
│   └── metrics.go               # Advisor, Snapshot, Take, Publish
│
├── bandit/                      # Separate module: ready-made Bandits (stdlib only)
│   ├── go.mod / go.sum          # depends on root only
│   ├── thompson.go              # Thompson + Greedy, promoted out of bench
│   ├── sample.go                # Beta/Gamma draws (Marsaglia-Tsang)
│   ├── store.go                 # Store interface: Sync / Window / Decide
│   ├── memstore.go              # In-process Store: tests and fleet simulation
│   ├── config.go                # Config, Mode, EvidenceMode, defaults
│   ├── errors.go                # Sentinel errors from NewDistributed
│   ├── fingerprint.go           # regime: which replicas may pool with which
│   ├── window.go                # decay weighting, evidence cap, Thompson draw
│   ├── distributed.go           # the Bandit surface the cache calls (never blocks)
│   ├── coordinate.go            # the sync goroutine: one round trip per epoch
│   └── snapshot.go              # observability: fallback, leadership, fleet arms
│
├── docker-compose.yml           # Valkey 8 + Redis 7 for `make redis-test`
│
├── bandit/redis/                # Separate module: keeps go-redis out of bandit
│   ├── go.mod / go.sum          # depends on root + bandit + redis/go-redis/v9
│   ├── TESTING.md               # what the real-server runs verified, and did not
│   ├── store.go                 # bandit.Store over Valkey/Redis
│   ├── scripts.go               # Lua: server-clock buckets, SET NX leadership
│   └── keys.go                  # key schema, hash tags, counter field encoding
│
├── bench/                       # Separate module: workloads + evidence harness
│   ├── workload.go              # deterministic zipf/uniform/loop/scan/phase-shift
│   ├── harness.go               # replay, Result, tables
│   ├── fleet.go                 # Shard/Split, paced replay, fleet comparison
│   ├── evidence_test.go         # policy comparison + sampling-fidelity check
│   ├── fleet_test.go            # does pooling beat deciding alone?
│   ├── timeline_test.go         # ActivePolicy() plot over a phase-shift run
│   ├── trace.go                 # real-trace loaders (Twitter/LIRS/ARC formats)
│   ├── memory_test.go           # memory multiplier + allocations
│   └── tuning_test.go           # epoch/migration configuration sweep
│
├── lfu/                         # Separate module: LFU cache
│   ├── go.mod / go.sum
│   ├── lfu.go                   # Thread-safe LFU wrapper with eviction callbacks
│   ├── lfu_test.go              # (stub - needs tests)
│   ├── internal/
│   │   └── list.go              # Doubly-linked list for frequency buckets
│   └── simplelfu/
│       ├── lfu.go               # Core LFU algorithm (O(1) add/get/evict)
│       └── lfu_test.go          # (stub - needs tests)
│
└── examples/
    └── basic/
        ├── go.mod / go.sum
        └── main.go              # HTTP server demo (GET/SET endpoints)
```

---

## Core Interfaces

### `Cacher[K, V]` (interfaces.go)
Standard cache interface compatible with `hashicorp/golang-lru/v2`:
```go
Add(key K, value V) bool
Get(key K) (V, bool)
Remove(key K) bool
Keys() []K
Values() []V
Len() int
Peek(key K) (V, bool)
Purge()
Resize(size int) int
Contains(key K) bool
```

### `Policy[K, V]` (interfaces.go)
Extends `Cacher` with capacity tracking and stats:
```go
Cap() int
GetStats() PolicyStats
ResetStats()
GetType() PolicyType
```

### `Bandit` / `EpochBandit` (interfaces.go)
MAB strategy abstraction. **Both methods run under the cache's write lock, so
an implementation must not block** -- see the distributed-bandit notes below.
`EpochBandit` is an optional extension delivering one whole reporting epoch
per call instead of one arm at a time; a bandit implementing it receives
`RecordEpoch` and never `RecordStats`.

```go
RecordStats(stats ShadowStats)
SelectPolicy() PolicyType
```

---

## Key Types

| Type | Location | Purpose |
|---|---|---|
| `AdaptiveCache[K,V]` | cache.go | Main adaptive cache orchestrator |
| `CacheWrapper[K,V]` | wrapper.go | Wraps any Cacher, adds hit/miss tracking |
| `PolicyType` | models.go | Enum: Undefined, LRU, LFU, TwoQueue, ARC, Random, TTL, TinyLFU |
| `MigrationStrategy` | models.go | Enum: MigrationCold, MigrationWarm, MigrationGradual |
| `PolicyStats` | models.go | Hits + Misses counters |
| `ShadowStats` | models.go | Per-epoch policy performance |
| `GlobalStats` | models.go | Aggregate statistics |

---

## Dependencies

| Package | Version | Role |
|---|---|---|
| `hashicorp/golang-lru/v2` | v2.0.6 | LRU/2Q/expirable (policies module). v2.0.7 also builds; the pin is inertia, not a defect |
| `stitchfix/mab` | v0.1.1 | Multi-Armed Bandit (Thompson Sampling) |
| `redis/go-redis/v9` | v9.21.0 | Valkey/Redis client (`bandit/redis` module only) |
| `alicebob/miniredis/v2` | v2.38.0 | Fake Redis for `bandit/redis` tests (test-only) |
| `gonum.org/v1/gonum` | v0.8.2 | Numerical computing (used by mab) |
| `golang.org/x/exp` | indirect | Used by gonum |
| `stretchr/testify` | v1.11.1 | Test assertions (root module, test-only) |

---

## Code Patterns

### Generics - All cache types use Go generics
```go
type AdaptiveCache[K comparable, V any] struct { ... }
type Cacher[K comparable, V any] interface { ... }
```

### Thread Safety
- `sync.RWMutex` in `AdaptiveCache` guards policy switching
- `sync.RWMutex` in LFU `Cache` guards all operations
- Eviction callbacks invoked outside critical sections

### Context-Based Lifecycle
```go
func New[K comparable, V any](ctx context.Context, ...) *AdaptiveCache[K, V]
// Background goroutine stops on ctx.Done()
```

### Shadow Caching Pattern
- Active policy stores real key/value pairs
- Shadow policies receive `Add(key, zeroValue)` calls to track access patterns
- Stats are reset each epoch after bandit records them

---

## Development Commands

```bash
# Run root package tests
go test ./...

# Run LFU package tests
cd lfu && go test ./...

# Run example
cd examples/basic && go run main.go

# Lint all modules (golangci-lint v2, config in .golangci.yml)
make lint          # or: golangci-lint run ./...  (per module)
make lint-fix      # apply --fix and formatters
make install-tools # install the pinned golangci-lint version

# Run the bandit/redis suite against real servers rather than miniredis.
# docker-compose.yml brings up both Valkey 8 and Redis 7; the suite runs
# against each, because the adapter claims to support both.
make redis-up            # start them and wait for health
make redis-test          # start, run against both, tear down
make redis-down          # stop them
# or, against a server you already have:
AS_CACHE_REDIS_ADDR=127.0.0.1:6379 sh -c 'cd bandit/redis && go test ./...'

# Regenerate stringer (after modifying PolicyType in models.go)
go generate ./...

# Tidy dependencies
go mod tidy
cd lfu && go mod tidy
cd examples/basic && go mod tidy
```

---

## Current Status & Incomplete Features

### Implemented
- [x] `AdaptiveCache.Add()` and `Get()` with shadow policy tracking
- [x] `AdaptiveCache.Remove()` - delegates to active policy, propagates to shadows
- [x] `AdaptiveCache.Purge()` - purges all policies
- [x] `AdaptiveCache.Resize()` - resizes all policies, returns total eviction count
- [x] `AdaptiveCache.Contains()` - delegates to active policy
- [x] `AdaptiveCache.Keys()` / `Values()` / `Len()` / `Peek()` - delegate to active policy
- [x] `AdaptiveCache.Stats()` - cumulative: completed epochs (`globalStats`) plus the active policy's in-progress epoch
- [x] Background epoch goroutine with bandit-based policy selection
- [x] `CacheWrapper` with hit/miss statistics
- [x] LFU implementation (simplelfu + thread-safe wrapper) — all methods implemented
- [x] `lfu.Cache`: `Resize`, `ContainsOrAdd`, `PeekOrAdd`, `RemoveOldest`, `GetOldest`
- [x] `simplelfu.LFU`: `Resize`, `GetOldest`, `RemoveOldest`
- [x] Basic example with HTTP server
- [x] Roadmap Milestone 1 (correctness): locked epoch switch in `runEpoch`; atomic
  `CacheWrapper` counters; active policy's per-epoch stats reported to the bandit
  alongside shadows with all counters reset each epoch (active counts folded into
  `AdaptiveCache.globalStats` so `Stats()` stays cumulative and demotion never
  leaks active-tenure stats into a shadow epoch); constructor validation with
  sentinel errors in `errors.go` (`ErrNilSettings`, `ErrInvalidEpochDuration`,
  `ErrNilBandit`, `ErrNilPolicy`, `ErrDuplicatePolicy`); idempotent `Close()`
  (`sync.Once`) that waits for the epoch goroutine (`sync.WaitGroup`); explicit
  `go vet` CI step and `TestAdaptiveCache_StressAcrossEpochBoundaries` (1ms
  epochs, all three migration strategies, full public API under `-race`).
  Migration strategies moved verbatim from `cache.go` to `migration.go` to keep
  files under the 400-line rule.

- [x] Roadmap Milestone 2 (overhead reduction), three of four items:
  - **Sampled shadow caching** (`sampling.go`, `shadow.go`): `Settings.ShadowSampleRate`
    gates the shadow fan-out on `maphash.Comparable(seed, key) < threshold`. One
    sampler is shared by every policy so all arms measure the same substream --
    per-policy seeds would make their hit rates incomparable. Shadows resize to
    `ceil(rate*Cap)` so each stays a faithful miniature; `MinShadowCapacity`
    (default 256) floors that, raising the *effective rate* rather than only the
    capacity, and disabling sampling when a cache is too small to host a useful
    miniature.
  - **Stats deviate from the roadmap wording deliberately**: sampled counts are
    NOT scaled up by `1/rate`. That would restore magnitude while inventing
    confidence, handing a Beta posterior 20x the evidence collected. Instead the
    *active* arm is measured over the same sampled substream
    (`activeSampledHits`/`activeSampledMisses`, reported by `selectPolicyLocked`),
    so every arm carries equal, honest evidence. `Stats()` still reports real
    unsampled traffic; only the bandit sees the sample.
  - **Value dropping on demotion** (`demoteLocked`): a demoted policy keeps its
    keys (that is its eviction bookkeeping) but its entries are rewritten to the
    zero value, unsampled keys removed, then it shrinks to miniature capacity.
    Rewriting in `Keys()` order preserves LRU recency and leaves LFU relative
    order untouched. Deferred while a gradual window is open, since that window
    promotes out of the source's real values.
  - **Switch stability** (`stability.go`): `MinHitRateImprovement`,
    `SwitchCooldownEpochs`, `MinEpochRequests`, all off at their zero values.
  - **Ordering invariant** (documented on `switchLocked`): *every mutation of a
    policy must happen while that policy is not the active one.* Mutate the
    incoming policy before promoting it, the outgoing one after demoting it.
    This is what keeps a caller from ever reading a policy mid-rewrite.
  - **Test policies**: `mockPolicy` does not enforce capacity (its `Resize` only
    records the new size). That blind spot hid three capacity bugs found in
    review. `evictingPolicy` in `evicting_test.go` does enforce it -- use it for
    anything whose subject is capacity, resizing, or eviction.
  - Measured on an M1 Max with mutex-backed stub policies: `Get` 100 -> 36 ns/op
    with one shadow, 145 -> 38 ns/op with three; `Add` 109 -> 52 and 189 -> 57;
    mixed parallel 184 -> 85. The structural win is that cost becomes flat in the
    number of shadows, so adding policies is nearly free.
  - **Deferred: lock-free reads** (`atomic.Pointer` to the active policy). An
    adversarial safety analysis found the usual seqlock-retry framing unsound and
    surfaced five further hazards -- retry must wrap all six read delegations
    (`Values()` on a stale post-drop state returns N zeros at the right length),
    retries are not side-effect-free (double-counted stats, double-bumped
    recency), the `GetStats`/`ResetStats` split silently discards hits once reads
    are unlocked (fixing it changes the public `CacheStats` interface), and
    `MigrationGradual` cannot go lock-free since `promoteLocked` mutates from
    inside `Get`. Worth its own scoped change.

- [x] Roadmap Milestone 4 (evidence), synthetic half. `make evidence` replays
  the suite; see the README for the tables. The headline finding is negative and
  should not be smoothed over: **adaptive selection never beats the best fixed
  policy on these workloads**, including phase-shift, where W-TinyLFU wins by
  3.8 points. The `ActivePolicy()` timeline shows the bandit working correctly —
  it explores, picks W-TinyLFU, and holds it 90% of the run — but there is no
  crossover to exploit because W-TinyLFU is best in both regimes. What the
  library does deliver is a bound on the cost of guessing wrong (77.5% vs LRU's
  0.0% on `loop`). That argues for Milestone 5 advisor mode as the primary
  product rather than a stepping stone.
  - Milestone 2's sampling was validated here: sampled shadows preserve the
    policy ranking, running 1-3 points pessimistic uniformly across arms.
  - Evidence tests are guarded by `testing.Short()` and excluded from
    `make test`, which now passes `-short`. Under `-race` the epoch pacing
    changes ~15x and the measurements become meaningless. Run `make evidence`.
  - The root module ships no `Bandit`; `bench/bandit.go` has a Thompson
    sampler (Beta posteriors via Marsaglia-Tsang gamma draws, with discounting
    so it can change its mind) and a greedy control. Worth promoting if
    advisor mode lands.

- [x] Roadmap Milestone 4 (evidence), real traces. `./scripts/fetch-traces.sh`
  downloads five published traces; none are committed (see `.gitignore`).
  Loaders self-test against published record/distinct counts, and the ARC
  layout's range expansion is asserted -- each record stands for `blockCount`
  accesses, and reading it as one-key-per-line would silently produce a
  workload incomparable with the literature.
  - **The real traces overturned the synthetic conclusion.** The best fixed
    policy varies by trace: 2Q wins on Twitter Twemcache and ARC OLTP,
    W-TinyLFU on ARC P3 and the LIRS traces. On OLTP, W-TinyLFU is
    second-*worst*. Tuned sensibly (50ms epoch, warm migration), adaptive lands
    within ~1 point of the best fixed policy and beats it on P3 by 0.76.
  - **Epoch duration is the setting that matters.** A 2ms epoch on a 20k cache
    means copying the cache hundreds of times per replay: 13,476 ns/op and a
    7-point hit-rate loss on P3. `MigrationCold` costs 28 points on OLTP. The
    stability gates cost 37 points on `loop`, which must re-adapt constantly.
  - Memory: six policies cost 2.65x a single LRU, not 6x (shadows hold keys,
    never values); 1.32x with sampling. The old README claim was wrong.
- [x] Roadmap Milestone 5 (advisor mode). `Settings.ObserveOnly` measures every
  arm while guaranteeing the cache behaves exactly like the policy it was built
  with; `Advice()` reports which policy wins and by how much. The bandit may be
  nil in this mode (implementing one is the fiddliest part of using the
  library, and nothing is ever selected), and the `EvictPartialCapacityFilling`
  capacity gate is bypassed, since it exists to avoid switching on thin
  evidence and would otherwise suppress the very measurement being asked for.
  The `metrics` module publishes a `Snapshot` through expvar, evaluated on
  scrape, with a duplicate-name check so a registration mistake returns an
  error instead of panicking the process.
  - **`tenureStats`, not lifetime stats.** A policy's measurements are cleared
    when it changes role. Accumulating across a role change pooled its active
    tenure (full capacity, all traffic) with its shadow tenure (miniature
    capacity, a sample), and left the outgoing policy's long history
    outweighing the incoming one's short history -- so right after a correct
    switch, `Advice` named the policy the cache had just moved away from as
    best, for a number of epochs linear in the history length. Do not
    "improve" this by accumulating for longer.
  - **`Advice.Epochs` counts reporting epochs, not ticks.** The capacity gate
    can skip measurement indefinitely, and `epochID` would report thousands of
    epochs of evidence behind nothing.
  - Ties in `Advice` are broken by `PolicyType`: ranging a map and sorting
    stably on hit rate alone made `Best` flap between equally-performing arms
    on an unchanged cache.
  - `metrics.Publish` needs both the mutex and the `recover`: `expvar.Get`
    followed by `expvar.Publish` is check-then-act, and expvar panics on a
    duplicate name.

- [x] LFU added to `policies` (`NewLFU`) and to the evidence suite. Putting it
  through the shared conformance suite for the first time found a real bug:
  it accepted entries at zero capacity, where every other policy holds nothing.
  A pre-existing test asserted the buggy behaviour (that the entry stayed
  retrievable) and was corrected -- its purpose was guarding a panic, and the
  retrievability assertion was incidental.
  - Evidence: LFU is the **best** policy on synthetic `zipf` (73.5%) and the
    **worst** on both large real traces (41.4% Twitter, 45.4% OLTP). Synthetic
    Zipf holds popularity stationary, which is exactly LFU's assumption; real
    traffic shifts and stale frequency counts pin dead entries. This is the
    clearest evidence in the repo that synthetic workloads mislead.

- [x] Distributed bandit (`bandit`, `bandit/redis`). A fleet pools its
  per-epoch counts through Valkey/Redis so replicas that individually see too
  little traffic to rank their arms can still select. Five findings shaped it:
  - **The `Bandit` interface runs under the cache's write lock.** `RecordStats`
    and `SelectPolicy` are both called from `selectPolicyLocked`, inside
    `runEpoch`'s `c.mu.Lock()`. Go's `RWMutex` queues new readers behind a
    waiting writer, so a blocking call there stalls every `Get` in the process
    for its duration -- a store timeout becomes a cache outage. `Distributed`
    therefore buffers in `RecordEpoch` and serves `SelectPolicy` from an
    `atomic.Int64`; all I/O is on its own goroutine. The non-blocking rule is
    now documented on `Bandit` itself, and asserted by
    `TestDistributed_RecordAndSelectNeverWaitOnTheStore`.
  - **Two clocks.** The tuned single-node epoch is 50ms, which is below both a
    round trip and any fleet-wide clock agreement. `CoordinationEpoch` is a
    separate, seconds-scale clock. Buckets are derived from the *store's* clock
    inside Lua (`redis.call('TIME')`), so no replica's clock is ever consulted
    and a skewed machine cannot poison a window. Needs Redis 7 / Valkey 7.2 for
    effects replication.
  - **Decay cannot be applied in place by N writers.** Multiplying a shared
    counter once per replica per epoch means a fleet of fifty forgets fifty
    times faster than a fleet of one. The store holds plain per-bucket sums;
    `aggregate` applies `decay^age` on read. Same arithmetic at any fleet size.
  - **Pooling multiplies evidence by fleet size, which kills exploration.** A
    Beta posterior narrows with the square root of its evidence, so a thousand
    replicas produce draws that always return the same arm. `capEvidence`
    bounds the effective sample size, preserving the rate exactly.
    `TestCapEvidence_RestoresExploration` is the demonstration: uncapped, a
    marginally worse arm is drawn zero times in 200.
  - **The active/shadow role gap is why `ModeLeader` is the default.** The
    active arm is measured at full capacity, shadows on miniatures running 1-3
    points pessimistic. Under leader election every replica has the same arm in
    the flattering role so the bias cancels on summing; under
    `ModeSharedPosterior` it is asymmetric and compounds with deployment share.
    `EvidenceShadowOnly` removes it and is rejected under `ModeLeader`, where
    the fleet-wide active policy is nobody's shadow and would have no evidence
    at all (`ErrShadowOnlyUnderLeader`).
  - **Replicas only pool when they measure the same thing.** `Namespace` is
    suffixed with a fingerprint of arms + capacity + sample rate. Pooling a
    1000-entry cache's hit rate with a 100-entry one produces a number that
    describes neither, and nothing in the counts would reveal it. Epoch
    duration is deliberately *not* in the fingerprint: reporting twice as often
    contributes twice the counts at the same rate, and rates are what is
    compared.
  - **The adapter suite runs against miniredis by default and against real
    servers via `make redis-test`.** A fake can be too permissive about
    precisely what this leans on -- `TIME` inside a script, `SET NX PX`, and
    `HINCRBY` on a key the script names itself rather than declaring in `KEYS`
    -- so `docker-compose.yml` brings up both Valkey 8 and Redis 7 and the
    suite runs against each. Testing one engine while documenting two is how a
    support claim quietly stops being true. Verified against Valkey 8.1.9 and
    Redis 7.4.10: 20 tests, 0 skips, 0 failures on each. See
    `bandit/redis/TESTING.md`, which also records what is *not* covered
    (Redis Cluster, failover, Redis 6) and the zsh word-splitting trap that
    makes a hand-run two-engine loop silently test the fake instead.
  - Root-module changes this needed, both additive: the optional `EpochBandit`
    interface (a bandit that must key evidence externally needs the epoch
    boundary, the epoch id and which arm was active -- none of which the
    per-arm `ShadowStats` stream carries), and `EpochReport.Capacity` /
    `SampleRate` for the fingerprint.

- [x] **Bug found and fixed: an unrecognised bandit selection panicked the
  process.** `runEpoch` switched to whatever `SelectPolicy` returned without
  checking the cache actually had it; `migrateData` then looked the missing
  policy up in the map and dereferenced a nil interface. `Undefined` is the
  natural return from a bandit that has not formed an opinion -- which a
  distributed one does for every epoch before its first sync -- so this was
  reachable in normal operation. Now guarded by `hasPolicy`; an unrecognised
  selection means no change, which is what the docs already implied.

- [x] **Bug found and fixed: the default migration strategy stopped purging
  shadow zeros.** When `MigrationStrategy` was renumbered to `iota + 1`, its
  zero value -- the documented default -- stopped matching any case in
  `migrateData`'s switch, so a switch skipped purging the incoming policy's
  zero-value shadow entries and served them to callers as real data. Cold is
  now the `default:` arm rather than a named case, so no strategy value can
  fall through the one step every strategy must take.
  `TestMigration_DefaultStrategyNeverServesAShadowZero` covers all four values.
  The same renumbering hit `bandit.Mode`, where a zero value claimed no
  leadership and followed no leader; `validate` now resolves it explicitly.

- [x] Evidence: **pooling helps only in the regime it was built for.** Paced to
  ~8 requests per cache epoch per replica, a pooled fleet gains 2.3-3.9 points
  over replicas deciding alone (58-59% vs 55.5%), and the mechanism is visible
  in the endings: starved replicas scatter across 5 policies, the pooled fleet
  holds 1. Unstarved, pooling *loses* -- 1-2 points on uniform traffic, 5.1
  points on a fleet whose replicas serve different workloads, where one
  fleet-wide policy is a compromise nobody wanted.
  - **The unpaced fleet tests measure the wrong regime.** `Replay` runs flat
    out, so it delivers thousands of requests per epoch however small the
    workload; a smaller workload just finishes sooner. `ReplayPaced` holds a
    request rate, which is the only way to reproduce thin traffic, and it costs
    wall-clock time to do so. Do not "speed up" the paced test by unpacing it.
  - The coordination-epoch sweep (10/25/50/200ms: -0.41/-0.67/-2.21/-3.47 vs
    local) shows most of the unstarved loss is the fleet getting fewer chances
    to change its mind -- but it closes towards break-even, never past it, and
    the fastest setting is the one a real round trip makes most expensive.
    These replays use `MemStore`, so coordination is free in a way it will not
    be in production.
  - `bench/bandit.go` is gone; `bench` imports the `bandit` module.

### Incomplete / TODO

- [x] Data migration between policies on switch — `MigrationStrategy` in `Settings` (`MigrationCold` default, `MigrationWarm` copies all keys from old active to new active)
- [x] Unit tests for LFU packages (simplelfu: 98.3% coverage, lfu wrapper: 100% coverage)
- [x] Unit tests for root package (cache_test.go: 93.6% coverage -- CacheWrapper, AdaptiveCache delegated methods, tryChangePolicy, epoch-based switching, constructor edge cases, concurrent access)
- [x] Roadmap Milestone 3 (policy coverage), two of three items — LRU, 2Q,
  Random, TTL and ARC adapters. Four findings shaped the design:
  - **ARC is patented by IBM** (US 6,996,676). Upstream `hashicorp/golang-lru`
    split it into its own module in v2 for that reason; `policies/arc` keeps
    that split so importing `policies` never pulls a patented implementation
    into a build. Do not merge it into `policies` for convenience.
  - **Neither 2Q nor ARC satisfies `Cacher`**: their `Add`/`Remove` return
    nothing and neither has `Resize`, which the miniature-shadow mechanism
    depends on. One shared `policies.Adapt` covers both. Its `Resize` rebuilds
    the cache, discarding the algorithm's learned adaptation state — so an
    adapted policy resized every epoch would stay permanently unadapted and
    under-report its own hit rate.
  - **`TTLCache` deliberately does NOT use `expirable.LRU`.** Three defects
    made that untenable, all found in review: its `Get`/`Peek` return
    `(zeroValue, true)` for an expired-but-unreaped entry (a bare `return` over
    an already-true named result) -- a zero-value leak, the one invariant this
    library is built on; its `Values` returns a full-length slice padded with
    trailing zeros that no longer line up with `Keys`; and `NewLRU` starts a
    reaper goroutine per cache with no way to stop it, leaking the goroutine
    and the whole cache forever. `TTLCache` instead stores a `ttlEntry{value,
    expiresAt}` in a plain `lru.Cache` and expires lazily on read. It also
    makes size 0 mean *empty*, where `expirable` documents 0 as *unlimited* --
    which, since shadows are resized automatically, would have turned a
    bounded shadow into an unbounded one.
  - **`Keys()` order is not portable.** 2Q returns frequent-then-recent, ARC
    returns recent-then-frequent; neither is a global recency order. So
    `AdaptedCache.Resize` replays every entry and lets the rebuilt cache pick
    its own victims -- which entries survive a shrink is explicitly not
    meaningful. Any "keep the tail" rule is right for one cache and exactly
    backwards for the other.
  - **`RandomCache.Add` must evict before inserting.** Inserting first puts the
    caller's own write into the victim draw, losing it with probability
    1/(size+1) -- a write accepted and gone before the next read. No other
    policy here does that, and no conformance test caught it until one was
    added that fills a cache and then reads the fresh key back.
  - **golang-lru v2.0.7 builds fine; an earlier note here said otherwise and
    was wrong.** The claim was that its published `simplelru` imports
    `.../simplelru/internal`, which the module does not contain. What actually
    happened is that one machine's module cache held a corrupted extraction of
    v2.0.7 whose `simplelru/lru.go` line 9 read
    `github.com/hashicorp/golang-lru/v2/simplelru/internal`, where the
    published file reads `github.com/hashicorp/golang-lru/v2/internal`. The
    v2.0.6 and v2.0.7 zips have identical file lists and both contain
    `internal/list.go`; a build against v2.0.7 with an empty `GOMODCACHE`
    succeeds. The pin at v2.0.6 is therefore only inertia, not a defence.
    - The lesson is about the evidence, not the version: "verified against the
      checksum database" was asserted but never actually run. When a
      dependency looks broken, compare the module zip from `proxy.golang.org`
      against the extracted copy in `GOMODCACHE` before concluding anything is
      upstream -- `go mod verify` does not re-hash an already-extracted tree.
- [x] W-TinyLFU arm (`policies/tinylfu`, over `maypok86/otter` v2) — the
  baseline that actually needs beating. otter is natively resizable, so unlike
  2Q and ARC this arm never rebuilds and keeps its frequency sketch. Caveat:
  otter reports an *approximate* size, so `Len()` is approximate and the
  `EvictPartialCapacityFilling=false` capacity gate (which compares `Len()` to
  `Cap()` for exact equality) may not fire — set it to true when this arm is in
  play.
- [x] README Idea section, rewritten to describe the mechanism as built:
  per-request (active serves, sampler gates, shadows take keys only) and
  per-epoch (equal evidence per arm, bandit names an arm, stability gates and
  migration). The stale `## TODO` section, whose two items both shipped in
  0.1.0, is replaced by `## What is not done` -- lock-free reads, unsteppable
  wall-clock epochs, no adaptive sizing, no production use.

- [x] CI covers every module. `bandit` and `bandit/redis` were absent from both
  matrices in `.github/workflows/ci.yml`, so the newest and most
  concurrency-sensitive module in the repo was checked by nothing but a local
  `make test`. Both matrices now mirror the Makefile's `MODULES` (keep them in
  step). A separate `redis` job runs the `bandit/redis` suite against Valkey 8
  and Redis 7 service containers -- the CI equivalent of `make redis-test` --
  because miniredis is exactly the kind of fake that is too permissive about
  what this adapter leans on. `AS_CACHE_REDIS_ADDR` is set as a step `env`
  rather than exported inline, which is the same word-splitting trap
  `bandit/redis/TESTING.md` documents: get it wrong and the job silently tests
  the fake while reporting that it tested a server. Verified green on the merge
  commit: 24 jobs, both engines included.

- [x] `release-check` runs in CI and in `make all`. It ran in neither: this
  file claimed it was "part of `make all`" while the target was `fmt vet lint
  test`, so the one gate that catches an unreleasable module was purely
  opt-in, and the failure it guards against is invisible until a stranger's
  `go get` fails. Both are now wired up, and the claim is true.

- [x] **Request-counted epochs and the `benchclient` module**, so the cache can
  enter a benchmark suite (`maypok86/benchmarks`) and produce a number worth
  quoting. Four findings, in the order they surfaced:
  - **`Settings.EpochRequests` ends an epoch every N `Get` calls.** A
    wall-clock epoch makes adaptation a function of machine speed: replaying
    one trace on a loaded machine re-evaluates fewer times and lands on a
    different hit rate. `Get` is the unit because hits and misses are recorded
    there and nowhere else. The counter subtracts the limit rather than
    resetting to zero, so requests arriving mid-crossing are still counted, and
    the trigger compares `== limit` so exactly one goroutine runs each epoch.
    It runs on the caller's goroutine, outside every lock the `Get` took --
    `runEpoch` needs the write lock.
  - **The Thompson bandit was not reproducible even when seeded.**
    `SelectPolicy` drew one sample per arm while ranging a map, so map
    iteration order decided which arm got which draw. The seed fixes the
    sequence of numbers, not the recipients. This is the same defect as the
    epoch-report ordering fixed in 0.2.0, in the module written to fix it.
    Anything that consumes randomness while iterating a map has this bug.
  - **Determinism is a property of the whole stack, and one arm breaks it.**
    With the bandit fixed, LRU/LFU/2Q/Random replay identically; W-TinyLFU does
    not. Measured with no cache above it, one trace gave three hit counts and
    left 527, 504 and 545 entries at capacity 500 -- otter evicts
    asynchronously and reports an approximate size. Hence `DefaultArms`
    excludes it and `ArmsWithWindowTinyLFU` exists to make that trade
    explicitly. Do not "improve" the default by adding the best arm to it.
  - **`benchclient` imports nothing from any benchmark suite.** The contract is
    five methods and Go interfaces are structural, so a local `contract`
    interface plus a `var _` assertion pins it without a dependency, and breaks
    the build rather than the registration if it ever drifts.

### Releasing 0.2.0

Root was tagged `v0.1.0` and `v0.1.1`; **no submodule had ever been tagged**, so
`policies`, `metrics` and the rest were unconsumable at any version. 0.2.0 is
the first release to tag the whole graph, uniformly, including modules that did
not change -- each sibling's `require` has to move to the root's new version
anyway. All eight are published and were verified by resolving them from
proxy.golang.org into a module with no `replace` directives.

Three things learned doing it, none of which are obvious from the outside.

- **`release-check` passed a module it should have failed.** Its grep was
  anchored to requires written inside a `require (` block, so `policies`'
  standalone `require github.com/sshaplygin/as-cache/lfu v0.0.0-000101...`
  line was never examined. It reads `go mod edit -json` now, which is the
  parse the go command itself uses, so no layout can hide an entry.
- **Verify the chain before pushing a tag, not after.** A bare clone plus
  `GOPROXY=direct` and a `GIT_CONFIG_*` url rewrite resolves the require lines
  exactly as a stranger would, with no global git config touched and nothing
  published. Module versions are immutable once the proxy fetches them.
- **A message-only amend is safe after tagging; a tree change is not.** The
  module zip is built from the file tree, so rewriting a commit message and
  moving the tags leaves every `h1:` hash identical (checked against the
  checksum database before and after). Adding so much as a file under `docs/`
  would not -- that is inside the root module, and its hash is already
  recorded. Anything landing after a tag has to be a commit after the tag.

The CHANGELOG now also carries a `[0.1.1]` section. The MPL licence work was
filed under `[0.1.0]`, which shipped before the LICENSE files existed.

---

## Implementation Plan

### Phase 1: Test Coverage
Priority: fill empty test stubs before adding new features.

**`lfu/simplelfu/lfu_test.go`** -- DONE (98.3% coverage)
- Test Add/Get/Contains/Peek/Remove/Purge/Keys/Values/Len
- Test eviction behavior (least-frequently-used item removed)
- Test frequency increment on repeated access
- Edge cases: empty cache, single item, duplicate keys
- Bug fixes applied: removed double Freq increment in Add, fixed Keys/Values slice init
- Bucket-index invariant enforced: every bucket in `evictList` holds at least one
  entry, and `minFreq` always addresses a live bucket while the cache is non-empty.
  `Add`'s eviction path and `removeElement` previously left an emptied bucket in
  the map, so a later `minFreq` recompute selected it and panicked on a nil
  dereference (`GetOldest`/`RemoveOldest`/`Resize`). Entry removal now funnels
  through `detach`/`recomputeMinFreq`, `Add` only evicts when the cache is
  non-empty (so `Resize(0)` + `Add` cannot panic), and the lookup helpers degrade
  to a miss instead of panicking if the index is ever corrupted.

**`lfu/lfu_test.go`** -- DONE (100% coverage)

- `Resize`, `ContainsOrAdd`, `PeekOrAdd`, `RemoveOldest` and `GetOldest` are
  covered directly; they are fully implemented (there are no panic stubs) but
  were previously untested, which is why the simplelfu bucket-index panics went
  unnoticed -- those methods are their public entry points.
- Test thread-safe wrapper around simplelfu
- Test eviction callbacks (buffered channel, DefaultEvictedBufferSize=16)
- Test concurrent Add/Get under race detector
- Concurrent tests for mixed operations, purge-while-reading, keys/values

**Root package tests (`cache_test.go`)** -- DONE (93.6% coverage)
- `CacheWrapper`: hit/miss stats tracking, GetStats/ResetStats, Cap, Name, GetType, delegated methods
- `AdaptiveCache`: Stats, Resize, Contains, Keys, Values, Len, Peek, ActivePolicy
- `AdaptiveCache`: Add/Get with epoch-based switching, tryChangePolicy (switch, no-switch, skip-when-not-full)
- Test bandit integration with mock bandit (recordingBandit verifies shadow stats delivery)
- Test context cancellation stops background goroutine (Close)
- Constructor edge cases: empty policies, nil policies
- Remove propagates to shadows, Purge clears all policies
- Concurrent access to all delegated read methods under race detector
- Migration strategy tests: Cold, Warm, Gradual (promotion, drain, zero-value safety, concurrent)
- All assertions use `testify/require` (fatal) and `testify/assert` (non-fatal) — no bare `t.Fatal`/`t.Error`
- Note: `tryChangePolicy()` returns `PolicyType` (the bandit's selection), not `bool`; tests compare against `LRU`/`LFU` constants

### Phase 2: Complete AdaptiveCache Methods
Implement the missing methods that currently return zero values:
- `Remove(key K)`: delegate to active policy, propagate to shadows
- `Purge()`: purge all policies
- `Contains(key K)`: check active policy
- `Keys()` / `Values()` / `Len()` / `Peek()`: delegate to active policy
- `Resize(size int)`: resize all policies
- `Stats()`: aggregate hit/miss from all wrappers

### Phase 3: Policy Migration on Switch -- DONE

`MigrationStrategy` enum added to `models.go`; `Settings.MigrationStrategy` field controls behaviour:

- `MigrationCold` (default, 0): start fresh — simple, causes temporary miss spike
- `MigrationWarm`: on switch, purge zero-value shadow entries from new active policy, then copy all key/value pairs from old active via `Keys()`+`Peek()`

- `MigrationGradual`: Get-time promotion (during the window, `Get` takes the write lock and promotes the eligible key from the old policy into the new active BEFORE the counted lookup, so promoted requests register as active-policy hits — not misses) + Add-time drain (one key migrated per Add call). Migration window closes when no eligible keys remain (drained, promoted, or removed), on `Purge()`, on the next policy switch, and unconditionally at the next epoch boundary (`runEpoch` calls `closeMigrationLocked` first, so a workload that stops touching pending keys cannot leave the source pinned at full capacity holding real values). Switching away from an empty policy never opens a window.

Bug fix applied during implementation: all three strategies now purge shadow zero-value entries from the new active policy at switch time, so callers never observe a shadow zero as a real cached value.

### Phase 4: Additional Policies
Add wrappers for:
- `hashicorp/golang-lru/v2/expirable` (TTL-based)
- `hashicorp/golang-lru/v2` 2Q variant
- Random eviction policy

Each new policy only needs to implement the `Cacher` interface and be wrapped by `CacheWrapper`.

---

## Testing Guidelines

- Use `go test -race ./...` to catch race conditions (mandatory given concurrent design)
- Mock the `Bandit` interface to test policy switching deterministically
- Use table-driven tests for cache operation coverage
- Test epoch transitions with short durations (e.g., 1ms) in tests
- Minimum 80% coverage target
- Use `require.*` for assertions that must stop the test on failure (setup, preconditions)
- Use `assert.*` for assertions where the test can continue (value checks, multiple independent conditions)
- Do not use bare `t.Fatal`, `t.Fatalf`, `t.Error`, or `t.Errorf` — use testify instead
- All three test packages (`ascache`, `lfu`, `simplelfu`) use `github.com/stretchr/testify`

---

## Releasing

This is a multi-module repository, which has one trap that will bite anyone who
tags without knowing about it.

Every sibling module depends on the others through a `replace` directive
pointing at a local path. That is what makes local development work -- and
**`replace` directives are ignored when a module is consumed as a dependency**.
So `policies/go.mod` requiring `github.com/sshaplygin/as-cache v0.0.0` builds
and tests perfectly here while being impossible for anyone else to use:

    reading github.com/sshaplygin/as-cache/go.mod at revision v0.0.0:
    unknown revision v0.0.0

`make release-check` catches this; it is part of `make all`. Do not tag until it
passes.

The licence is MPL 2.0, and **every publishable module carries its own copy**.
A Go module zip contains only its own directory, so a root-only LICENSE never
reaches someone fetching `policies/arc` or any other submodule. Upstream
`hashicorp/golang-lru` ships a LICENSE inside its `arc` submodule for the same
reason. `release-check` verifies this per module.

Releasing therefore goes bottom-up through the dependency graph -- root, then
`lfu`, then `policies`, then `policies/arc`, `policies/tinylfu` and `metrics` --
updating each module's `require` to the version its dependency was just tagged
at. `bench` and `examples/*` are internal and are never tagged.

Go module versions are immutable once the module proxy has fetched them. A
broken `v0.1.0` cannot be replaced, only superseded, so verify the whole chain
resolves before pushing any tag.

## Rules

1. No emojis in code, comments, or documentation
2. All public types must have godoc comments
3. Run `go vet ./...` before committing
4. Keep each file under 400 lines; split by responsibility
5. No `panic` except in initialization failures or truly unimplemented stubs
6. Eviction callbacks must not be called while holding a mutex
7. All new policies must implement `Cacher[K, V]` and be wrapped via `CacheWrapper`
8. Update `PolicyType` enum and regenerate stringer when adding new policies
