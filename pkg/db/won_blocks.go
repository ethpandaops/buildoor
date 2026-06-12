package db

import (
	"github.com/jmoiron/sqlx"
)

// maxWonBlocks bounds the won_blocks table; older rows are pruned on insert.
const maxWonBlocks = 5000

// WonBlockSource identifies which subsystem delivered a won block.
const (
	WonBlockSourceBuilderAPI = "builder_api"
	WonBlockSourceEPBS       = "epbs"
)

// WonBlock is a successfully delivered/included block, from either the Builder
// API or the ePBS reveal path.
type WonBlock struct {
	ID              int64  `db:"id" json:"-"`
	Source          string `db:"source" json:"source"`
	Slot            uint64 `db:"slot" json:"slot"`
	BlockHash       string `db:"block_hash" json:"block_hash"`
	NumTransactions int    `db:"num_transactions" json:"num_transactions"`
	NumBlobs        int    `db:"num_blobs" json:"num_blobs"`
	ValueWei        string `db:"value_wei" json:"value_wei"`
	ValueETH        string `db:"value_eth" json:"value_eth"`
	Timestamp       int64  `db:"timestamp" json:"timestamp"`
}

// AddWonBlock inserts a won block and prunes the table to maxWonBlocks rows.
// No-op when the database is disabled.
func (d *Database) AddWonBlock(wb WonBlock) error {
	if !d.enabled {
		return nil
	}

	return d.RunDBTransaction(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO won_blocks
				(source, slot, block_hash, num_transactions, num_blobs, value_wei, value_eth, timestamp)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			wb.Source, wb.Slot, wb.BlockHash, wb.NumTransactions, wb.NumBlobs,
			wb.ValueWei, wb.ValueETH, wb.Timestamp); err != nil {
			return err
		}

		_, err := tx.Exec(`
			DELETE FROM won_blocks
			WHERE id NOT IN (SELECT id FROM won_blocks ORDER BY id DESC LIMIT $1)`,
			maxWonBlocks)

		return err
	})
}

// GetWonBlocks returns a page of won blocks (newest first) and the total count.
// Returns an empty page when the database is disabled.
func (d *Database) GetWonBlocks(offset, limit int) ([]WonBlock, int, error) {
	if !d.enabled {
		return []WonBlock{}, 0, nil
	}

	if offset < 0 {
		offset = 0
	}

	if limit <= 0 {
		limit = 20
	}

	var total int
	if err := d.readerDB.Get(&total, `SELECT COUNT(*) FROM won_blocks`); err != nil {
		return nil, 0, err
	}

	blocks := []WonBlock{}

	err := d.readerDB.Select(&blocks, `
		SELECT id, source, slot, block_hash, num_transactions, num_blobs, value_wei, value_eth, timestamp
		FROM won_blocks
		ORDER BY id DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}

	return blocks, total, nil
}
