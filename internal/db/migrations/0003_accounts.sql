-- +goose Up
CREATE TABLE accounts (
    id              INTEGER PRIMARY KEY,
    provider        TEXT    NOT NULL,
    label           TEXT    NOT NULL,
    email           TEXT    NOT NULL,
    enc_credentials BLOB    NOT NULL,
    status          TEXT    NOT NULL,            -- active | reconnect_required
    enabled         INTEGER NOT NULL DEFAULT 1,  -- participa en el enrutado de subidas
    priority        INTEGER NOT NULL DEFAULT 100,
    quota_used      INTEGER NOT NULL DEFAULT 0,
    quota_total     INTEGER NOT NULL DEFAULT 0,
    root_remote_id  TEXT    NOT NULL DEFAULT '',
    last_error      TEXT    NOT NULL DEFAULT '',
    last_sync_at    INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    UNIQUE (provider, email)
);

-- +goose Down
DROP TABLE accounts;
