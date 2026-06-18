package config

import (
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/db"
)

const subsidyKey = "epbs.bid_subsidy"

func testLogger() logrus.FieldLogger {
	l := logrus.New()
	l.SetOutput(io.Discard)

	return l
}

func defaultsConfig() *Config {
	c := DefaultConfig()
	c.ApplySlotDefaults(12000)

	return c
}

// boot simulates a process start: it builds a fresh effective config from the
// (possibly bumped) defaults, optionally overlays an operator-supplied subsidy,
// and constructs a settings.Service against the shared db.
func boot(t *testing.T, store *db.Database, defaults *Config, suppliedVal *uint64) *Service {
	t.Helper()

	eff := *defaults // value copy — the shared effective config for this "boot"
	supplied := map[string]bool{}

	if suppliedVal != nil {
		eff.EPBS.BidSubsidy = *suppliedVal
		supplied[subsidyKey] = true
	}

	svc, err := NewService(&eff, defaults, supplied, store, testLogger())
	require.NoError(t, err)

	return svc
}

func u64(v uint64) *uint64 { return &v }

func setSubsidy(t *testing.T, svc *Service, v uint64) {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err)
	require.NoError(t, svc.Set(subsidyKey, raw, "tester"))
}

// TestThreeWayResolution walks the worked example from the design: UI overrides
// survive unchanged-flag restarts, a changed flag wins, and a default bump never
// clobbers a UI override.
func TestThreeWayResolution(t *testing.T) {
	dir := t.TempDir()
	store := db.NewDatabase(&db.Config{File: filepath.Join(dir, "state.db")}, testLogger())
	require.NoError(t, store.Init())

	defaults := defaultsConfig()
	require.Equal(t, uint64(100000000), defaults.EPBS.BidSubsidy)

	// 1. Boot with --epbs-bid-subsidy 500 -> CLI wins over default.
	svc := boot(t, store, defaults, u64(500))
	require.Equal(t, uint64(500), svc.Load().EPBS.BidSubsidy)

	// 2. UI sets 600 -> UI wins.
	setSubsidy(t, svc, 600)
	require.Equal(t, uint64(600), svc.Load().EPBS.BidSubsidy)

	// 3. Restart, flag unchanged at 500 -> UI override (600) still wins.
	svc = boot(t, store, defaults, u64(500))
	require.Equal(t, uint64(600), svc.Load().EPBS.BidSubsidy)

	// 4. Operator changes the flag to 700 -> CLI change wins over old UI value.
	svc = boot(t, store, defaults, u64(700))
	require.Equal(t, uint64(700), svc.Load().EPBS.BidSubsidy)

	// 5. Restart, flag unchanged at 700 -> CLI stays (UI 600 does not resurrect).
	svc = boot(t, store, defaults, u64(700))
	require.Equal(t, uint64(700), svc.Load().EPBS.BidSubsidy)

	// 6. UI sets 800 -> UI wins again.
	setSubsidy(t, svc, 800)
	require.Equal(t, uint64(800), svc.Load().EPBS.BidSubsidy)

	// 7. Upgrade safety: flag removed, hardcoded default bumped to 550M.
	//    The default bump must NOT clobber the UI override.
	bumped := defaultsConfig()
	bumped.EPBS.BidSubsidy = 550000000
	svc = boot(t, store, bumped, nil)
	require.Equal(t, uint64(800), svc.Load().EPBS.BidSubsidy)

	require.NoError(t, store.Close())
}

// TestDisabledDBNoPersistence verifies that with a disabled db, settings still
// resolve in-memory but nothing survives a "restart".
func TestDisabledDBNoPersistence(t *testing.T) {
	store := db.NewDatabase(&db.Config{File: ""}, testLogger())
	require.NoError(t, store.Init())
	require.False(t, store.Enabled())

	defaults := defaultsConfig()

	svc := boot(t, store, defaults, nil)
	require.Equal(t, defaults.EPBS.BidSubsidy, svc.Load().EPBS.BidSubsidy)

	setSubsidy(t, svc, 123)
	require.Equal(t, uint64(123), svc.Load().EPBS.BidSubsidy)

	// New "boot" — no persistence, falls back to the default.
	svc = boot(t, store, defaults, nil)
	require.Equal(t, defaults.EPBS.BidSubsidy, svc.Load().EPBS.BidSubsidy)
}

// TestUnsuppliedUsesDefault verifies an unsupplied key resolves to the default
// even if the effective config was seeded with a different value.
func TestUnsuppliedUsesDefault(t *testing.T) {
	store := db.NewDatabase(&db.Config{File: ""}, testLogger())
	require.NoError(t, store.Init())

	defaults := defaultsConfig()

	eff := *defaults
	eff.EPBS.BidSubsidy = 999 // present in effective but NOT operator-supplied

	svc, err := NewService(&eff, defaults, map[string]bool{}, store, testLogger())
	require.NoError(t, err)
	require.Equal(t, defaults.EPBS.BidSubsidy, svc.Load().EPBS.BidSubsidy)
}
