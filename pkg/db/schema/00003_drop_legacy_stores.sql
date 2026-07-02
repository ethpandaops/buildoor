-- +goose Up
-- +goose StatementBegin
DROP TABLE IF EXISTS "proposer_preferences";
DROP TABLE IF EXISTS "validator_registrations";
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
-- +goose StatementEnd
