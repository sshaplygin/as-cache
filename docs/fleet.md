# Running a fleet

One replica of a service sees one replica's traffic. If you run fifty of them
behind a load balancer, each cache sees a fiftieth of the requests, and the
"you cannot give it enough traffic per epoch to measure anything" caveat stops
being about your traffic and starts being about how it was divided.

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

Read [the fleet evidence](evidence.md#does-pooling-across-a-fleet-help) before
reaching for it. The answer is yes in one specific regime -- replicas
individually too starved of traffic to rank their own arms -- and no everywhere
else, and which case you are in is measurable in advance.

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

## Which replicas pool with which

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

## The two modes

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

## Pooling changes how much evidence a posterior sees

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
