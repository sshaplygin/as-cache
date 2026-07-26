package bandit

import (
	"fmt"
	"hash/fnv"
	"strings"

	ascache "github.com/sshaplygin/as-cache"
)

// regime is the identity of what a cache is measuring: which arms, at what
// capacity, over how much of the keyspace.
//
// Two replicas may only pool evidence when these match. A hit rate is a
// statement about a specific cache size against a specific substream, and
// summing one taken at 1000 entries with one taken at 100 produces a number
// that describes neither cache - it just looks like a hit rate. Nothing in the
// counts themselves reveals the mismatch, and the resulting decision looks
// entirely reasonable, so this is checked structurally rather than left to be
// noticed.
//
// Epoch duration is deliberately not part of it. A replica reporting twice as
// often contributes twice the counts, but at the same rate, and rates are what
// the comparison is made on. Fleets may be tuned per replica.
type regime struct {
	arms       []ascache.PolicyType
	capacity   int
	sampleRate float64
}

// String renders the regime in the form that gets hashed. It is legible on
// purpose: when a fleet mysteriously splits into two namespaces, this is the
// string worth logging.
func (r regime) String() string {
	names := make([]string, 0, len(r.arms))
	for _, arm := range r.arms {
		names = append(names, arm.String())
	}

	// The rate is quantised before it is rendered. It arrives as a float that
	// has been through a capacity-floor calculation, so two replicas
	// configured identically can differ in the last bits and would otherwise
	// fingerprint apart for no reason a caller could ever see.
	return fmt.Sprintf("arms=%s;cap=%d;rate=%.4f",
		strings.Join(names, ","), r.capacity, r.sampleRate)
}

// fingerprint is the short form appended to the namespace.
func (r regime) fingerprint() string {
	h := fnv.New64a()
	// Hash.Write never returns an error, per the hash.Hash contract.
	_, _ = h.Write([]byte(r.String()))

	return fmt.Sprintf("%012x", h.Sum64()&0xffffffffffff)
}

// regimeOf reads the measurement regime out of an epoch report. The arms are
// already sorted by PolicyType - EpochReport documents that ordering - so the
// fingerprint does not depend on anything the cache could vary between runs.
func regimeOf(report ascache.EpochReport) regime {
	arms := make([]ascache.PolicyType, 0, len(report.Stats))
	for _, stats := range report.Stats {
		arms = append(arms, stats.Policy)
	}

	return regime{
		arms:       arms,
		capacity:   report.Capacity,
		sampleRate: report.SampleRate,
	}
}

// equal reports whether two regimes may pool. Sample rates are compared at the
// precision they are fingerprinted at, so the two can never disagree.
func (r regime) equal(other regime) bool {
	return r.String() == other.String()
}

// scopedNamespace is the namespace a replica in this regime coordinates under.
func scopedNamespace(namespace string, r regime) string {
	return namespace + ":" + r.fingerprint()
}
