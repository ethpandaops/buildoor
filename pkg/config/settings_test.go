package config

import (
	"database/sql"
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

// A UI override stored under a retired key must carry over to the setting that
// replaced it: merging two settings may not discard what the operator set.
func TestRetiredKeyOverrideIsAdopted(t *testing.T) {
	dir := t.TempDir()
	store := db.NewDatabase(&db.Config{File: filepath.Join(dir, "state.db")}, testLogger())
	require.NoError(t, store.Init())

	// State left behind by a release that still had a separate top-up amount.
	require.NoError(t, store.PutSetting(db.SettingRow{
		Key:     "topup_amount",
		UIValue: sql.NullString{String: "32000000000", Valid: true},
		UISeq:   7,
		Actor:   "tester",
	}))

	defaults := defaultsConfig()

	svc := boot(t, store, defaults, nil)
	require.Equal(t, uint64(32000000000), svc.Load().DepositAmount)

	// The adoption is persisted under the successor key, and a later edit of
	// that key is not overwritten by the retired row on the next start.
	raw, err := json.Marshal(uint64(40000000000))
	require.NoError(t, err)
	require.NoError(t, svc.Set(KeyDepositAmount, raw, "tester"))

	svc = boot(t, store, defaults, nil)
	require.Equal(t, uint64(40000000000), svc.Load().DepositAmount)

	require.NoError(t, store.Close())
}

// Values the consuming module would silently replace with a fallback must be
// rejected at write time.
func TestSetRejectsValuesTheConsumerWouldIgnore(t *testing.T) {
	store := db.NewDatabase(&db.Config{File: ""}, testLogger())
	require.NoError(t, store.Init())

	svc := boot(t, store, defaultsConfig(), nil)

	tests := []struct {
		key   string
		value any
		ok    bool
	}{
		{KeyRevealGateMode, "vote_or_time", true},
		{KeyRevealGateMode, "votes", false},
		{KeyRevealBroadcastValidation, "consensus", true},
		{KeyRevealBroadcastValidation, "strict", false},
		{KeyBuildCandidateParentEmpty, "never", true},
		{KeyBuildCandidateParentEmpty, "sometimes", false},
		{KeyEPBSKeyStrategy, "least_used", true},
		{KeyEPBSKeyStrategy, "", false},
		{KeyBuilderAPIKeyStrategy, "", true},
		{KeyBuilderAPIKeyStrategy, "fastest", false},
		{KeyEPBSBidCandidate, "grandparent_full", true},
		{KeyEPBSBidCandidate, "uncle", false},
		{KeyBuilderAPIServeCandidates, "parent_full, parent_empty", true},
		{KeyBuilderAPIServeCandidates, "parent_full,uncle", false},
		{KeyRevealVoteThreshold, uint64(100), true},
		{KeyRevealVoteThreshold, uint64(101), false},
		{KeyBuilderAPIExecutionPaymentPct, uint64(250), false},
		{KeyDepositAmount, uint64(32000000000), true},
		{KeyDepositAmount, uint64(0), false},
		{KeyScheduleMode, "every_nth", true},
		{KeyScheduleMode, "sometimes", false},
	}

	for _, tt := range tests {
		raw, err := json.Marshal(tt.value)
		require.NoError(t, err)

		err = svc.Set(tt.key, raw, "tester")
		if tt.ok {
			require.NoError(t, err, "%s=%v", tt.key, tt.value)
		} else {
			require.Error(t, err, "%s=%v", tt.key, tt.value)
		}
	}
}

// The shipped defaults must pass the validation operator values go through.
func TestDefaultsAreValid(t *testing.T) {
	defaults := defaultsConfig()

	for _, field := range Fields() {
		require.NoError(t, ValidateSetting(field.Key, field.Get(defaults)), field.Key)
	}
}
