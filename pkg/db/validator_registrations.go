package db

import (
	"github.com/jmoiron/sqlx"
)

// ValidatorRegistration is a persisted Builder API validator registration
// (proposer fee-recipient preference). raw holds the JSON-encoded signed
// registration so the in-memory store can be rehydrated verbatim on restart.
type ValidatorRegistration struct {
	Pubkey       string `db:"pubkey"`
	FeeRecipient string `db:"fee_recipient"`
	GasLimit     uint64 `db:"gas_limit"`
	Timestamp    int64  `db:"timestamp"`
	Raw          string `db:"raw"`
	UpdatedAt    int64  `db:"updated_at"`
}

// PutValidatorRegistration upserts a validator registration by pubkey. No-op
// when the database is disabled.
func (d *Database) PutValidatorRegistration(reg ValidatorRegistration) error {
	if !d.enabled {
		return nil
	}

	return d.RunDBTransaction(func(tx *sqlx.Tx) error {
		_, err := tx.Exec(`
			INSERT OR REPLACE INTO validator_registrations
				(pubkey, fee_recipient, gas_limit, timestamp, raw, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			reg.Pubkey, reg.FeeRecipient, reg.GasLimit, reg.Timestamp, reg.Raw, reg.UpdatedAt)

		return err
	})
}

// GetValidatorRegistrations returns all persisted validator registrations.
// Returns an empty slice when the database is disabled.
func (d *Database) GetValidatorRegistrations() ([]ValidatorRegistration, error) {
	if !d.enabled {
		return []ValidatorRegistration{}, nil
	}

	regs := []ValidatorRegistration{}

	err := d.readerDB.Select(&regs, `
		SELECT pubkey, fee_recipient, gas_limit, timestamp, raw, updated_at
		FROM validator_registrations`)
	if err != nil {
		return nil, err
	}

	return regs, nil
}
