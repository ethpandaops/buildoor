package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// bootTiming simulates a process start the way cmd/run.go does it: the
// defaults follow the persisted slot timing (or the genesis duration), an
// optional operator-supplied reveal time overlays them.
func bootTiming(t *testing.T, store *db.Database, genesisMs int64, revealTime *int64) *Service {
	t.Helper()

	timing, ok, err := LoadSlotTiming(store)
	require.NoError(t, err)

	if !ok {
		timing = SlotTiming{DurationMs: genesisMs}
	}

	defaults := DefaultConfig()
	defaults.ApplySlotDefaults(timing.DurationMs)

	eff := *defaults
	supplied := map[string]bool{}

	if revealTime != nil {
		eff.Reveal.TimeMs = *revealTime
		supplied[KeyRevealTimeMs] = true
	}

	svc, err := NewService(&eff, defaults, supplied, store, testLogger())
	require.NoError(t, err)

	svc.InitSlotTiming(timing)

	return svc
}

func TestRescaleSlotTiming(t *testing.T) {
	store := db.NewDatabase(&db.Config{File: filepath.Join(t.TempDir(), "state.db")}, testLogger())
	require.NoError(t, store.Init())

	defer store.Close() //nolint:errcheck // test cleanup

	// 1. Genesis 12s; operator reveal time 5000 via CLI, the rest defaults.
	svc := bootTiming(t, store, 12000, i64(5000))
	cfg := svc.Load()
	require.Equal(t, int64(5000), cfg.Reveal.TimeMs)
	require.Equal(t, int64(-3400), cfg.EPBS.BuildStartTime)
	require.Equal(t, int64(500), cfg.Reveal.RetryIntervalMs)

	// 2. The 9s segment starts at slot 160: operator values scale by 9/12 and
	//    become UI overrides, defaults are recomputed, retries stay put.
	require.True(t, svc.RescaleSlotTiming(SlotTiming{Slot: 160, DurationMs: 9000}))
	require.Equal(t, int64(3750), cfg.Reveal.TimeMs)
	require.Equal(t, int64(-2550), cfg.EPBS.BuildStartTime)
	require.Equal(t, uint64(1950), cfg.PayloadBuildTime)
	require.Equal(t, int64(500), cfg.Reveal.RetryIntervalMs)
	require.Equal(t, SourceUI, svc.Sources()[KeyRevealTimeMs].Source)
	require.NotContains(t, svc.Sources(), KeyEPBSBuildStartTime)

	// 3. Only forward: the same or an earlier segment changes nothing.
	require.False(t, svc.RescaleSlotTiming(SlotTiming{Slot: 160, DurationMs: 9000}))
	require.False(t, svc.RescaleSlotTiming(SlotTiming{Slot: 0, DurationMs: 12000}))
	require.Equal(t, int64(3750), cfg.Reveal.TimeMs)

	timing, ok, err := LoadSlotTiming(store)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, SlotTiming{Slot: 160, DurationMs: 9000}, timing)

	// 4. Restart with the flag unchanged: the scaled value stays, the
	//    defaults follow the persisted 9s timing.
	svc = bootTiming(t, store, 12000, i64(5000))
	require.Equal(t, int64(3750), svc.Load().Reveal.TimeMs)
	require.Equal(t, int64(-2550), svc.Load().EPBS.BuildStartTime)

	// 5. A changed flag is taken as written for the current duration.
	svc = bootTiming(t, store, 12000, i64(4000))
	require.Equal(t, int64(4000), svc.Load().Reveal.TimeMs)
}

func TestRescaleSlotTimingWithoutStateDB(t *testing.T) {
	store := db.NewDatabase(&db.Config{File: ""}, testLogger())
	require.NoError(t, store.Init())

	// Without a state-db every start takes the operator values as written for
	// the genesis duration and scales them to the current segment.
	for range 2 {
		svc := bootTiming(t, store, 12000, i64(5000))
		require.True(t, svc.RescaleSlotTiming(SlotTiming{Slot: 160, DurationMs: 9000}))
		require.Equal(t, int64(3750), svc.Load().Reveal.TimeMs)
	}
}

func TestScaleTiming(t *testing.T) {
	tests := []struct {
		value any
		want  any
	}{
		{int64(-400), int64(-300)},
		{int64(1001), int64(751)},
		{int64(-1001), int64(-751)},
		{int64(0), int64(0)},
		{uint64(2600), uint64(1950)},
	}

	for _, tt := range tests {
		got, err := scaleTiming(tt.value, 12000, 9000)
		require.NoError(t, err)
		require.Equal(t, tt.want, got)
	}

	_, err := scaleTiming("x", 12000, 9000)
	require.Error(t, err)
}

func TestSlotScaledFieldsAreRegistered(t *testing.T) {
	registered := make(map[string]bool, len(slotScaledKeys))

	for _, f := range Fields() {
		if f.SlotScaled {
			registered[f.Key] = true
		}
	}

	for key := range slotScaledKeys {
		require.True(t, registered[key], "slot-scaled key %s is not a registered setting", key)
	}
}
