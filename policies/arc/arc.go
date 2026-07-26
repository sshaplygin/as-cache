// Package arc adapts the Adaptive Replacement Cache to ascache.Policy.
//
// It is a module of its own, separate from the other policy adapters, for one
// reason: ARC is patented by IBM (US 6,996,676, filed 2002). Upstream
// hashicorp/golang-lru made the same split in v2 so that its main module
// carries no patented algorithm, and this package preserves that property for
// as-cache. Importing github.com/sshaplygin/as-cache/policies never pulls ARC
// into a build; only importing this package does.
//
// Whether the patent still restricts anything is a question for the adopter
// and their counsel, not for this comment. The isolation exists so that the
// choice is explicit and never made by accident.
package arc

import (
	"fmt"

	arclru "github.com/hashicorp/golang-lru/arc/v2"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/policies"
)

// New returns an ARC cache holding up to size entries, satisfying
// ascache.Cacher.
//
// Upstream ARCCache reports neither evictions nor removals and has no Resize,
// so it is wrapped by policies.Adapt, which supplies all three. See
// policies.AdaptedCache.Resize for what resizing costs an adaptive algorithm.
func New[K comparable, V any](size int) (*policies.AdaptedCache[K, V], error) {
	return policies.Adapt[K, V](size, func(size int) (policies.PartialCacher[K, V], error) {
		cache, err := arclru.NewARC[K, V](size)
		if err != nil {
			return nil, fmt.Errorf("build arc cache: %w", err)
		}

		return cache, nil
	})
}

// NewPolicy returns an ARC policy of the given size, ready to be used as a
// bandit arm.
func NewPolicy[K comparable, V any](size int) (ascache.Policy[K, V], error) {
	cache, err := New[K, V](size)
	if err != nil {
		return nil, err
	}

	return ascache.NewCache[K, V](cache, ascache.ARC, size), nil
}
