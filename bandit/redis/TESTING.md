# Testing the Valkey/Redis store

This adapter is the only part of as-cache that depends on a server being there
and behaving. Everything else can be tested by calling it; this cannot.

The suite runs against [miniredis](https://github.com/alicebob/miniredis) by
default so `make test` stays offline, and against real servers on demand. Both
matter, for different reasons.

## Why a fake is not enough here

The adapter leans on three things that a fake can accept while a real server
rejects them, or accept in a way a real server would not:

1. **`redis.call('TIME')` inside a Lua script.** This is how a bucket is
   derived from the *server's* clock rather than a replica's, which is what
   frees a fleet from needing clock agreement. It requires effects replication;
   on Redis before 7.0 a script calling a non-deterministic command before a
   write was rejected outright. A fake with a permissive Lua interpreter will
   happily run a script that a Redis 6 server refuses.
2. **Keys the script names itself.** A bucket is not known when the call is
   made, so `syncScript` builds its key names from a base passed in `ARGV`
   rather than declaring them in `KEYS`. That works because every key for a
   namespace carries the same hash tag and therefore hashes to one slot. A
   single-instance fake has no slots at all, so it cannot notice a mistake
   here.
3. **`SET ... NX PX` truthiness and reply shapes.** Leadership depends on
   distinguishing "I claimed it" from "someone else has it", and the sync reply
   is a Lua table whose third element is deliberately an empty string rather
   than a nil, because a nil inside a Lua table truncates the array Redis
   builds from it and would silently take the bucket and leader values with it.

Testing one engine while the documentation claims two is how a support claim
quietly stops being true, so both are covered.

## Running it

```sh
make redis-up      # start Valkey and Redis, wait for their healthchecks
make redis-test    # start both, run the suite against each, tear down
make redis-down    # stop both and remove their volumes
```

Against a server you already have:

```sh
AS_CACHE_REDIS_ADDR=127.0.0.1:6379 go test -race -count=1 ./...
```

[`docker-compose.yml`](../../docker-compose.yml) in the repository root defines
both servers. Ports are deliberately off the defaults — Valkey on `63799`,
Redis on `63798` — so this cannot collide with a Redis you are already running
for something else. Neither container persists anything.

### A shell trap worth knowing

Do not write the two-engine loop as

```sh
for target in "valkey 127.0.0.1:63799" "redis 127.0.0.1:63798"; do
    set -- $target
    AS_CACHE_REDIS_ADDR=$2 go test ./...     # WRONG under zsh
done
```

**zsh does not word-split unquoted parameter expansions**, so `$2` is empty,
`AS_CACHE_REDIS_ADDR` is unset, and the run silently falls back to miniredis
while printing every sign of having used a real server. It passes, quickly, and
proves nothing. The `redis-test` recipe in the Makefile is safe because make
runs recipes under `/bin/sh`, which does split. When running by hand, pass the
address literally.

The tell is `TestStore_CountersExpire`: it waits out a real TTL on a real
server and takes about 0.6s, and fast-forwards a fake clock in 0.00s.

## Verified

Run on 2026-07-26, macOS 26.2 arm64, Go 1.25.5, `go test -race -count=1`.

| Backend | Version | Result |
| --- | --- | --- |
| miniredis | v2.38.0 | 20 passed, 0 skipped, 0 failed |
| Valkey | 8.1.9 (reports `redis_version` 7.2.4) | 20 passed, 0 skipped, 0 failed |
| Redis | 7.4.10 | 20 passed, 0 skipped, 0 failed |

```text
==> bandit/redis against valkey (127.0.0.1:63799)
ok  	github.com/sshaplygin/as-cache/bandit/redis	7.081s
==> bandit/redis against redis (127.0.0.1:63798)
ok  	github.com/sshaplygin/as-cache/bandit/redis	7.108s
```

Every test runs against every backend; nothing is skipped anywhere. The two
slow tests are slow on purpose: `TestStore_ReportsAnUnreachableServer` waits
out a dial timeout against a closed port, and `TestStore_CountersExpire` waits
out a real TTL.

## What each test establishes

| Test | What it would catch |
| --- | --- |
| `TestNew_RejectsANilClient` | A store constructed with nowhere to talk to |
| `TestStore_SyncReportsAServerDerivedBucket` | A script returning a constant instead of reading the server clock; the bucket is checked against the client's own clock, within tolerance |
| `TestStore_SumsCountsAcrossReplicas` | Counts overwriting rather than accumulating — the whole point of pooling |
| `TestStore_KeepsRolesApart` | Active-role and shadow-role counts merging, which would hide the measurement bias the two modes exist to manage |
| `TestStore_NamespacesDoNotPool` | Two fleets sharing a store and silently pooling each other's evidence |
| `TestStore_OneLeaderPerBucket` | Two replicas both believing they lead, which under `ModeLeader` means two decisions for one epoch |
| `TestStore_LeadershipIsNotClaimedUnlessAsked` | A shared-posterior replica claiming leadership it never wanted, starving whoever did |
| `TestStore_DecisionIsImmutableAndReportsWhatIsInForce` | A decision changing under replicas mid-epoch, and a leader that lost a race running its own draw alone |
| `TestStore_SyncReadsBackAPublishedDecision` | Followers never seeing what the leader decided |
| `TestStore_WindowOmitsBucketsThatHoldNothing` | A missing bucket read as a measured zero hit rate |
| `TestStore_WindowOfAnInvertedRangeIsEmpty` | A reversed range spinning or erroring instead of returning nothing |
| `TestStore_ZeroCountsCreateNoCounters` | An arm that measured nothing being recorded as an arm that measured badly |
| `TestStore_CountersExpire` | Counters outliving their window, so a leader decides on stale traffic |
| `TestStore_EveryKeyItWritesHasATTL` | Any key written without an expiry — a fleet that stops running must leave nothing behind in a shared store |
| `TestStore_EveryKeyForANamespaceSharesOneSlot` | A key missing its hash tag, which breaks the window pipeline and the scripts' computed key names on Redis Cluster |
| `TestStore_SurvivesAStrayFieldWrittenBySomethingElse` | A foreign field in a counter hash breaking a fleet's read of its own counters |
| `TestStore_ReportsAnUnreachableServer` | A store failure being swallowed, which would leave the bandit waiting instead of falling back to local selection |
| `TestStore_RespectsContextCancellation` | A call that ignores its context, which would stop `Close` from cancelling an in-flight round trip |
| `TestParseCountField_RoundTrips` | The field encoding and its parser drifting apart |
| `TestParseCountField_RejectsJunk` | Malformed fields being parsed into plausible-looking counts |

## Not covered

- **Redis Cluster.** The hash tags are verified against a real keyspace, so the
  property clustering depends on holds, but nothing here runs against an actual
  clustered deployment. Cross-slot behaviour is unproven.
- **Redis 6 and earlier.** Documented as unsupported rather than tested as
  unsupported. The expected failure is the sync script being rejected for
  calling `TIME` before a write.
- **Failover, replication lag and partitions.** A replica losing the store is
  covered in the `bandit` module's own tests via a store that fails on demand;
  what a real Valkey does mid-failover is not.
- **Load at fleet scale.** The cost model in the package documentation — one
  round trip per replica per coordination epoch, two more for the leader — is
  arithmetic, not a measurement.
