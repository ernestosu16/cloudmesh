-- name: CreateShare :one
INSERT INTO shares (token, file_id, folder_id, created_by, password_hash, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetShareByToken :one
SELECT * FROM shares WHERE token = ?;

-- name: GetShare :one
SELECT * FROM shares WHERE id = ?;

-- name: ListShares :many
SELECT s.*,
       CAST(COALESCE(f.name, d.name, '') AS TEXT) AS item_name,
       CAST(COALESCE(f.folder_id, d.parent_id, 0) AS INTEGER) AS item_parent_id,
       CAST(COALESCE(f.size, 0) AS INTEGER) AS item_size,
       CAST(COALESCE(u.email, '') AS TEXT) AS created_by_email
FROM shares s
LEFT JOIN files f ON f.id = s.file_id
LEFT JOIN folders d ON d.id = s.folder_id
LEFT JOIN users u ON u.id = s.created_by
ORDER BY s.created_at DESC, s.id DESC;

-- name: UpdateSharePassword :one
UPDATE shares SET password_hash = ? WHERE id = ?
RETURNING *;

-- name: DeleteShare :execrows
DELETE FROM shares WHERE id = ?;

-- name: CountShareDownload :exec
UPDATE shares SET downloads = downloads + 1 WHERE id = ?;
