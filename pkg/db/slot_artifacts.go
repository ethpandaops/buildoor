package db

import (
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

// SlotArtifact is one raw SSZ object produced for a slot: the built execution
// payload, a signed bid, or the signed payload envelope. Data holds the SSZ
// bytes; Fork identifies the container version for decoding; Meta is a small
// versioned JSON blob with display metadata (transport, values, timestamps).
type SlotArtifact struct {
	Slot      uint64 `db:"slot"`
	Kind      string `db:"kind"`
	Idx       int    `db:"idx"`
	Fork      int64  `db:"fork"`
	Meta      string `db:"meta"`
	Data      []byte `db:"data"`
	CreatedAt int64  `db:"created_at"`
}

// InsertSlotArtifacts writes a batch of artifacts in one transaction, using
// INSERT OR REPLACE (index allocation is the caller's responsibility and is
// restart-safe via GetMaxSlotArtifactIdx). No-op when the database is
// disabled or the batch is empty.
func (d *Database) InsertSlotArtifacts(batch []SlotArtifact) error {
	if !d.enabled || len(batch) == 0 {
		return nil
	}

	return d.RunDBTransaction(func(tx *sqlx.Tx) error {
		for _, artifact := range batch {
			if _, err := tx.Exec(`
				INSERT OR REPLACE INTO slot_artifacts
					(slot, kind, idx, fork, meta, data, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				artifact.Slot, artifact.Kind, artifact.Idx, artifact.Fork,
				artifact.Meta, artifact.Data, artifact.CreatedAt); err != nil {
				return err
			}
		}

		return nil
	})
}

// GetSlotArtifact returns one artifact, or nil when it does not exist (or the
// database is disabled).
func (d *Database) GetSlotArtifact(slot uint64, kind string, idx int) (*SlotArtifact, error) {
	if !d.enabled {
		return nil, nil
	}

	artifact := &SlotArtifact{}

	err := d.readerDB.Get(artifact, `
		SELECT slot, kind, idx, fork, meta, data, created_at
		FROM slot_artifacts
		WHERE slot = $1 AND kind = $2 AND idx = $3`, slot, kind, idx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	return artifact, nil
}

// GetSlotArtifactMetas lists a slot's artifacts of one kind without their
// data blobs (idx-ascending). Returns an empty slice when the database is
// disabled.
func (d *Database) GetSlotArtifactMetas(slot uint64, kind string) ([]SlotArtifact, error) {
	if !d.enabled {
		return []SlotArtifact{}, nil
	}

	metas := []SlotArtifact{}

	err := d.readerDB.Select(&metas, `
		SELECT slot, kind, idx, fork, meta, created_at
		FROM slot_artifacts
		WHERE slot = $1 AND kind = $2
		ORDER BY idx ASC`, slot, kind)
	if err != nil {
		return nil, err
	}

	return metas, nil
}

// GetMaxSlotArtifactIdx returns the highest stored index for (slot, kind) and
// whether any row exists — used to re-initialize index allocation after a
// restart so earlier artifacts are never overwritten.
func (d *Database) GetMaxSlotArtifactIdx(slot uint64, kind string) (int, bool, error) {
	if !d.enabled {
		return 0, false, nil
	}

	var maxIdx sql.NullInt64

	err := d.readerDB.Get(&maxIdx, `
		SELECT MAX(idx) FROM slot_artifacts
		WHERE slot = $1 AND kind = $2`, slot, kind)
	if err != nil {
		return 0, false, err
	}

	if !maxIdx.Valid {
		return 0, false, nil
	}

	return int(maxIdx.Int64), true, nil
}

// DeleteSlotArtifactsBefore prunes all artifacts for slots below the cutoff
// and returns the number of deleted rows. No-op when the database is
// disabled.
func (d *Database) DeleteSlotArtifactsBefore(cutoffSlot uint64) (int64, error) {
	if !d.enabled {
		return 0, nil
	}

	var deleted int64

	err := d.RunDBTransaction(func(tx *sqlx.Tx) error {
		result, err := tx.Exec(`DELETE FROM slot_artifacts WHERE slot < $1`, cutoffSlot)
		if err != nil {
			return err
		}

		deleted, err = result.RowsAffected()

		return err
	})

	return deleted, err
}
