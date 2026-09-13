package arc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sshaplygin/as-cache/policies/arc"
)

// TestKeysOrder_ARCIsRecentThenFrequent pins ARC's grouping: its recent list
// (T1) first, then its frequent list (T2), each oldest first. That is the
// opposite grouping to 2Q's, which docs/policies.md relies on to explain why
// an adapted cache's Resize replays every entry instead of keeping a head or a
// tail. The 2Q and LRU canaries are in the policies module, keys_order_test.go.
func TestKeysOrder_ARCIsRecentThenFrequent(t *testing.T) {
	p, err := arc.NewPolicy[string, int](8)
	require.NoError(t, err)

	p.Add("a", 1)
	p.Add("b", 2)
	p.Add("c", 3)
	require.Equal(t, []string{"a", "b", "c"}, p.Keys(), "all three are in the recent list")

	_, ok := p.Get("b")
	require.True(t, ok)

	assert.Equal(t, []string{"a", "c", "b"}, p.Keys(),
		"a second access moves b to the frequent list, which ARC returns last")
}
