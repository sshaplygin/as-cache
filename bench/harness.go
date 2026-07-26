package bench

import (
	"fmt"
	"sort"
	"strings"
	"time"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/policies"
	"github.com/sshaplygin/as-cache/policies/arc"
	"github.com/sshaplygin/as-cache/policies/tinylfu"
)

// Result is one policy's performance on one workload.
type Result struct {
	Policy   string
	Hits     int64
	Misses   int64
	Duration time.Duration
}

// HitRate returns the fraction of requests served from cache.
func (r Result) HitRate() float64 {
	total := r.Hits + r.Misses
	if total == 0 {
		return 0
	}

	return float64(r.Hits) / float64(total)
}

// NsPerOp returns the average time per request.
func (r Result) NsPerOp() float64 {
	total := r.Hits + r.Misses
	if total == 0 {
		return 0
	}

	return float64(r.Duration.Nanoseconds()) / float64(total)
}

// Cache is the subset of cache behaviour the harness replays against, so a
// bare policy and a full AdaptiveCache can be measured the same way.
type Cache interface {
	Get(key string) (int, bool)
	Add(key string, value int) bool
}

// Replay runs a workload against a cache, filling on every miss exactly as a
// read-through cache would, and reports what it served.
//
// Hits and misses are counted here rather than read from the cache's own
// statistics so that every subject is measured identically, including ones
// whose internal accounting is sampled.
func Replay(name string, c Cache, w Workload) Result {
	result := Result{Policy: name}

	start := time.Now()
	for i, key := range w.Keys {
		if _, ok := c.Get(key); ok {
			result.Hits++

			continue
		}
		result.Misses++
		c.Add(key, i)
	}
	result.Duration = time.Since(start)

	return result
}

// PolicyBuilder constructs a policy of the given capacity.
type PolicyBuilder struct {
	Name  string
	Build func(size int) (ascache.Policy[string, int], error)
}

// FixedPolicies returns every policy this repository ships, for measuring what
// each achieves on its own.
func FixedPolicies() []PolicyBuilder {
	return []PolicyBuilder{
		{"LRU", policies.NewLRU[string, int]},
		{"LFU", policies.NewLFU[string, int]},
		{"2Q", policies.NewTwoQueue[string, int]},
		{"Random", func(size int) (ascache.Policy[string, int], error) {
			return policies.NewRandomPolicy[string, int](size), nil
		}},
		{"TTL", func(size int) (ascache.Policy[string, int], error) {
			// A TTL longer than any run, so this measures its LRU behaviour
			// rather than expiry: the workloads carry no notion of staleness.
			return policies.NewTTL[string, int](size, time.Hour), nil
		}},
		{"ARC", arc.NewPolicy[string, int]},
		{"W-TinyLFU", tinylfu.NewPolicy[string, int]},
	}
}

// AdaptiveArms builds a fresh set of arms for an AdaptiveCache. Every arm
// needs its own instance per run, since policies carry state.
func AdaptiveArms(size int) ([]ascache.Policy[string, int], error) {
	arms := make([]ascache.Policy[string, int], 0, len(FixedPolicies()))
	for _, builder := range FixedPolicies() {
		policy, err := builder.Build(size)
		if err != nil {
			return nil, fmt.Errorf("build %s arm: %w", builder.Name, err)
		}
		arms = append(arms, policy)
	}

	return arms, nil
}

// Table renders results as a markdown table, best hit rate first.
func Table(results []Result) string {
	sorted := make([]Result, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].HitRate() > sorted[j].HitRate()
	})

	var b strings.Builder
	b.WriteString("| Policy | Hit rate | ns/op |\n| --- | --- | --- |\n")
	for _, r := range sorted {
		fmt.Fprintf(&b, "| %s | %.2f%% | %.0f |\n", r.Policy, r.HitRate()*100, r.NsPerOp())
	}

	return b.String()
}

// Ranking returns policy names ordered best hit rate first.
func Ranking(results []Result) []string {
	sorted := make([]Result, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].HitRate() > sorted[j].HitRate()
	})

	names := make([]string, len(sorted))
	for i, r := range sorted {
		names[i] = r.Policy
	}

	return names
}
