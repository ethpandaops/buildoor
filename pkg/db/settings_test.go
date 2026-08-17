package db

import (
	"database/sql"
	"io"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPutSettingsBatchRoundTrip confirms a multi-row batch upserts every row
// in the single transaction PutSettings runs (NM-23: settings used to be
// persisted one row per transaction).
func TestPutSettingsBatchRoundTrip(t *testing.T) {
	d := testDB(t)

	require.NoError(t, d.PutSettings([]SettingRow{
		{Key: "a", UIValue: sql.NullString{String: `1`, Valid: true}, UISeq: 1, UpdatedAt: 100},
		{Key: "b", UIValue: sql.NullString{String: `2`, Valid: true}, UISeq: 1, UpdatedAt: 100},
		{Key: "c", UIValue: sql.NullString{String: `3`, Valid: true}, UISeq: 1, UpdatedAt: 100},
	}))

	rows, err := d.GetSettings()
	require.NoError(t, err)

	byKey := make(map[string]SettingRow, len(rows))
	for _, r := range rows {
		byKey[r.Key] = r
	}

	require.Len(t, byKey, 3)
	assert.Equal(t, `1`, byKey["a"].UIValue.String)
	assert.Equal(t, `2`, byKey["b"].UIValue.String)
	assert.Equal(t, `3`, byKey["c"].UIValue.String)
}

// TestPutSettingsNoopWhenDisabled confirms a batch write against a disabled
// (no state-db configured) database is a silent no-op, not an error --
// callers rely on this to keep working in-memory-only.
func TestPutSettingsNoopWhenDisabled(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	d := NewDatabase(&Config{}, log)
	require.NoError(t, d.Init())
	require.False(t, d.Enabled())

	require.NoError(t, d.PutSettings([]SettingRow{{Key: "a"}}))
}

// TestPutSettingsFailsAfterClose confirms a batch write against a closed
// connection reports an error rather than silently succeeding.
func TestPutSettingsFailsAfterClose(t *testing.T) {
	d := testDB(t)
	require.NoError(t, d.Close())

	err := d.PutSettings([]SettingRow{{Key: "a"}})
	assert.Error(t, err)
}
