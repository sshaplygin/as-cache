package ascache

import (
	"fmt"
	"sort"
	"strings"
)

// PolicyReport is one policy's measured performance over the cache's lifetime.
type PolicyReport struct {
	// Policy is the arm this report describes.
	Policy PolicyType
	// Hits and Misses are the requests measured for this policy in its current
	// role - as the active policy, or as a shadow - since it last changed
	// role. Under ShadowSampleRate these are counts over the sampled
	// substream, not over all traffic: the rate is meaningful, the magnitude
	// is a sample.
	Hits   int64
	Misses int64
	// Active reports whether this policy was serving requests when the report
	// was taken.
	Active bool
}

// HitRate returns the fraction of measured requests this policy served, or 0
// when it has measured nothing.
func (r PolicyReport) HitRate() float64 {
	total := r.Hits + r.Misses
	if total == 0 {
		return 0
	}

	return float64(r.Hits) / float64(total)
}

// Advice is what the cache has learned about which policy suits the traffic it
// has seen.
//
// It is the answer to a question that is otherwise expensive to ask: not "is
// my cache fast" but "would a different eviction policy serve my traffic
// better, and by how much". Running in ObserveOnly mode makes that answerable
// without ever changing what the cache does.
type Advice struct {
	// Epochs is how many epochs actually measured something and fed this
	// advice. Advice from a handful of epochs is not worth acting on. It
	// counts reporting epochs rather than elapsed ticks, so an epoch the
	// capacity gate skipped is not counted as evidence.
	//
	// It resets to nothing for a policy that changes role, so shortly after a
	// switch the advice is deliberately thin rather than confidently stale.
	Epochs int64
	// Active is the policy serving requests.
	Active PolicyType
	// Best is the policy with the highest measured hit rate.
	Best PolicyType
	// Improvement is how many percentage points Best beats Active by. It is
	// zero when they are the same policy.
	Improvement float64
	// Sampled reports whether the measurements come from a sampled substream,
	// in which case the rates are estimates.
	Sampled bool
	// SampleRate is the fraction of the keyspace measured, 1 when sampling is
	// off.
	SampleRate float64
	// Reports holds every policy, best hit rate first.
	Reports []PolicyReport
}

// String renders the advice as a short human-readable summary.
func (a Advice) String() string {
	if len(a.Reports) == 0 {
		return "no measurements yet"
	}

	var b strings.Builder

	if a.Best == a.Active || a.Improvement <= 0 {
		fmt.Fprintf(&b, "%s is the best of the %d policies measured over %d epochs.\n",
			a.Active, len(a.Reports), a.Epochs)
	} else {
		fmt.Fprintf(&b, "On this traffic %s beats %s by %.2f points of hit rate, over %d epochs.\n",
			a.Best, a.Active, a.Improvement*100, a.Epochs)
	}

	if a.Sampled {
		fmt.Fprintf(&b, "Rates are estimated from %.1f%% of the keyspace.\n", a.SampleRate*100)
	}

	fmt.Fprintf(&b, "\n%-10s %9s %12s %12s\n", "policy", "hit rate", "hits", "misses")
	for _, r := range a.Reports {
		marker := " "
		if r.Active {
			marker = "*"
		}
		fmt.Fprintf(&b, "%s%-9s %8.2f%% %12d %12d\n",
			marker, r.Policy, r.HitRate()*100, r.Hits, r.Misses)
	}
	b.WriteString("\n* currently active\n")

	return b.String()
}

// observerBandit stands in when a cache is built for observation alone. It
// records nothing and selects nothing, because in ObserveOnly mode its
// selection would be discarded anyway.
type observerBandit struct{}

func (observerBandit) RecordStats(_ ShadowStats) {}
func (observerBandit) SelectPolicy() PolicyType  { return Undefined }

// Advice reports which policy has served this cache's traffic best.
//
// It is safe to call at any time and does not disturb measurement. The advice
// is only as good as the traffic behind it: a cache that has run for a few
// epochs, or one whose policies are within noise of each other, has nothing
// useful to say, and Epochs is included so a caller can tell.
func (c *AdaptiveCache[K, V]) Advice() Advice {
	c.mu.RLock()
	defer c.mu.RUnlock()

	advice := Advice{
		Epochs:     c.reportingEpochs,
		Active:     c.activePolicy,
		Best:       c.activePolicy,
		Sampled:    c.sampler.sampling,
		SampleRate: c.sampler.rate,
		Reports:    make([]PolicyReport, 0, len(c.tenureStats)),
	}

	for policyType, stats := range c.tenureStats {
		advice.Reports = append(advice.Reports, PolicyReport{
			Policy: policyType,
			Hits:   stats.Hits,
			Misses: stats.Misses,
			Active: policyType == c.activePolicy,
		})
	}

	// Ties are broken by policy so the answer is stable. Ranging over a map
	// gives a random order, and a stable sort preserves it among equal rates,
	// so without this a cache whose arms are performing identically would name
	// a different "best" policy on every call.
	sort.SliceStable(advice.Reports, func(i, j int) bool {
		left, right := advice.Reports[i], advice.Reports[j]
		if left.HitRate() != right.HitRate() {
			return left.HitRate() > right.HitRate()
		}

		return left.Policy < right.Policy
	})

	if len(advice.Reports) == 0 {
		return advice
	}

	best := advice.Reports[0]
	advice.Best = best.Policy

	for _, r := range advice.Reports {
		if r.Active {
			advice.Improvement = best.HitRate() - r.HitRate()

			break
		}
	}

	return advice
}
