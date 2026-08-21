-- audit_log - APPEND-ONLY, hash-chained. No UPDATE and no DELETE: a mutable audit log is a
-- decoration.

-- name: AppendAuditLog :one
INSERT INTO audit_log (
  id, circle_id, actor_membership_id, action, entity_type, entity_id, detail_json,
  prev_hash, hash, created_at
) VALUES (
  sqlc.arg(id), sqlc.arg(circle_id), sqlc.narg(actor_membership_id), sqlc.arg(action),
  sqlc.arg(entity_type), sqlc.narg(entity_id), sqlc.arg(detail_json), sqlc.narg(prev_hash),
  sqlc.arg(hash), sqlc.arg(created_at)
)
RETURNING *;

-- name: ListAuditLog :many
-- Newest first. An empty cursor is the first page: a caller should not have to know a sentinel id
-- that sorts above every ULID in order to read the beginning of a collection.
SELECT * FROM audit_log
WHERE circle_id = sqlc.arg(circle_id)
  AND (CAST(sqlc.arg(after_id) AS TEXT) = '' OR id < sqlc.arg(after_id))
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetLatestAuditLogEntry :one
-- The tail of the hash chain, so the next entry can name its predecessor.
SELECT * FROM audit_log WHERE circle_id = sqlc.arg(circle_id) ORDER BY id DESC LIMIT 1;
