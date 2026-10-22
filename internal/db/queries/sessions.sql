-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, expires_at, created_at)
VALUES (?, ?, ?, ?);

-- name: GetSessionUser :one
SELECT s.id AS session_id, s.expires_at, u.id AS user_id, u.email, u.name, u.created_at, u.totp_enabled, u.role, u.quota_bytes,
    CAST(json_array_length(u.recovery_codes) AS INTEGER) AS recovery_left
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.id = ? AND s.expires_at > ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= ?;

-- name: CountUserSessions :one
SELECT COUNT(*) FROM sessions WHERE user_id = ? AND expires_at > ?;

-- name: DeleteOtherSessions :exec
DELETE FROM sessions WHERE user_id = ? AND id <> ?;
