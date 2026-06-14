package db

import (
	"github.com/jmoiron/sqlx"
)

// maxProposerPreferences bounds the proposer_preferences table; older slots are
// pruned on insert. These are network-sourced and best-effort to persist.
const maxProposerPreferences = 2000

// ProposerPreference is a persisted Gloas proposer preference (from gossip).
// raw holds the JSON-encoded SignedProposerPreferences for verbatim rehydration.
type ProposerPreference struct {
	Slot           uint64 `db:"slot"`
	ValidatorIndex uint64 `db:"validator_index"`
	FeeRecipient   string `db:"fee_recipient"`
	TargetGasLimit uint64 `db:"target_gas_limit"`
	Raw            string `db:"raw"`
}

// PutProposerPreference upserts a proposer preference by slot and prunes the
// table to maxProposerPreferences rows. No-op when the database is disabled.
func (d *Database) PutProposerPreference(p ProposerPreference) error {
	if !d.enabled {
		return nil
	}

	return d.RunDBTransaction(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO proposer_preferences
				(slot, validator_index, fee_recipient, target_gas_limit, raw)
			VALUES ($1, $2, $3, $4, $5)`,
			p.Slot, p.ValidatorIndex, p.FeeRecipient, p.TargetGasLimit, p.Raw); err != nil {
			return err
		}

		_, err := tx.Exec(`
			DELETE FROM proposer_preferences
			WHERE slot NOT IN (SELECT slot FROM proposer_preferences ORDER BY slot DESC LIMIT $1)`,
			maxProposerPreferences)

		return err
	})
}

// GetProposerPreferences returns up to limit most-recent proposer preferences
// (highest slot first). Returns an empty slice when the database is disabled.
func (d *Database) GetProposerPreferences(limit int) ([]ProposerPreference, error) {
	if !d.enabled {
		return []ProposerPreference{}, nil
	}

	if limit <= 0 {
		limit = maxProposerPreferences
	}

	prefs := []ProposerPreference{}

	err := d.readerDB.Select(&prefs, `
		SELECT slot, validator_index, fee_recipient, target_gas_limit, raw
		FROM proposer_preferences
		ORDER BY slot DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}

	return prefs, nil
}
