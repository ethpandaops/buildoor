package builder_keys

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// otherEntryKey is a second, unrelated entry key used to simulate an operator
// pointing the same state-db at different builder key material.
const otherEntryKey = "5c1d7a4b2e9f30586ca1d4b7e29f0563a8d1c4b7e29f0536a8d1c4b7e29f0536"

func testDB(t *testing.T) *db.Database {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	stateDB := db.NewDatabase(&db.Config{File: filepath.Join(t.TempDir(), "state.db")}, log)
	require.NoError(t, stateDB.Init())

	t.Cleanup(func() { _ = stateDB.Close() })

	return stateDB
}

func newTestRegistry(t *testing.T, entryKey string) *Registry {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	cfg := &config.Config{BuilderKeys: config.BuilderKeysConfig{
		TargetCount:  1,
		DiscoveryGap: 2,
		MaxIndex:     50,
	}}

	registry, err := NewRegistry(config.NewStaticService(cfg), entryKey, log)
	require.NoError(t, err)

	return registry
}

// Usage history must survive a restart: a key deposited in an earlier run is
// recognised as ours before the beacon state confirms it, so the reconciler
// does not deposit for it twice.
func TestUsageSurvivesRestart(t *testing.T) {
	stateDB := testDB(t)

	first := newTestRegistry(t, testEntryKey)
	require.NoError(t, first.Start(context.Background(), nil, stateDB))

	first.MarkDepositSubmitted(3)
	require.NoError(t, first.usage.Flush())

	first.Stop()

	second := newTestRegistry(t, testEntryKey)
	require.NoError(t, second.Start(context.Background(), nil, stateDB))

	defer second.Stop()

	state := second.State(3)
	require.NotNil(t, state, "the key deposited before the restart must be tracked")
	require.Equal(t, uint32(1), state.UseCount)
	// The in-flight marker is process-local, so after a restart the key reads as
	// withdrawn until the beacon state proves otherwise.
	require.Equal(t, StatusWithdrawn, state.Status)

	// It is the lowest reusable index, so it is picked before any fresh index
	// above it — that is what keeps the highest derivation index bounded.
	require.Equal(t, uint64(0), second.NextDepositCandidate().KeyIndex())
}

// Pointing a state-db at different key material must fail loudly: acting on
// those records would deposit for, top up, or exit keys we do not control.
func TestStartRejectsForeignUsageRecords(t *testing.T) {
	stateDB := testDB(t)

	first := newTestRegistry(t, testEntryKey)
	require.NoError(t, first.Start(context.Background(), nil, stateDB))

	first.MarkDepositSubmitted(2)
	require.NoError(t, first.usage.Flush())

	first.Stop()

	foreign := newTestRegistry(t, otherEntryKey)

	err := foreign.Start(context.Background(), nil, stateDB)
	require.ErrorContains(t, err, "builder key source changed")
}

func TestRegistryWithoutStateDBStillDerives(t *testing.T) {
	registry := newTestRegistry(t, testEntryKey)
	require.NoError(t, registry.Start(context.Background(), nil, nil))

	defer registry.Stop()

	entrySigner, err := signer.NewBLSSigner(testEntryKey)
	require.NoError(t, err)
	require.Equal(t, entrySigner.PublicKey(), registry.Primary().Pubkey())
	require.Len(t, registry.Keys(), 1)
}
