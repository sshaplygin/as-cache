// Package redis backs a distributed bandit with Valkey or Redis.
//
// It implements github.com/sshaplygin/as-cache/bandit.Store over
// github.com/redis/go-redis/v9, which speaks to both.
//
//	client := goredis.NewClient(&goredis.Options{Addr: "localhost:6379"})
//	store, err := redisstore.New(redisstore.Options{Client: client})
//	if err != nil {
//	    return err
//	}
//	defer store.Close()
//
//	b, err := bandit.NewDistributed(bandit.Config{
//	    Store:             store,
//	    Namespace:         "sessions",
//	    CoordinationEpoch: time.Second,
//	})
//
// # What it stores
//
// Per-policy hit and miss integers, a leader's node identifier, and a policy
// name. No cache keys and no cache values ever leave the process, so the store
// holds nothing that needs protecting beyond the usual.
//
// Everything written carries a TTL, sized from the bandit's window. A fleet
// that stops running leaves nothing behind.
//
// # Load
//
// One round trip per replica per coordination epoch, plus two more for the
// replica leading that epoch. At a one-second epoch a thousand replicas
// produce about a thousand small pipelined calls a second against a single
// key slot, which is not much - but it is the number to check before running a
// coordination epoch faster than a second.
//
// # Requirements
//
// Redis 7.0 or Valkey 7.2 and above. Buckets are derived from the server's
// clock inside a Lua script, which needs effects replication - the default
// since Redis 7 - and which is the point: no replica's clock is ever
// consulted, so a fleet needs no clock agreement at all.
//
// # Redis Cluster
//
// Every key for one namespace shares a hash tag, so a namespace lives in a
// single slot and the scripts and pipelines here work unchanged on a cluster.
package redis
