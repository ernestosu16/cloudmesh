-- name: CreateFolder :one
INSERT INTO folders (parent_id, name, created_at) VALUES (?, ?, ?)
RETURNING *;

-- name: GetFolder :one
SELECT * FROM folders WHERE id = ?;

-- name: GetFolderByName :one
SELECT * FROM folders WHERE parent_id = ? AND name = ?;

-- name: ListFolders :many
SELECT * FROM folders WHERE parent_id = ? ORDER BY name COLLATE NOCASE;

-- name: UpdateFolder :one
UPDATE folders SET name = ?, parent_id = ? WHERE id = ?
RETURNING *;

-- name: DeleteFolder :execrows
DELETE FROM folders WHERE id = ?;

-- name: FolderTree :many
WITH RECURSIVE tree(id) AS (
    SELECT folders.id FROM folders WHERE folders.id = @id
    UNION ALL
    SELECT f.id FROM folders f JOIN tree ON f.parent_id = tree.id
)
SELECT id FROM tree;

-- name: TrashFolderFiles :exec
-- Internal files stay: parts follow their composite and orphans are cleaned by S3.
UPDATE files SET deleted_at = @deleted_at
WHERE folder_id = @folder_id AND deleted_at = 0 AND internal = 0;

-- name: CountFolderContents :one
SELECT (SELECT COUNT(*) FROM folders WHERE parent_id = @id) + (SELECT COUNT(*) FROM files WHERE folder_id = @id AND deleted_at = 0) AS n;

-- name: SearchFolders :many
SELECT * FROM folders WHERE name LIKE '%' || @q || '%' ORDER BY name COLLATE NOCASE LIMIT 50;

-- name: CountRestrictedFolders :one
SELECT COUNT(*) FROM folders WHERE restricted = 1;

-- name: HiddenFolders :many
WITH RECURSIVE hidden(id) AS (
    SELECT f.id FROM folders f
    WHERE f.restricted = 1
      AND NOT EXISTS (SELECT 1 FROM folder_access a WHERE a.folder_id = f.id AND a.user_id = @user_id)
    UNION
    SELECT c.id FROM folders c JOIN hidden h ON c.parent_id = h.id
)
SELECT id FROM hidden;

-- name: ReadOnlyFolders :many
WITH RECURSIVE ro(id) AS (
    SELECT f.id FROM folders f
    WHERE f.restricted = 1
      AND NOT EXISTS (SELECT 1 FROM folder_access a WHERE a.folder_id = f.id AND a.user_id = @user_id AND a.level = 'write')
    UNION
    SELECT c.id FROM folders c JOIN ro r ON c.parent_id = r.id
)
SELECT id FROM ro;

-- name: SetFolderRestricted :execrows
UPDATE folders SET restricted = ? WHERE id = ?;

-- name: ListFolderAccess :many
SELECT a.user_id, a.level, u.email, u.name, u.role
FROM folder_access a JOIN users u ON u.id = a.user_id
WHERE a.folder_id = ? ORDER BY u.email;

-- name: ClearFolderAccess :exec
DELETE FROM folder_access WHERE folder_id = ?;

-- name: AddFolderAccess :exec
INSERT INTO folder_access (folder_id, user_id, level) VALUES (?, ?, ?);

-- name: ListAllFolderAccess :many
SELECT * FROM folder_access ORDER BY folder_id, user_id;

-- name: DeleteAllFolders :exec
DELETE FROM folders;

-- name: SubfolderSizes :many
-- Each folder below the children of parent_id, tagged with the child it hangs
-- from (top), whether a restricted folder lies on the way (closed) and the
-- bytes of its own files, counted like ListFilesInFolder (no trash, no parts,
-- nothing still uploading).
WITH RECURSIVE tree(top, id, closed) AS (
    SELECT folders.id, folders.id, folders.restricted FROM folders WHERE folders.parent_id = @parent_id
    UNION ALL
    SELECT tree.top, f.id, MAX(tree.closed, f.restricted) FROM folders f JOIN tree ON f.parent_id = tree.id
)
SELECT CAST(tree.top AS INTEGER) AS top, CAST(tree.id AS INTEGER) AS id, CAST(tree.closed AS INTEGER) AS closed,
    CAST(COALESCE((SELECT SUM(files.size) FROM files WHERE files.folder_id = tree.id AND files.status <> 'uploading' AND files.deleted_at = 0 AND files.internal = 0), 0) AS INTEGER) AS bytes
FROM tree;
