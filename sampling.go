package ascache

import (
	"hash/maphash"
	"math"
)

// maxUint64AsFloat is 2^64 as a float64. Converting a float64 that is greater
// than or equal to it back to uint64 is undefined in Go, so rates at or above 1
// are handled separately rather than scaled.
const maxUint64AsFloat = float64(1 << 64)

// keySampler decides whether a key belongs to the deterministic subset of the
// keyspace that shadow policies track. Sampling lets a shadow estimate its hit
// rate from a small fraction of traffic instead of mirroring every operation.
//
// The decision is a pure function of the key and the seed, so a given key is
// either always sampled or never sampled for the lifetime of the sampler. That
// matters twice over: a sampled shadow sees a coherent access pattern for the
// keys it does track (rather than a random scatter that would destroy any
// notion of reuse), and every shadow sharing one sampler measures the same
// sub-workload, which is what makes their hit rates comparable to each other.
//
// The seed is drawn per cache rather than fixed, so the sampled subset differs
// between processes and cannot be predicted or targeted by a caller.
//
// Sampled counts are never scaled back up to full-traffic magnitude before
// reaching the bandit. Scaling would restore magnitude while inventing
// confidence, handing a Beta posterior twenty times the evidence that was
// actually collected. Instead every arm, the active policy included, is
// measured over this same sampled substream, so the arms carry equal and
// honest evidence and remain directly comparable.
type keySampler[K comparable] struct {
	seed maphash.Seed
	// threshold is the exclusive upper bound on a key's hash for it to be in
	// the sample. It is only meaningful when sampling is true.
	threshold uint64
	// sampling reports whether any filtering happens at all. It is false when
	// the rate is 1 (or above), in which case every key is in the sample.
	sampling bool
	rate     float64
}

// newKeySampler returns a sampler admitting approximately rate of the keyspace.
// A rate of 1 or above admits every key and performs no hashing.
func newKeySampler[K comparable](rate float64) *keySampler[K] {
	s := &keySampler[K]{
		seed: maphash.MakeSeed(),
		rate: rate,
	}

	if rate >= 1 {
		s.rate = 1
		return s
	}

	s.sampling = true
	s.threshold = uint64(math.Max(0, rate) * maxUint64AsFloat)

	return s
}

// sampled reports whether key is part of the tracked sample.
func (s *keySampler[K]) sampled(key K) bool {
	if !s.sampling {
		return true
	}

	return maphash.Comparable(s.seed, key) < s.threshold
}

// scaledCapacity returns the miniature capacity corresponding to sampling rate
// of a cache of the given size, holding the identity capacity/size == rate.
//
// Unlike shadowCapacity it applies no floor. The floor exists to stop a cache
// from being built with a miniature too small to measure, and it works by
// raising the sample rate to match. After construction the rate can no longer
// move, so applying the floor alone would leave shadows running at a capacity
// larger than their share of the traffic - and a shadow of capacity C fed an
// r-sampled stream simulates a cache of C/r. Every shadow would then simulate a
// larger cache than the active policy actually is and report a better hit rate
// for that reason alone, which is a systematic bias against whichever policy is
// active. A miniature that is merely small is noisy; one that is inconsistent
// with its rate is wrong, so the identity wins.
func scaledCapacity(size int, rate float64) int {
	if size <= 0 || rate >= 1 {
		return size
	}

	capacity := int(math.Ceil(rate * float64(size)))
	if capacity < 1 {
		capacity = 1
	}
	if capacity > size {
		capacity = size
	}

	return capacity
}

// shadowCapacity returns the capacity a shadow policy should run at to
// simulate a full-size cache of nominalCap over the sampled substream, and the
// effective rate that capacity corresponds to.
//
// A cache of capacity rate*N fed an rate-sampled stream approximates a cache
// of capacity N fed the full stream, so the capacity has to shrink with the
// rate for the estimate to mean anything. A floor guards the degenerate end:
// a five-entry miniature measures noise, so when rate*nominalCap falls below
// minCapacity the rate itself is raised (not just the capacity) to keep the
// simulation identity intact, up to the point where sampling disables itself.
func shadowCapacity(nominalCap int, rate float64, minCapacity int) (capacity int, effectiveRate float64) {
	if nominalCap <= 0 || rate >= 1 {
		return nominalCap, 1
	}

	capacity = int(math.Ceil(rate * float64(nominalCap)))
	effectiveRate = rate

	if capacity < minCapacity {
		capacity = minCapacity
		effectiveRate = float64(minCapacity) / float64(nominalCap)
	}

	if capacity >= nominalCap {
		return nominalCap, 1
	}

	return capacity, effectiveRate
}
