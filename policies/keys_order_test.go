package policies_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sshaplygin/as-cache/policies"
)

// These pin the order in which upstream hashicorp/golang-lru returns Keys().
// Nothing in golang-lru promises it, and code here depends on it:
//
//   - demoteLocked rewrites a demoted policy to zero values by walking Keys().
//     For an LRU that walk re-establishes the same recency order only because
//     Keys() runs oldest to newest; were it newest first, every demotion would
//     invert the order the policy had learned.
//   - docs/policies.md states that 2Q and ARC return opposite groupings, which
//     is why AdaptedCache.Resize replays every entry rather than keeping a head
//     or a tail. If upstream changes either, that page is wrong.
//
// Each test asserts an exact sequence after one access that moves an entry, so
// a changed order fails rather than passing by coincidence on insertion order.
// ARC lives in its own module; its canary is policies/arc's
// TestKeysOrder_ARCIsRecentThenFrequent.

// TestKeysOrder_LRUIsOldestToNewest pins the order demotion relies on.
func TestKeysOrder_LRUIsOldestToNewest(t *testing.T) {
	p, err := policies.NewLRU[string, int](8)
	require.NoError(t, err)

	p.Add("a", 1)
	p.Add("b", 2)
	p.Add("c", 3)
	require.Equal(t, []string{"a", "b", "c"}, p.Keys(), "insertion order, oldest first")

	_, ok := p.Get("a")
	require.True(t, ok)

	assert.Equal(t, []string{"b", "c", "a"}, p.Keys(),
		"an access moves the key to the newest end; demoteLocked's recency rewrite depends on this")
}

// TestKeysOrder_TwoQueueIsFrequentThenRecent pins 2Q's grouping: its frequent
// list first, then its recent list, each oldest first. It is not a recency
// order -- the key accessed last comes out first.
func TestKeysOrder_TwoQueueIsFrequentThenRecent(t *testing.T) {
	p, err := policies.NewTwoQueue[string, int](8)
	require.NoError(t, err)

	p.Add("a", 1)
	p.Add("b", 2)
	p.Add("c", 3)
	require.Equal(t, []string{"a", "b", "c"}, p.Keys(), "all three are in the recent list")

	_, ok := p.Get("b")
	require.True(t, ok)

	assert.Equal(t, []string{"b", "a", "c"}, p.Keys(),
		"a second access promotes b to the frequent list, which 2Q returns first")
}
