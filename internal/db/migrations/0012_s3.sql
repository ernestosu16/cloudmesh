-- +goose Up
-- API compatible con S3 al estilo de Cloudflare R2. Cada bucket apunta a una
-- carpeta del disco: las claves con «/» son subcarpetas y los objetos son
-- archivos normales del índice.

-- etag: MD5 en hex (o «md5-N» en multipart), lo que esperan los clientes S3.
-- internal: partes de una subida multipart; no se ven en el disco.
ALTER TABLE files ADD COLUMN etag TEXT NOT NULL DEFAULT '';
ALTER TABLE files ADD COLUMN internal INTEGER NOT NULL DEFAULT 0;

-- Un archivo con layout 'parts' es la concatenación de sus partes, en orden.
CREATE TABLE file_parts (
    file_id      INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    part_number  INTEGER NOT NULL,
    part_file_id INTEGER NOT NULL REFERENCES files(id),
    size         INTEGER NOT NULL,
    PRIMARY KEY (file_id, part_number)
);
CREATE INDEX file_parts_part ON file_parts(part_file_id);

CREATE TABLE s3_buckets (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    folder_id  INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL
);

-- Llaves de acceso. permission: admin_rw | admin_ro | object_rw | object_ro.
-- Las de objetos pueden limitarse a ciertos buckets (s3_key_buckets); sin
-- filas, valen para todos. El secreto va cifrado con la clave maestra porque
-- SigV4 lo necesita en claro para verificar firmas.
CREATE TABLE s3_keys (
    id            INTEGER PRIMARY KEY,
    access_key_id TEXT    NOT NULL UNIQUE,
    enc_secret    BLOB    NOT NULL,
    name          TEXT    NOT NULL,
    permission    TEXT    NOT NULL,
    created_by    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE s3_key_buckets (
    key_id    INTEGER NOT NULL REFERENCES s3_keys(id) ON DELETE CASCADE,
    bucket_id INTEGER NOT NULL REFERENCES s3_buckets(id) ON DELETE CASCADE,
    PRIMARY KEY (key_id, bucket_id)
);

-- Subidas multipart en curso; las partes son archivos internos.
CREATE TABLE s3_uploads (
    id         TEXT    PRIMARY KEY,
    bucket_id  INTEGER NOT NULL REFERENCES s3_buckets(id) ON DELETE CASCADE,
    object_key TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE s3_upload_parts (
    upload_id   TEXT    NOT NULL REFERENCES s3_uploads(id) ON DELETE CASCADE,
    part_number INTEGER NOT NULL,
    file_id     INTEGER NOT NULL REFERENCES files(id),
    size        INTEGER NOT NULL,
    etag        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (upload_id, part_number)
);

-- +goose Down
DROP TABLE s3_upload_parts;
DROP TABLE s3_uploads;
DROP TABLE s3_key_buckets;
DROP TABLE s3_keys;
DROP TABLE s3_buckets;
DROP TABLE file_parts;
ALTER TABLE files DROP COLUMN internal;
ALTER TABLE files DROP COLUMN etag;
