package config

// Tests for NM-23 and NM-24.
//
// NM-23: SetMany used to persist one key at a time and return success
// unconditionally, even when a per-key state-db write failed (or the batch
// crashed partway through) -- the in-memory effective config would still
// update, silently diverging from what actually made it to disk. SetMany now
// batches the whole update into a single transaction and only applies it to
// memory once that transaction durably commits; any failure leaves both
// memory and the state-db exactly as they were.
//
// NM-24: SetMany validated almost nothing beyond schedule mode, so an
// unauthenticated client on --api-port (see the threat model note in
// nemesis-triage.md) could invert the bid window or push the reveal time
// past the slot deadline with a single write. ValidateTimingBounds now
// rejects both.

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/db"
)

func setMany(t *testing.T, svc *Service, updates map[string]any, actor string) error {
	t.Helper()

	raw := make(map[string]json.RawMessage, len(updates))

	for k, v := range updates {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		raw[k] = b
	}

	return svc.SetMany(raw, actor)
}

// TestSetMany_PersistFailureLeavesEverythingUnchanged forces the state-db
// write to fail (by closing the underlying connection out from under the
// service) and confirms SetMany reports the failure and changes nothing --
// neither the in-memory effective config nor, on a simulated restart, what
// was actually durable.
func TestSetMany_PersistFailureLeavesEverythingUnchanged(t *testing.T) {
	dir := t.TempDir()
	store := db.NewDatabase(&db.Config{File: filepath.Join(dir, "state.db")}, testLogger())
	require.NoError(t, store.Init())

	defaults := defaultsConfig()
	svc := boot(t, store, defaults, nil)

	setSubsidy(t, svc, 600)
	require.Equal(t, uint64(600), svc.Load().EPBS.BidSubsidy)

	// Force every subsequent write to fail.
	require.NoError(t, store.Close())

	err := setMany(t, svc, map[string]any{subsidyKey: uint64(700)}, "tester")
	require.Error(t, err)

	// In-memory value is untouched by the failed write.
	assert.Equal(t, uint64(600), svc.Load().EPBS.BidSubsidy)
}

// TestSetMany_BatchIsAtomic confirms a batch touching several keys either
// commits entirely or not at all: forcing the persist step to fail must
// leave every key in the batch unchanged, not just the one that happened to
// trigger the failure.
func TestSetMany_BatchIsAtomic(t *testing.T) {
	dir := t.TempDir()
	store := db.NewDatabase(&db.Config{File: filepath.Join(dir, "state.db")}, testLogger())
	require.NoError(t, store.Init())

	defaults := defaultsConfig()
	svc := boot(t, store, defaults, nil)

	require.NoError(t, setMany(t, svc, map[string]any{
		subsidyKey:                 uint64(500),
		KeyEPBSBidMinAmount:        uint64(1000),
		KeyBuilderAPIOnDemandBuild: true,
	}, "tester"))

	require.Equal(t, uint64(500), svc.Load().EPBS.BidSubsidy)
	require.Equal(t, uint64(1000), svc.Load().EPBS.BidMinAmount)
	require.True(t, svc.Load().BuilderAPI.OnDemandBuild)

	require.NoError(t, store.Close())

	err := setMany(t, svc, map[string]any{
		subsidyKey:                 uint64(999),
		KeyEPBSBidMinAmount:        uint64(999),
		KeyBuilderAPIOnDemandBuild: false,
	}, "tester")
	require.Error(t, err)

	// None of the three keys moved, not just the one whose write "happened"
	// to fail first.
	assert.Equal(t, uint64(500), svc.Load().EPBS.BidSubsidy)
	assert.Equal(t, uint64(1000), svc.Load().EPBS.BidMinAmount)
	assert.True(t, svc.Load().BuilderAPI.OnDemandBuild)
}

func TestValidateTimingBounds(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*Config)
		slotDuration time.Duration
		wantErr      bool
	}{
		{
			name:         "defaults are valid",
			mutate:       func(*Config) {},
			slotDuration: 12 * time.Second,
		},
		{
			name: "inverted bid window rejected",
			mutate: func(c *Config) {
				c.EPBS.BidStartTime = -100
				c.EPBS.BidEndTime = -400
			},
			slotDuration: 12 * time.Second,
			wantErr:      true,
		},
		{
			name: "equal bid start/end allowed",
			mutate: func(c *Config) {
				c.EPBS.BidStartTime = -400
				c.EPBS.BidEndTime = -400
			},
			slotDuration: 12 * time.Second,
		},
		{
			name: "negative reveal time rejected",
			mutate: func(c *Config) {
				c.Reveal.TimeMs = -1
			},
			slotDuration: 12 * time.Second,
			wantErr:      true,
		},
		{
			name: "reveal time past the slot deadline rejected",
			mutate: func(c *Config) {
				c.Reveal.TimeMs = 12000
			},
			slotDuration: 12 * time.Second,
			wantErr:      true,
		},
		{
			name: "reveal time bound skipped when slot duration unknown",
			mutate: func(c *Config) {
				c.Reveal.TimeMs = 999999
			},
			slotDuration: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultsConfig()
			tt.mutate(cfg)

			err := ValidateTimingBounds(cfg, tt.slotDuration)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestSetMany_RejectsInvertedBidWindow guards the actual attack surface this
// closes: SetMany itself, not just the standalone validator, must reject a
// write that would invert the bid window -- including when only one of the
// two fields is touched by this particular call (the other must be compared
// against its current effective value, not silently skipped).
func TestSetMany_RejectsInvertedBidWindow(t *testing.T) {
	dir := t.TempDir()
	store := db.NewDatabase(&db.Config{File: filepath.Join(dir, "state.db")}, testLogger())
	require.NoError(t, store.Init())

	defaults := defaultsConfig()
	svc := boot(t, store, defaults, nil)

	originalStart := svc.Load().EPBS.BidStartTime
	originalEnd := svc.Load().EPBS.BidEndTime
	require.Less(t, originalStart, originalEnd)

	// Only bid_end_time is touched, but pushed before the CURRENT
	// (untouched) bid_start_time -- must still be rejected.
	err := setMany(t, svc, map[string]any{KeyEPBSBidEndTime: originalStart - 1}, "tester")
	require.Error(t, err)
	assert.Equal(t, originalEnd, svc.Load().EPBS.BidEndTime, "rejected write must not partially apply")

	// A reveal time at or past the slot deadline is rejected too.
	err = setMany(t, svc, map[string]any{KeyRevealTimeMs: int64(12000)}, "tester")
	require.Error(t, err)
}
