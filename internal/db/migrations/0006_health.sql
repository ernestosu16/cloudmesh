-- +goose Up
-- Los bloques sobreviven al borrado de su cuenta (account_id a NULL) para que
-- el rebuild sepa qué regenerar. missing marca bloques ausentes o corruptos.
CREATE TABLE chunks_new (
    id         INTEGER PRIMARY KEY,
    file_id    INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    stripe     INTEGER NOT NULL,
    idx        INTEGER NOT NULL,
    kind       TEXT    NOT NULL,
    account_id INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    remote_id  TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    sha256     TEXT    NOT NULL DEFAULT '',
    missing    INTEGER NOT NULL DEFAULT 0,
    checked_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE (file_id, stripe, idx)
);
INSERT INTO chunks_new (id, file_id, stripe, idx, kind, account_id, remote_id, size, sha256)
SELECT id, file_id, stripe, idx, kind, account_id, remote_id, size, sha256 FROM chunks;
DROP TABLE chunks;
ALTER TABLE chunks_new RENAME TO chunks;
CREATE INDEX chunks_file_id ON chunks(file_id, stripe, idx);
CREATE INDEX chunks_account_id ON chunks(account_id);
CREATE UNIQUE INDEX chunks_account_remote ON chunks(account_id, remote_id) WHERE account_id IS NOT NULL;

ALTER TABLE accounts ADD COLUMN manifest_remote_id TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 1;
