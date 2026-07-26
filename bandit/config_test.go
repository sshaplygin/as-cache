package bandit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ascache "github.com/sshaplygin/as-cache"
)

func validConfig() Config {
	return Config{
		Store:             NewMemStore(),
		Namespace:         "test",
		CoordinationEpoch: time.Second,
	}
}

func TestConfig_RejectsWhatCannotBeDefaulted(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr error
	}{
		{
			name:    "no store",
			mutate:  func(c *Config) { c.Store = nil },
			wantErr: ErrNilStore,
		},
		{
			name:    "no namespace",
			mutate:  func(c *Config) { c.Namespace = "" },
			wantErr: ErrEmptyNamespace,
		},
		{
			name:    "zero coordination epoch",
			mutate:  func(c *Config) { c.CoordinationEpoch = 0 },
			wantErr: ErrInvalidCoordinationEpoch,
		},
		{
			name:    "negative coordination epoch",
			mutate:  func(c *Config) { c.CoordinationEpoch = -time.Second },
			wantErr: ErrInvalidCoordinationEpoch,
		},
		{
			name:    "negative window",
			mutate:  func(c *Config) { c.Window = -1 },
			wantErr: ErrInvalidWindow,
		},
		{
			name:    "decay above one",
			mutate:  func(c *Config) { c.Decay = 1.5 },
			wantErr: ErrInvalidDecay,
		},
		{
			name:    "decay below zero",
			mutate:  func(c *Config) { c.Decay = -0.1 },
			wantErr: ErrInvalidDecay,
		},
		{
			name:    "jitter of half an epoch",
			mutate:  func(c *Config) { c.Jitter = 0.5 },
			wantErr: ErrInvalidJitter,
		},
		{
			name:    "negative jitter",
			mutate:  func(c *Config) { c.Jitter = -0.1 },
			wantErr: ErrInvalidJitter,
		},
		{
			name: "shadow-only evidence under leader election",
			mutate: func(c *Config) {
				c.Mode = ModeLeader
				c.Evidence = EvidenceShadowOnly
			},
			wantErr: ErrShadowOnlyUnderLeader,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			_, err := NewDistributed(cfg)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestConfig_ShadowOnlyIsAllowedUnderSharedPosterior(t *testing.T) {
	cfg := validConfig()
	cfg.Mode = ModeSharedPosterior
	cfg.Evidence = EvidenceShadowOnly

	bandit, err := NewDistributed(cfg)
	require.NoError(t, err)
	assert.NoError(t, bandit.Close())
}

func TestConfig_ZeroModeLeadsRatherThanDoingNothing(t *testing.T) {
	// Mode and EvidenceMode number from one, so their zero value names no mode
	// at all. Left unresolved it matches neither branch: the replica claims no
	// leadership and follows no leader, so the fleet syncs forever and never
	// decides anything - a configuration that looks healthy and selects
	// nothing.
	cfg := validConfig()
	require.Zero(t, uint8(cfg.Mode))
	require.NoError(t, cfg.validate())

	assert.Equal(t, ModeLeader, cfg.Mode)
	assert.Equal(t, EvidenceAll, cfg.Evidence)
}

func TestConfig_ZeroModeActuallyClaimsLeadership(t *testing.T) {
	store, clock := newFleetStore(t)

	subject := fleet(t, 1, store, clock, func(cfg *Config) {
		cfg.Mode = 0
	})[0]

	subject.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.2, ascache.TinyLFU: 0.9})
	subject.bandit.sync()
	clock.advance(testEpoch)
	subject.report(t, 100, map[ascache.PolicyType]float64{ascache.LRU: 0.2, ascache.TinyLFU: 0.9})
	subject.bandit.sync()

	snapshot := subject.bandit.Snapshot()
	assert.Positive(t, snapshot.Leaderships)
	assert.Equal(t, ascache.TinyLFU.String(), snapshot.Selection)
}

func TestConfig_FillsInDefaults(t *testing.T) {
	cfg := validConfig()
	require.NoError(t, cfg.validate())

	assert.Equal(t, DefaultWindow, cfg.Window)
	assert.InDelta(t, DefaultDecay, cfg.Decay, 1e-9)
	assert.InDelta(t, DefaultJitter, cfg.Jitter, 1e-9)
	assert.InDelta(t, DefaultLocalDiscount, cfg.LocalDiscount, 1e-9)
	assert.InDelta(t, DefaultMaxEvidence, cfg.MaxEvidence, 1e-9)
	assert.Equal(t, 3*time.Second, cfg.FallbackAfter)
	assert.Equal(t, time.Second, cfg.SyncTimeout)
	assert.NotEmpty(t, cfg.NodeID)
	assert.NotZero(t, cfg.Seed)
	assert.NotNil(t, cfg.Now)
}

func TestConfig_UnseededReplicasDoNotShareJitter(t *testing.T) {
	// Seeding a fleet identically would synchronise the very jitter that
	// exists to keep it from arriving at the store in lockstep.
	seeds := make(map[uint64]struct{})
	for range 20 {
		cfg := validConfig()
		require.NoError(t, cfg.validate())
		seeds[cfg.Seed] = struct{}{}
	}

	assert.Greater(t, len(seeds), 15)
}

func TestConfig_ExplicitValuesSurviveValidation(t *testing.T) {
	cfg := validConfig()
	cfg.Window = 3
	cfg.Decay = 1
	cfg.Jitter = 0.25
	cfg.MaxEvidence = -1
	cfg.NodeID = "chosen"
	cfg.FallbackAfter = time.Minute

	require.NoError(t, cfg.validate())

	assert.Equal(t, 3, cfg.Window)
	assert.InDelta(t, 1.0, cfg.Decay, 1e-9)
	assert.InDelta(t, 0.25, cfg.Jitter, 1e-9)
	assert.InDelta(t, -1.0, cfg.MaxEvidence, 1e-9, "a negative cap disables it and must not be defaulted away")
	assert.Equal(t, "chosen", cfg.NodeID)
	assert.Equal(t, time.Minute, cfg.FallbackAfter)
}

func TestConfig_DerivedTTLsOutliveTheirWindow(t *testing.T) {
	cfg := validConfig()
	cfg.Window = 10
	require.NoError(t, cfg.validate())

	// A counter TTL shorter than the window the leader reads back would leave
	// holes in it, and the fleet would silently decide on less evidence than
	// it was configured for.
	assert.Greater(t, cfg.counterTTL(), time.Duration(cfg.Window)*cfg.CoordinationEpoch)
	assert.Positive(t, cfg.leaderTTL())
	assert.Greater(t, cfg.decisionTTL(), cfg.CoordinationEpoch,
		"a decision must outlive its bucket so a replica that ticks late still finds it")
}
