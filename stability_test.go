package ascache

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSwitchStability_ZeroTrafficOnActiveBlocksSwitch pins the edge the
// improvement gate used to get wrong. hitRate reports 0 for an arm that saw no
// requests, so an active policy with no traffic in the measured epoch looked
// like one serving nothing, and any candidate with a single hit cleared the
// threshold against it. That is a switch made on no evidence about the policy
// being replaced -- exactly what MinHitRateImprovement exists to prevent.
func TestSwitchStability_ZeroTrafficOnActiveBlocksSwitch(t *testing.T) {
	ac, _, lfu := makeStabilityCache(t, &Settings{MinHitRateImprovement: 0.02})

	primeActiveStats(ac, 0, 0)
	primeStats(lfu, 5, 0)
	ac.runEpoch()

	assert.Equal(t, LRU, ac.ActivePolicy(),
		"an active policy with no traffic cannot be out-performed; there is nothing to compare")
}

// TestSwitchStability_ZeroTrafficOnCandidateBlocksSwitch is the other edge.
// It already held before the fix -- a candidate's empty epoch scores 0 and
// loses the comparison -- and is pinned so the fix for the active edge cannot
// quietly turn a candidate with no evidence into a winner.
func TestSwitchStability_ZeroTrafficOnCandidateBlocksSwitch(t *testing.T) {
	ac, _, lfu := makeStabilityCache(t, &Settings{MinHitRateImprovement: 0.02})

	primeActiveStats(ac, 5, 5)
	primeStats(lfu, 0, 0)
	ac.runEpoch()

	assert.Equal(t, LRU, ac.ActivePolicy(),
		"a candidate with no traffic has shown nothing and must not win")
}
