-- name: CreateInvitation :one
INSERT INTO invitations (token_hash, email, role, quota_bytes, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: DeletePendingInvitations :exec
DELETE FROM invitations WHERE email = ? AND accepted_at = 0;

-- name: GetInvitationByToken :one
SELECT * FROM invitations WHERE token_hash = ?;

-- name: ListPendingInvitations :many
SELECT i.*, CAST(COALESCE(u.email, '') AS TEXT) AS created_by_email
FROM invitations i LEFT JOIN users u ON u.id = i.created_by
WHERE i.accepted_at = 0 AND i.expires_at > ?
ORDER BY i.created_at DESC, i.id DESC;

-- name: AcceptInvitation :execrows
UPDATE invitations SET accepted_at = ? WHERE id = ? AND accepted_at = 0 AND expires_at > ?;

-- name: DeleteInvitation :execrows
DELETE FROM invitations WHERE id = ? AND accepted_at = 0;

-- name: DeleteOldInvitations :exec
DELETE FROM invitations WHERE expires_at < ? OR (accepted_at > 0 AND accepted_at < ?);

-- name: ReopenInvitation :exec
UPDATE invitations SET accepted_at = 0 WHERE id = ?;
