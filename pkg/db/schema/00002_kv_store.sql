-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS "kv_store"
(
    "namespace"  TEXT    NOT NULL,
    "key"        TEXT    NOT NULL,
    "value"      BLOB    NOT NULL,
    "updated_at" INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT "kv_store_pkey" PRIMARY KEY ("namespace", "key")
);

-- The typed store tables are replaced by kv_store namespaces
-- (proposer_preferences, validator_registrations, won_blocks).
DROP TABLE IF EXISTS "proposer_preferences";
DROP TABLE IF EXISTS "validator_registrations";
DROP TABLE IF EXISTS "won_blocks";
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS "kv_store";

CREATE TABLE IF NOT EXISTS "validator_registrations"
(
    "pubkey"        TEXT NOT NULL,
    "fee_recipient" TEXT NOT NULL,
    "gas_limit"     INTEGER NOT NULL DEFAULT 0,
    "timestamp"     INTEGER NOT NULL DEFAULT 0,
    "raw"           TEXT NOT NULL,
    "updated_at"    INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT "validator_registrations_pkey" PRIMARY KEY ("pubkey")
);

CREATE TABLE IF NOT EXISTS "proposer_preferences"
(
    "slot"             INTEGER NOT NULL,
    "validator_index"  INTEGER NOT NULL DEFAULT 0,
    "fee_recipient"    TEXT NOT NULL DEFAULT '',
    "target_gas_limit" INTEGER NOT NULL DEFAULT 0,
    "raw"              TEXT NOT NULL,
    CONSTRAINT "proposer_preferences_pkey" PRIMARY KEY ("slot")
);

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
