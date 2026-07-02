-- +goose Up
-- +goose StatementBegin
DROP TABLE IF EXISTS "won_blocks";
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS "won_blocks"
(
    "id"               INTEGER PRIMARY KEY AUTOINCREMENT,
    "source"           TEXT NOT NULL,
    "slot"             INTEGER NOT NULL,
    "block_hash"       TEXT NOT NULL,
    "num_transactions" INTEGER NOT NULL DEFAULT 0,
    "num_blobs"        INTEGER NOT NULL DEFAULT 0,
    "value_wei"        TEXT NOT NULL DEFAULT '0',
    "value_eth"        TEXT NOT NULL DEFAULT '0',
    "timestamp"        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS "won_blocks_slot_idx" ON "won_blocks" ("slot" DESC);
CREATE INDEX IF NOT EXISTS "won_blocks_ts_idx" ON "won_blocks" ("timestamp" DESC);
-- +goose StatementEnd
