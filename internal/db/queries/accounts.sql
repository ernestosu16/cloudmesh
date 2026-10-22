-- name: CreateAccount :one
INSERT INTO accounts (provider, label, email, enc_credentials, status, root_remote_id, quota_used, quota_total, last_sync_at, created_at, foreign_used)
VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY priority, id;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = ?;

-- name: UpdateAccountSettings :one
UPDATE accounts SET label = ?, priority = ?, enabled = ?, contrib_mode = ?, contrib_value = ? WHERE id = ?
RETURNING *;

-- name: UpdateAccountQuota :exec
UPDATE accounts
SET quota_used = ?, foreign_used = ?, quota_total = ?, last_sync_at = ?, status = 'active', last_error = ''
WHERE id = ?;

-- name: UpdateAccountCredentials :exec
UPDATE accounts
SET enc_credentials = ?, root_remote_id = ?, status = 'active', last_error = ''
WHERE id = ?;

-- name: SetAccountStatus :exec
UPDATE accounts SET status = ?, last_error = ? WHERE id = ?;

-- name: AccountChunkBytes :many
SELECT account_id, CAST(COALESCE(SUM(size), 0) AS INTEGER) AS used
FROM chunks WHERE account_id IS NOT NULL AND missing = 0
GROUP BY account_id;

-- name: DeleteAccount :execrows
DELETE FROM accounts WHERE id = ?;

-- name: UpdateAccountRoot :exec
UPDATE accounts SET root_remote_id = ? WHERE id = ?;

-- name: TouchAccountSync :exec
UPDATE accounts SET last_sync_at = ? WHERE id = ?;

-- name: SetAccountManifest :exec
UPDATE accounts SET manifest_remote_id = ? WHERE id = ?;

-- name: InsertAccountWithID :exec
INSERT INTO accounts (id, provider, label, email, enc_credentials, status, enabled, priority, quota_used, quota_total, root_remote_id, last_error, last_sync_at, created_at, contrib_mode, contrib_value)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
