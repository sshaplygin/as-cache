// Package metrics exposes an AdaptiveCache's measurements to monitoring.
//
// A cache that changes its own eviction policy is only safe to run if you can
// see what it is doing. This package turns the cache's own accounting into a
// snapshot suitable for scraping, and publishes it through expvar.
//
// It depends on nothing outside the standard library. A Prometheus collector
// is a few lines on top of Snapshot; see the package example rather than a
// dependency here, because how metrics are named and labelled is a decision
// that belongs to the application, not to a cache library.
package metrics

import (
	"encoding/json"
	"expvar"
	"fmt"
	"sort"
	"sync"

	ascache "github.com/sshaplygin/as-cache"
)

// publishMu serialises Publish so its check and its registration cannot be
// interleaved with another Publish from this package.
var publishMu sync.Mutex

// Advisor is the part of an AdaptiveCache this package reads. Every
// AdaptiveCache satisfies it, whatever its key and value types, which is why
// this is an interface rather than a generic parameter.
type Advisor interface {
	Advice() ascache.Advice
	Stats() ascache.GlobalStats
	ActivePolicy() ascache.PolicyType
	Len() int
}

// PolicySnapshot is one policy's measurements at a point in time.
type PolicySnapshot struct {
	Policy  string  `json:"policy"`
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	HitRate float64 `json:"hit_rate"`
	Active  bool    `json:"active"`
}

// Snapshot is everything worth exporting about a cache at a point in time.
//
// The fields are chosen so that the two questions an operator actually asks
// are answerable from a dashboard: is the cache working (Served, HitRate,
// Entries), and is it about to do something surprising (ActivePolicy, Epochs,
// BestPolicy, Improvement).
type Snapshot struct {
	// ActivePolicy is the policy serving requests. Graph it over time: this is
	// the series that shows switching behaviour.
	ActivePolicy string `json:"active_policy"`
	// Epochs is how many measurement rounds have completed.
	Epochs int64 `json:"epochs"`
	// Entries is how many entries the active policy currently holds.
	Entries int `json:"entries"`

	// Hits, Misses and HitRate describe the traffic the cache actually served,
	// unsampled.
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	HitRate float64 `json:"hit_rate"`

	// BestPolicy is the policy with the best measured hit rate, and
	// Improvement is how many points it beats the active one by. When
	// Improvement stays high, the cache is leaving hit rate on the table -
	// either because switching is gated off, or because it is in observe-only
	// mode and waiting for a human.
	BestPolicy  string  `json:"best_policy"`
	Improvement float64 `json:"improvement"`

	// Sampled reports whether the per-policy numbers are estimates from a
	// sampled substream. Hits, Misses and HitRate above are never sampled.
	Sampled    bool    `json:"sampled"`
	SampleRate float64 `json:"sample_rate"`

	// Policies holds every arm, best hit rate first.
	Policies []PolicySnapshot `json:"policies"`
}

// Take reads a cache's current measurements.
func Take(cache Advisor) Snapshot {
	advice := cache.Advice()
	stats := cache.Stats()

	snapshot := Snapshot{
		ActivePolicy: advice.Active.String(),
		Epochs:       advice.Epochs,
		Entries:      cache.Len(),
		Hits:         stats.Hits,
		Misses:       stats.Misses,
		BestPolicy:   advice.Best.String(),
		Improvement:  advice.Improvement,
		Sampled:      advice.Sampled,
		SampleRate:   advice.SampleRate,
		Policies:     make([]PolicySnapshot, 0, len(advice.Reports)),
	}

	if total := stats.Hits + stats.Misses; total > 0 {
		snapshot.HitRate = float64(stats.Hits) / float64(total)
	}

	for _, report := range advice.Reports {
		snapshot.Policies = append(snapshot.Policies, PolicySnapshot{
			Policy:  report.Policy.String(),
			Hits:    report.Hits,
			Misses:  report.Misses,
			HitRate: report.HitRate(),
			Active:  report.Active,
		})
	}

	sort.SliceStable(snapshot.Policies, func(i, j int) bool {
		return snapshot.Policies[i].HitRate > snapshot.Policies[j].HitRate
	})

	return snapshot
}

// String renders a snapshot as JSON, which is what expvar serves.
func (s Snapshot) String() string {
	encoded, err := json.Marshal(s)
	if err != nil {
		// Snapshot contains only plain scalars and slices of the same, so
		// this cannot fail; report rather than panic if it somehow does.
		return fmt.Sprintf("{%q:%q}", "error", err.Error())
	}

	return string(encoded)
}

// Publish exposes a cache's snapshot under the given name in expvar, so it
// appears in the /debug/vars handler alongside the rest of a process's
// published state.
//
// The value is computed when scraped rather than on a timer, so publishing
// costs nothing until something reads it.
//
// expvar panics if a name is published twice, which would take down a process
// over a metrics-registration mistake, so this reports that as an error
// instead.
//
// Checking with expvar.Get before publishing is not enough on its own: the two
// calls are separate, so a concurrent publisher can register the name in
// between and the panic happens anyway. A mutex closes that window for callers
// of this function, and the recover closes it for a name registered directly
// through expvar by something else - which this package cannot synchronise
// with, and which is exactly the mistake a shared metric name produces.
func Publish(name string, cache Advisor) (err error) {
	publishMu.Lock()
	defer publishMu.Unlock()

	if expvar.Get(name) != nil {
		return fmt.Errorf("metrics: %q is already published", name)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("metrics: publishing %q: %v", name, recovered)
		}
	}()

	expvar.Publish(name, expvar.Func(func() any {
		return Take(cache)
	}))

	return nil
}
