-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS "slot_artifacts"
(
    "slot"       INTEGER NOT NULL,
    "kind"       TEXT    NOT NULL,
    "idx"        INTEGER NOT NULL DEFAULT 0,
    "fork"       INTEGER NOT NULL DEFAULT 0,
    "meta"       TEXT    NOT NULL DEFAULT '',
    "data"       BLOB    NOT NULL,
    "created_at" INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT "slot_artifacts_pkey" PRIMARY KEY ("slot", "kind", "idx")
);

CREATE INDEX IF NOT EXISTS "idx_slot_artifacts_slot" ON "slot_artifacts" ("slot");
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS "slot_artifacts";
-- +goose StatementEnd
