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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS "kv_store";
-- +goose StatementEnd
