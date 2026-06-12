package db

import (
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// SettingRow is the persisted 3-way state for a single settings key.
//
// Resolution: the hardcoded default (in code) is the floor; cli_value and
// ui_value override it, and whichever has the higher seq wins. A seq of 0
// means that layer is absent. cli_value tracks the last operator-supplied
// value (flag/env/config); a change to it is detected by value-diff on
// startup and bumps cli_seq so the CLI write "wins" until the UI sets a newer
// value.
type SettingRow struct {
	Key       string         `db:"key"`
	CLIValue  sql.NullString `db:"cli_value"`
	CLISeq    int64          `db:"cli_seq"`
	UIValue   sql.NullString `db:"ui_value"`
	UISeq     int64          `db:"ui_seq"`
	UpdatedAt int64          `db:"updated_at"`
	Actor     string         `db:"actor"`
}

// GetSettings returns all persisted settings rows. Returns an empty slice when
// the database is disabled.
func (d *Database) GetSettings() ([]SettingRow, error) {
	if !d.enabled {
		return nil, nil
	}

	rows := []SettingRow{}

	err := d.readerDB.Select(&rows, `
		SELECT key, cli_value, cli_seq, ui_value, ui_seq, updated_at, actor
		FROM settings`)
	if err != nil {
		return nil, err
	}

	return rows, nil
}

// PutSetting upserts the full 3-way state for a settings key. No-op when the
// database is disabled. The settings service owns the in-memory authority and
// always writes the complete row, so a plain INSERT OR REPLACE is correct.
func (d *Database) PutSetting(row SettingRow) error {
	if !d.enabled {
		return nil
	}

	return d.RunDBTransaction(func(tx *sqlx.Tx) error {
		_, err := tx.Exec(`
			INSERT OR REPLACE INTO settings
				(key, cli_value, cli_seq, ui_value, ui_seq, updated_at, actor)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			row.Key, row.CLIValue, row.CLISeq, row.UIValue, row.UISeq, row.UpdatedAt, row.Actor)

		return err
	})
}
