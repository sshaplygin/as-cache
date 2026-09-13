package ascache

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeLimitedGradualCache builds a two-policy cache under MigrationGradual with
// the given window cap, holding pending real values k1..k5 (stored as 1..5, so
// a zero read back is unambiguously a shadow placeholder), and opens a window
// by switching from LRU to LFU.
func makeLimitedGradualCache(t *testing.T, maxRequests int64) *AdaptiveCache[string, int] {
	t.Helper()

	ac, err := NewAdaptiveCache(
		[]Policy[string, int]{
			newMockPolicy[string, int](LRU, 10),
			newMockPolicy[string, int](LFU, 10),
		},
		&mockBandit{next: LRU},
		&Settings{
			EpochDuration:               24 * time.Hour,
			EvictPartialCapacityFilling: true,
			MigrationStrategy:           MigrationGradual,
			MigrationMaxRequests:        maxRequests,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })

	for i := 1; i <= 5; i++ {
		ac.Add("k"+strconv.Itoa(i), i)
	}
	triggerSwitch(ac, LFU)
	require.True(t, windowOpen(ac), "switching away from a non-empty policy must open a window")

	return ac
}

func windowOpen(ac *AdaptiveCache[string, int]) bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	return ac.migrating
}

// TestMigrationGradual_MaxRequestsClosesWindow pins the cap. Gets for keys
// nobody stored promote nothing, so without a cap the window would stay open
// until the next epoch and every one of those Gets would take the write lock.
// With a cap of 3 the third Get closes it, and Gets go back to the read-lock
// path.
func TestMigrationGradual_MaxRequestsClosesWindow(t *testing.T) {
	ac := makeLimitedGradualCache(t, 3)

	ac.Get("absent-1")
	ac.Get("absent-2")
	require.True(t, windowOpen(ac), "the window must stay open below the cap")

	ac.Get("absent-3")
	assert.False(t, windowOpen(ac), "the Get reaching the cap must close the window")

	ac.mu.RLock()
	source := ac.migrateFrom
	ac.mu.RUnlock()
	assert.Equal(t, Undefined, source, "a closed window leaves no migration source")
}

// TestMigrationGradual_MaxRequestsAbandonsPendingKeys pins what the cap costs.
// Closing the window demotes the source, rewriting it to zero values, so a key
// that was never promoted is gone. The one thing that must not happen is the
// central invariant breaking on the way out: a Get for an abandoned key is a
// miss, never a zero returned as a hit.
func TestMigrationGradual_MaxRequestsAbandonsPendingKeys(t *testing.T) {
	ac := makeLimitedGradualCache(t, 2)

	ac.Get("absent-1")
	ac.Get("absent-2")
	require.False(t, windowOpen(ac), "the cap of 2 is reached")

	for i := 1; i <= 5; i++ {
		key := "k" + strconv.Itoa(i)
		value, found := ac.Get(key)
		if found {
			assert.NotZero(t, value, "Get(%q) returned a shadow zero as a hit", key)
		}
		assert.False(t, found, "Get(%q): a key abandoned by the capped window must be a miss", key)
	}
}

// TestMigrationGradual_ZeroMaxRequestsSetsNoCap pins backward compatibility:
// the zero value behaves exactly as the cache did before the setting existed,
// leaving the window open however many Gets arrive, until something else
// closes it.
func TestMigrationGradual_ZeroMaxRequestsSetsNoCap(t *testing.T) {
	ac := makeLimitedGradualCache(t, 0)

	for i := range 1000 {
		ac.Get("absent-" + strconv.Itoa(i))
	}

	assert.True(t, windowOpen(ac), "with no cap the window stays open until the epoch ends it")
}

func TestNewAdaptiveCache_RejectsNegativeMigrationMaxRequests(t *testing.T) {
	_, err := NewAdaptiveCache(
		[]Policy[string, int]{newMockPolicy[string, int](LRU, 10)},
		&mockBandit{next: LRU},
		&Settings{EpochDuration: time.Hour, MigrationMaxRequests: -1},
	)

	assert.True(t, errors.Is(err, ErrInvalidMigrationMaxRequests),
		"a negative cap has no meaning and must be rejected, got %v", err)
}
