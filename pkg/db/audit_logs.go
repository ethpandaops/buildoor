package db

import (
	"github.com/jmoiron/sqlx"
)

// maxAuditLogs bounds the audit_log table; older rows are pruned on insert.
const maxAuditLogs = 10000

// AuditLog records a single authenticated mutating action against the API.
type AuditLog struct {
	ID         int64  `db:"id" json:"id"`
	Timestamp  int64  `db:"timestamp" json:"timestamp"`
	Actor      string `db:"actor" json:"actor"`
	RemoteAddr string `db:"remote_addr" json:"remote_addr"`
	Action     string `db:"action" json:"action"`
	Target     string `db:"target" json:"target"`
	Detail     string `db:"detail" json:"detail"`
	Result     string `db:"result" json:"result"`
}

// AppendAuditLog inserts an audit entry and prunes the table to maxAuditLogs
// rows. No-op when the database is disabled.
func (d *Database) AppendAuditLog(entry AuditLog) error {
	if !d.enabled {
		return nil
	}

	return d.RunDBTransaction(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO audit_log
				(timestamp, actor, remote_addr, action, target, detail, result)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			entry.Timestamp, entry.Actor, entry.RemoteAddr, entry.Action,
			entry.Target, entry.Detail, entry.Result); err != nil {
			return err
		}

		_, err := tx.Exec(`
			DELETE FROM audit_log
			WHERE id NOT IN (SELECT id FROM audit_log ORDER BY id DESC LIMIT $1)`,
			maxAuditLogs)

		return err
	})
}

// GetAuditLogs returns a page of audit entries (newest first) and the total
// count. Returns an empty page when the database is disabled.
func (d *Database) GetAuditLogs(offset, limit int) ([]AuditLog, int, error) {
	if !d.enabled {
		return []AuditLog{}, 0, nil
	}

	if offset < 0 {
		offset = 0
	}

	if limit <= 0 {
		limit = 20
	}

	var total int
	if err := d.readerDB.Get(&total, `SELECT COUNT(*) FROM audit_log`); err != nil {
		return nil, 0, err
	}

	logs := []AuditLog{}

	err := d.readerDB.Select(&logs, `
		SELECT id, timestamp, actor, remote_addr, action, target, detail, result
		FROM audit_log
		ORDER BY id DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}

	return logs, total, nil
}
