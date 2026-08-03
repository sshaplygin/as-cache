package ascache

import "errors"

// ErrEmptyPolicies is returned by NewAdaptiveCache when the policies slice is
// nil or empty.
var ErrEmptyPolicies = errors.New("must provide non zero policies size")

// ErrNilPolicy is returned by NewAdaptiveCache when one of the provided
// policies is nil.
var ErrNilPolicy = errors.New("policy must not be nil")

// ErrDuplicatePolicy is returned by NewAdaptiveCache when two policies report
// the same PolicyType.
var ErrDuplicatePolicy = errors.New("duplicate policy type")

// ErrNilBandit is returned by NewAdaptiveCache when the bandit is nil.
var ErrNilBandit = errors.New("bandit must not be nil")

// ErrNilSettings is returned by NewAdaptiveCache when settings is nil.
var ErrNilSettings = errors.New("settings must not be nil")

// ErrInvalidEpochDuration is returned by NewAdaptiveCache when neither epoch
// clock is set: Settings.EpochDuration is zero or negative and
// Settings.EpochRequests is zero, so nothing would ever end an epoch.
var ErrInvalidEpochDuration = errors.New(
	"epoch duration must be positive, or epoch requests must be set",
)

// ErrInvalidEpochRequests is returned by NewAdaptiveCache when
// Settings.EpochRequests is negative.
var ErrInvalidEpochRequests = errors.New("epoch requests must not be negative")
