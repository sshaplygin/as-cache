# Getting started

## Install

The core module has no dependencies. The ready-made policies and the bandits
live in companion modules, so you only pull in a cache library if you use one.

```bash
go get github.com/sshaplygin/as-cache
go get github.com/sshaplygin/as-cache/policies
go get github.com/sshaplygin/as-cache/bandit
```

## A working cache

```go
lru, _ := policies.NewLRU[string, int](10000)
twoQ, _ := policies.NewTwoQueue[string, int](10000)

cache, err := ascache.NewAdaptiveCache(
    []ascache.Policy[string, int]{lru, twoQ},
    bandit.NewThompson(0.9, 1), // discount, seed
    &ascache.Settings{EpochDuration: time.Minute, ShadowSampleRate: 0.05},
)
if err != nil {
    return err
}
defer cache.Close()

cache.Add("k", 1)
v, ok := cache.Get("k")
```

That is the whole surface: build some arms, hand them to a bandit, and use the
result as you would any cache. Which arms are worth carrying is
[policies](policies.md); what the `Settings` fields do, and which ones actually
move the numbers, is [configuration](configuration.md).

Two runnable examples ship with the repository:

- [examples/basic/main.go](../examples/basic/main.go) — an HTTP server over an
  adaptive cache, driven by `bandit.NewThompson`.
- [examples/migration/main.go](../examples/migration/main.go) — the three
  migration strategies side by side.

## Start by observing instead

If you are not ready to let a cache change its own eviction policy under you,
run it in observe-only mode. The cache behaves exactly like the policy you
built it with, every other arm is measured against your real traffic, and
`Advice()` reports which one is winning and by how much. Nothing switches, so
there is nothing to roll back — see [advisor mode](advisor-mode.md).

## AdaptiveCache API

All methods are safe for concurrent use.

| Method | Description |
| --- | --- |
| `Add(key, value) bool` | Add or update a key; returns true if an eviction occurred |
| `Get(key) (V, bool)` | Retrieve a value; records a hit or miss |
| `Contains(key) bool` | Check presence without recording a hit |
| `Peek(key) (V, bool)` | Read a value without recording a hit |
| `Remove(key) bool` | Delete a key from all policies |
| `Purge()` | Clear all policies and reset migration state |
| `Keys() []K` | Keys in the active policy |
| `Values() []V` | Values in the active policy |
| `Len() int` | Number of entries in the active policy |
| `Resize(size) int` | Resize all policies; returns the total eviction count |
| `Stats() GlobalStats` | Cumulative hit/miss counts served by the cache |
| `Advice() Advice` | Which policy is winning, and by how much |
| `ActivePolicy() PolicyType` | Which policy is currently serving requests |
| `Close() error` | Stop the background epoch goroutine |

The method set through `Close` is deliberately the same shape as
`hashicorp/golang-lru/v2`, so an `AdaptiveCache` drops into code already
written against that, and an existing cache is usually already a valid arm. The
full interface definitions are in [design](design.md#implementing-the-bandit-interface).

## Prior art

- [Cache replacement policies — Wikipedia](https://en.wikipedia.org/wiki/Cache_replacement_policies)
- [hashicorp/golang-lru](https://github.com/hashicorp/golang-lru) — the LRU, 2Q and ARC implementations behind three of the arms
- [maypok86/otter](https://github.com/maypok86/otter) — the W-TinyLFU arm
- [scalalang2/golang-fifo](https://github.com/scalalang2/golang-fifo) — the S3-FIFO and SIEVE arms
- [dgraph-io/ristretto](https://github.com/dgraph-io/ristretto) — an early influence on the idea of measuring admission rather than assuming it
