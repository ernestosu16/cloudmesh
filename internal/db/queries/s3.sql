-- name: CreateBucket :one
INSERT INTO s3_buckets (name, folder_id, created_at) VALUES (?, ?, ?)
RETURNING *;

-- name: GetBucketByName :one
SELECT * FROM s3_buckets WHERE name = ?;

-- name: GetBucket :one
SELECT * FROM s3_buckets WHERE id = ?;

-- name: ListBuckets :many
SELECT * FROM s3_buckets ORDER BY name;

-- name: DeleteBucket :execrows
DELETE FROM s3_buckets WHERE id = ?;

-- name: InsertBucketWithID :exec
INSERT INTO s3_buckets (id, name, folder_id, created_at, public, cors, lifecycle) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: SetBucketPublic :execrows
UPDATE s3_buckets SET public = ? WHERE id = ?;

-- name: SetBucketCORS :execrows
UPDATE s3_buckets SET cors = ? WHERE id = ?;

-- name: SetBucketLifecycle :execrows
UPDATE s3_buckets SET lifecycle = ? WHERE id = ?;

-- name: ListLifecycleBuckets :many
SELECT * FROM s3_buckets WHERE lifecycle <> '' ORDER BY id;

-- name: CreateKey :one
INSERT INTO s3_keys (access_key_id, enc_secret, name, permission, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetKeyByAccessKeyID :one
SELECT * FROM s3_keys WHERE access_key_id = ?;

-- name: ListKeys :many
SELECT k.*, CAST(COALESCE(u.email, '') AS TEXT) AS created_by_email
FROM s3_keys k LEFT JOIN users u ON u.id = k.created_by
ORDER BY k.created_at DESC, k.id DESC;

-- name: DeleteKey :execrows
DELETE FROM s3_keys WHERE id = ?;

-- name: SetKeySecret :execrows
UPDATE s3_keys SET enc_secret = ? WHERE id = ?;

-- name: TouchKey :exec
UPDATE s3_keys SET last_used_at = ? WHERE id = ? AND last_used_at < ?;

-- name: AddKeyBucket :exec
INSERT INTO s3_key_buckets (key_id, bucket_id) VALUES (?, ?);

-- name: ListKeyBuckets :many
SELECT bucket_id FROM s3_key_buckets WHERE key_id = ? ORDER BY bucket_id;

-- name: ListAllKeyBuckets :many
SELECT * FROM s3_key_buckets ORDER BY key_id, bucket_id;

-- name: ListKeysForManifest :many
SELECT id, access_key_id, enc_secret, name, permission, created_by, created_at FROM s3_keys ORDER BY id;

-- name: InsertKeyWithID :exec
INSERT INTO s3_keys (id, access_key_id, enc_secret, name, permission, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: CreateUpload :exec
INSERT INTO s3_uploads (id, bucket_id, object_key, created_at, mtime, meta) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetUpload :one
SELECT * FROM s3_uploads WHERE id = ?;

-- name: DeleteUpload :exec
DELETE FROM s3_uploads WHERE id = ?;

-- name: ListUploads :many
SELECT * FROM s3_uploads WHERE bucket_id = ? ORDER BY object_key, created_at;

-- name: ListStaleUploads :many
SELECT id FROM s3_uploads WHERE created_at < ?;

-- name: ListBucketStaleUploads :many
SELECT id, object_key FROM s3_uploads WHERE bucket_id = ? AND created_at < ?;

-- name: GetUploadPart :one
SELECT * FROM s3_upload_parts WHERE upload_id = ? AND part_number = ?;

-- name: PutUploadPart :exec
INSERT INTO s3_upload_parts (upload_id, part_number, file_id, size, etag, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (upload_id, part_number) DO UPDATE SET file_id = excluded.file_id, size = excluded.size,
    etag = excluded.etag, created_at = excluded.created_at;

-- name: ListUploadParts :many
SELECT * FROM s3_upload_parts WHERE upload_id = ? ORDER BY part_number;

-- name: CountBucketUploads :one
SELECT COUNT(*) FROM s3_uploads WHERE bucket_id = ?;
