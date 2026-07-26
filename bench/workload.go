// Package bench generates cache workloads and replays them against policies,
// so claims about which policy wins where can be checked rather than asserted.
//
// Every generator is deterministic given its seed: a run is reproducible, and
// two policies are always compared on the identical request sequence.
package bench

import (
	"math/rand/v2"
	"strconv"
)

// Workload is a named request sequence to replay against a cache.
type Workload struct {
	// Name identifies the workload in reports.
	Name string
	// Keys is the request sequence, in order.
	Keys []string
	// Description says what the workload is meant to represent and which
	// policies it is expected to favour, so a surprising result is
	// recognisable as surprising.
	Description string
}

// Len returns the number of requests.
func (w Workload) Len() int { return len(w.Keys) }

// key renders a key id. Keys are strings because that is what a real cache
// usually holds, and it keeps hashing costs realistic.
func key(id int) string {
	return "k" + strconv.Itoa(id)
}

// newRNG returns a deterministic source for a given seed. A reproducible
// sequence is the requirement here: two policies must be compared on the
// identical request stream, which a cryptographic source would defeat.
//
//nolint:gosec // deliberate: determinism is the point, see above
func newRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}

// newZipf builds a Zipf source over ids in [0, keyspace). It returns ids as
// ints, bounded by keyspace, so no conversion can wrap.
func newZipf(rng *rand.Rand, s float64, keyspace int) func() int {
	if keyspace < 2 {
		return func() int { return 0 }
	}

	// keyspace is a positive int here, so both conversions are exact in either
	// direction and the drawn id is clamped into range before it is used.
	maxID := uint64(keyspace - 1) //nolint:gosec // keyspace is >= 2 here, so this is exact
	zipf := rand.NewZipf(rng, s, 1, maxID)

	return func() int {
		drawn := min(zipf.Uint64(), maxID)

		return int(drawn) //nolint:gosec // clamped to maxID on the line above
	}
}

// Zipf returns a workload whose key popularity follows a Zipf distribution:
// a small head of very hot keys and a long tail of cold ones. This is the
// shape most real caches see, and it rewards policies that track frequency.
func Zipf(requests, keyspace int, s float64, seed uint64) Workload {
	// v = 1 so the most popular key is id 0; keyspace bounds the support.
	draw := newZipf(newRNG(seed), s, keyspace)

	keys := make([]string, requests)
	for i := range keys {
		keys[i] = key(draw())
	}

	return Workload{
		Name:        "zipf",
		Keys:        keys,
		Description: "skewed popularity; favours frequency-aware policies (LFU, W-TinyLFU)",
	}
}

// Uniform returns a workload with no reuse structure: every key is equally
// likely. Nothing can predict it, so elaborate bookkeeping earns nothing and
// random eviction is competitive. It is the control that shows when a policy
// is paying for information the workload does not contain.
func Uniform(requests, keyspace int, seed uint64) Workload {
	rng := newRNG(seed)

	keys := make([]string, requests)
	for i := range keys {
		keys[i] = key(rng.IntN(keyspace))
	}

	return Workload{
		Name:        "uniform",
		Keys:        keys,
		Description: "no reuse structure; random eviction is competitive",
	}
}

// Loop returns a cyclic scan over a working set slightly larger than the
// cache. It is the classic LRU pathology: every key is evicted exactly before
// it is needed again, so LRU hits nothing at all, while random eviction keeps
// an arbitrary fraction resident and does far better.
func Loop(requests, workingSet int) Workload {
	keys := make([]string, requests)
	for i := range keys {
		keys[i] = key(i % workingSet)
	}

	return Workload{
		Name:        "loop",
		Keys:        keys,
		Description: "cyclic scan just over capacity; pathological for LRU, fine for random",
	}
}

// Scan returns a workload that repeatedly reads a small hot set and then
// sweeps a long run of one-off keys. The sweep is what flushes a naive
// recency cache: policies that admit on frequency keep the hot set, policies
// that admit on recency lose it every sweep.
func Scan(rounds, hotSet, hotReads, scanLen int) Workload {
	keys := make([]string, 0, rounds*(hotReads+scanLen))
	scanID := 1000000

	for r := 0; r < rounds; r++ {
		for i := 0; i < hotReads; i++ {
			keys = append(keys, key(i%hotSet))
		}
		for i := 0; i < scanLen; i++ {
			keys = append(keys, key(scanID))
			scanID++
		}
	}

	return Workload{
		Name:        "scan",
		Keys:        keys,
		Description: "hot set plus repeated one-off sweeps; favours scan-resistant policies (2Q, ARC, W-TinyLFU)",
	}
}

// PhaseShift alternates between two regimes that reward different policies:
// a Zipf phase, where frequency wins, and a loop phase, where frequency is
// exactly the wrong signal because every key is equally and briefly popular.
//
// This is the workload adaptive selection exists for. A fixed policy must be
// mediocre in one phase to be good in the other; a cache that switches can be
// good in both, if the switching actually works and is fast enough to matter.
func PhaseShift(phases, perPhase, keyspace, workingSet int, seed uint64) Workload {
	draw := newZipf(newRNG(seed), 1.1, keyspace)

	keys := make([]string, 0, phases*perPhase)
	for p := 0; p < phases; p++ {
		if p%2 == 0 {
			for i := 0; i < perPhase; i++ {
				keys = append(keys, key(draw()))
			}

			continue
		}
		for i := 0; i < perPhase; i++ {
			keys = append(keys, key(i%workingSet))
		}
	}

	return Workload{
		Name:        "phase-shift",
		Keys:        keys,
		Description: "alternating zipf and loop phases; no fixed policy is good in both",
	}
}

// PhaseBoundaries returns the request indices at which PhaseShift changes
// regime, for plotting which policy was active when.
func PhaseBoundaries(phases, perPhase int) []int {
	bounds := make([]int, 0, phases)
	for p := 1; p < phases; p++ {
		bounds = append(bounds, p*perPhase)
	}

	return bounds
}
