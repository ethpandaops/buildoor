package db

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func testDB(t *testing.T) *Database {
	t.Helper()

	log := logrus.New()
	log.SetOutput(io.Discard)

	d := NewDatabase(&Config{File: filepath.Join(t.TempDir(), "state.db")}, log)
	require.NoError(t, d.Init())
	require.True(t, d.Enabled())

	t.Cleanup(func() { _ = d.Close() })

	return d
}

func TestAuditLogRoundTrip(t *testing.T) {
	d := testDB(t)

	require.NoError(t, d.AppendAuditLog(AuditLog{
		Timestamp: 100,
		Actor:     "alice",
		Action:    "config.epbs",
		Detail:    `{"bid_subsidy":600}`,
		Result:    "ok",
	}))
	require.NoError(t, d.AppendAuditLog(AuditLog{
		Timestamp: 200,
		Actor:     "bob",
		Action:    "services.toggle",
		Result:    "ok",
	}))

	entries, total, err := d.GetAuditLogs(0, 10)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, entries, 2)
	require.Equal(t, "bob", entries[0].Actor) // newest first
	require.Equal(t, "alice", entries[1].Actor)
}

func TestDisabledDBNoOps(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	d := NewDatabase(&Config{File: ""}, log)
	require.NoError(t, d.Init())
	require.False(t, d.Enabled())

	// Writes are dropped, reads are empty — never panic.
	require.NoError(t, d.AppendAuditLog(AuditLog{Timestamp: 1, Action: "noop"}))

	entries, total, err := d.GetAuditLogs(0, 10)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Zero(t, total)

	rows, err := d.GetSettings()
	require.NoError(t, err)
	require.Empty(t, rows)
}
