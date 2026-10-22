-- name: CreateUser :one
INSERT INTO users (email, password_hash, created_at, role, quota_bytes)
VALUES (?, ?, ?, ?, ?)
RETURNING id, email, name, created_at, role, quota_bytes;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ?;

-- name: GetUserByID :one
SELECT id, email, created_at FROM users WHERE id = ?;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: ListUsers :many
SELECT * FROM users ORDER BY id;

-- name: InsertUserWithID :exec
INSERT INTO users (id, email, password_hash, name, created_at, totp_secret, totp_enabled, totp_last_step, role, quota_bytes, recovery_codes)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = ?;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ? WHERE id = ?;

-- name: GetUserWithHash :one
SELECT * FROM users WHERE id = ?;

-- name: UpdateUserName :one
UPDATE users SET name = ? WHERE id = ?
RETURNING id, email, name, created_at, totp_enabled, role, quota_bytes;

-- name: SetUserTOTP :execrows
UPDATE users SET totp_secret = ?, totp_enabled = ?, totp_last_step = 0, recovery_codes = '[]' WHERE id = ?;

-- name: SetRecoveryCodes :execrows
UPDATE users SET recovery_codes = ? WHERE id = ? AND totp_enabled = 1;

-- name: UseRecoveryCode :execrows
UPDATE users SET recovery_codes = ? WHERE id = ? AND recovery_codes = ?;

-- name: EnableUserTOTP :execrows
UPDATE users SET totp_enabled = 1 WHERE id = ? AND length(totp_secret) > 0;

-- name: UseTOTPStep :execrows
UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?;

-- name: GetUserIDByEmail :one
SELECT id FROM users WHERE email = ?;

-- name: UpdateUserAccess :one
UPDATE users SET role = ?, quota_bytes = ? WHERE id = ?
RETURNING id, email, name, created_at, totp_enabled, role, quota_bytes;

-- name: CountAdmins :one
SELECT COUNT(*) FROM users WHERE role = 'admin';

-- name: UsageByOwner :many
SELECT CAST(owner_id AS INTEGER) AS owner_id, CAST(COALESCE(SUM(size), 0) AS INTEGER) AS used
FROM files WHERE owner_id IS NOT NULL AND layout <> 'parts'
GROUP BY owner_id;

-- name: OwnerUsage :one
SELECT CAST(COALESCE(SUM(size), 0) AS INTEGER) FROM files WHERE owner_id = ? AND layout <> 'parts';
