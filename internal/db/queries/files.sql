-- name: InsertFile :one
INSERT INTO files (folder_id, name, size, mime, mtime, layout, data_shards, parity_shards, chunk_size, sha256, status, created_at, internal, etag, owner_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetFile :one
SELECT * FROM files WHERE id = ?;

-- name: ListFilesInFolder :many
SELECT * FROM files WHERE folder_id = ? AND status <> 'uploading' AND deleted_at = 0 AND internal = 0 ORDER BY name COLLATE NOCASE;

-- name: SearchFiles :many
SELECT * FROM files WHERE name LIKE '%' || @q || '%' AND status <> 'uploading' AND deleted_at = 0 AND internal = 0
ORDER BY name COLLATE NOCASE LIMIT 100;

-- name: UpdateFileLocation :one
UPDATE files SET name = ?, folder_id = ? WHERE id = ?
RETURNING *;

-- name: UpdateFileRemote :exec
UPDATE files SET name = ?, size = ?, mime = ?, mtime = ? WHERE id = ?;

-- name: FinishFile :exec
UPDATE files SET size = ?, sha256 = ?, etag = ?, status = 'ready' WHERE id = ?;

-- name: SetFileStatus :exec
UPDATE files SET status = ? WHERE id = ?;

-- name: DeleteFile :execrows
DELETE FROM files WHERE id = ?;

-- name: CountFiles :one
SELECT COUNT(*) FROM files;

-- name: ListWholeFilesByAccount :many
SELECT f.id, f.name, f.size, f.mtime, c.remote_id
FROM files f JOIN chunks c ON c.file_id = f.id
WHERE f.layout = 'whole' AND c.account_id = ?;

-- name: InsertChunk :one
INSERT INTO chunks (file_id, stripe, idx, kind, account_id, remote_id, size, sha256, encrypted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListChunks :many
SELECT c.*, CAST(COALESCE(a.label, '') AS TEXT) AS account_label, CAST(COALESCE(a.status, '') AS TEXT) AS account_status
FROM chunks c LEFT JOIN accounts a ON a.id = c.account_id
WHERE c.file_id = ? ORDER BY c.stripe, c.idx;

-- name: CountChunksByAccount :one
SELECT COUNT(*) FROM chunks WHERE account_id = ?;

-- name: AccountLabels :many
SELECT id, label FROM accounts;

-- name: ListChunksByAccount :many
SELECT * FROM chunks WHERE account_id = ?;

-- name: ListChunkRowsByFile :many
SELECT * FROM chunks WHERE file_id = ? ORDER BY stripe, idx;

-- name: MarkChunkMissing :exec
UPDATE chunks SET missing = ?, checked_at = ? WHERE id = ?;

-- name: ReplaceChunk :exec
UPDATE chunks SET account_id = ?, remote_id = ?, size = ?, sha256 = ?, encrypted = ?, missing = 0, checked_at = ? WHERE id = ?;

-- name: SetFileKey :execrows
UPDATE files SET enc_key = ? WHERE id = ? AND enc_key = x'';

-- name: ListPlainChunks :many
SELECT c.* FROM chunks c JOIN files f ON f.id = c.file_id
WHERE c.encrypted = 0 AND c.missing = 0 AND c.account_id IS NOT NULL
  AND f.layout = 'rs' AND f.status <> 'uploading' AND c.id > ?
ORDER BY c.id LIMIT ?;

-- name: EncryptChunk :execrows
UPDATE chunks SET remote_id = ?, size = ?, sha256 = ?, encrypted = 1, checked_at = ?
WHERE id = ? AND remote_id = ? AND encrypted = 0;

-- name: CountWholeFiles :one
SELECT COUNT(*) FROM files WHERE layout = 'whole';

-- name: CountPlainChunks :one
SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id
WHERE c.encrypted = 0 AND f.layout = 'rs';

-- name: ListFileIDsWithChunksOn :many
SELECT DISTINCT file_id FROM chunks WHERE account_id = ?;

-- name: ListFileIDsNeedingRepair :many
SELECT DISTINCT file_id FROM chunks WHERE missing = 1 OR account_id IS NULL;

-- name: ListFilesByStatus :many
SELECT * FROM files WHERE status = ? ORDER BY name COLLATE NOCASE LIMIT 200;

-- name: CountFilesByStatus :many
SELECT status, COUNT(*) AS n FROM files WHERE internal = 0 GROUP BY status;

-- name: CountMissingChunks :one
SELECT COUNT(*) FROM chunks WHERE missing = 1 OR account_id IS NULL;

-- name: CountChunksPerAccount :many
SELECT account_id, COUNT(*) AS n, COALESCE(SUM(missing), 0) AS missing FROM chunks WHERE account_id IS NOT NULL GROUP BY account_id;

-- name: ListAllFiles :many
SELECT * FROM files ORDER BY id;

-- name: ListAllChunks :many
SELECT * FROM chunks ORDER BY id;

-- name: ListAllFolders :many
SELECT * FROM folders ORDER BY id;

-- name: InsertFolderWithID :exec
INSERT INTO folders (id, parent_id, name, created_at, restricted) VALUES (?, ?, ?, ?, ?);

-- name: InsertFileWithID :exec
INSERT INTO files (id, folder_id, name, size, mime, mtime, layout, data_shards, parity_shards, chunk_size, sha256, status, created_at, deleted_at, etag, internal, meta, owner_id, enc_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertChunkWithID :exec
INSERT INTO chunks (id, file_id, stripe, idx, kind, account_id, remote_id, size, sha256, missing, checked_at, encrypted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: TrashFile :execrows
UPDATE files SET deleted_at = ? WHERE id = ? AND deleted_at = 0 AND status <> 'uploading';

-- name: RestoreFile :one
UPDATE files SET deleted_at = 0, folder_id = ? WHERE id = ? AND deleted_at > 0
RETURNING *;

-- name: ListTrash :many
SELECT f.*,
       CAST(COALESCE(d.name, '') AS TEXT) AS folder_name,
       CAST(f.folder_id = 0 OR d.id IS NOT NULL AS INTEGER) AS folder_exists
FROM files f LEFT JOIN folders d ON d.id = f.folder_id
WHERE f.deleted_at > 0
ORDER BY f.deleted_at DESC, f.id DESC;

-- name: ListTrashedBefore :many
SELECT id FROM files WHERE deleted_at > 0 AND deleted_at < ? ORDER BY id;

-- name: ListObjectFiles :many
SELECT * FROM files
WHERE folder_id = ? AND name = ? AND status <> 'uploading' AND deleted_at = 0 AND internal = 0
ORDER BY id DESC;

-- name: InsertFilePart :exec
INSERT INTO file_parts (file_id, part_number, part_file_id, size) VALUES (?, ?, ?, ?);

-- name: ListFileParts :many
SELECT * FROM file_parts WHERE file_id = ? ORDER BY part_number;

-- name: ListAllFileParts :many
SELECT * FROM file_parts ORDER BY file_id, part_number;

-- name: ListOrphanInternalFiles :many
SELECT f.id FROM files f
WHERE f.internal = 1 AND f.created_at < ?
  AND NOT EXISTS (SELECT 1 FROM file_parts p WHERE p.part_file_id = f.id)
  AND NOT EXISTS (SELECT 1 FROM s3_upload_parts u WHERE u.file_id = f.id);

-- name: ListCompositesOfPart :many
SELECT file_id FROM file_parts WHERE part_file_id = ?;

-- name: SetClientMeta :exec
UPDATE files SET mtime = ?, meta = ? WHERE id = ?;

-- name: ListAllRemoteChunks :many
SELECT c.account_id, c.remote_id, f.layout
FROM chunks c JOIN files f ON f.id = c.file_id
WHERE c.account_id IS NOT NULL;

-- name: DeleteAllFiles :execrows
DELETE FROM files;
