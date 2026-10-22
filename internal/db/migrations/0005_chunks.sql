-- +goose Up
-- Los archivos dejan de apuntar a un único nodo remoto: pasan a tener bloques.
CREATE TABLE files_new (
    id            INTEGER PRIMARY KEY,
    folder_id     INTEGER NOT NULL DEFAULT 0,
    name          TEXT    NOT NULL,
    size          INTEGER NOT NULL,
    mime          TEXT    NOT NULL DEFAULT '',
    mtime         INTEGER NOT NULL,
    layout        TEXT    NOT NULL,            -- whole | rs
    data_shards   INTEGER NOT NULL DEFAULT 1,  -- k
    parity_shards INTEGER NOT NULL DEFAULT 0,  -- m
    chunk_size    INTEGER NOT NULL DEFAULT 0,  -- bytes por bloque de datos (0 en whole)
    sha256        TEXT    NOT NULL DEFAULT '', -- del contenido completo
    status        TEXT    NOT NULL DEFAULT 'ready', -- uploading | ready | degraded | lost
    created_at    INTEGER NOT NULL
);

CREATE TABLE chunks (
    id         INTEGER PRIMARY KEY,
    file_id    INTEGER NOT NULL REFERENCES files_new(id) ON DELETE CASCADE,
    stripe     INTEGER NOT NULL,
    idx        INTEGER NOT NULL,               -- 0..k+m-1 dentro de la franja
    kind       TEXT    NOT NULL,               -- data | parity
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    remote_id  TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    sha256     TEXT    NOT NULL DEFAULT '',
    UNIQUE (file_id, stripe, idx),
    UNIQUE (account_id, remote_id)
);

INSERT INTO files_new (id, folder_id, name, size, mime, mtime, layout, created_at)
SELECT id, folder_id, name, size, mime, remote_mtime, 'whole', created_at FROM files;

INSERT INTO chunks (file_id, stripe, idx, kind, account_id, remote_id, size)
SELECT id, 0, 0, 'data', account_id, remote_id, size FROM files;

DROP TABLE files;
ALTER TABLE files_new RENAME TO files;

CREATE INDEX files_folder_id ON files(folder_id, name);
CREATE INDEX files_name ON files(name);
CREATE INDEX chunks_file_id ON chunks(file_id, stripe, idx);
CREATE INDEX chunks_account_id ON chunks(account_id);

INSERT OR IGNORE INTO settings (key, value) VALUES
    ('data_shards', '2'),
    ('parity_shards', '1'),
    ('chunk_size', '33554432');

-- +goose Down
SELECT 1; -- irreversible: la migración 0004 no tiene bloques
