-- +goose Up
-- Enlaces públicos a un archivo o a una carpeta (nunca la raíz). El token va en
-- claro porque el dueño debe poder volver a copiar el enlace.
CREATE TABLE shares (
    id            INTEGER PRIMARY KEY,
    token         TEXT    NOT NULL UNIQUE,
    file_id       INTEGER REFERENCES files(id) ON DELETE CASCADE,
    folder_id     INTEGER REFERENCES folders(id) ON DELETE CASCADE,
    created_by    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    password_hash TEXT    NOT NULL DEFAULT '',
    expires_at    INTEGER NOT NULL DEFAULT 0,
    max_downloads INTEGER NOT NULL DEFAULT 0,
    downloads     INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    CHECK ((file_id IS NULL) <> (folder_id IS NULL))
);
CREATE INDEX shares_file_id ON shares(file_id);
CREATE INDEX shares_folder_id ON shares(folder_id);

-- +goose Down
DROP TABLE shares;
