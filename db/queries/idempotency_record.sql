-- idempotency_record - (principal, key) -> the request that was made and the response returned.
-- The principal is the MEMBERSHIP, never the token, so a rotation mid-retry still replays.

-- name: CreateIdempotencyRecord :one
INSERT INTO idempotency_record (
  id, principal_membership_id, key, request_hash, expires_at, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(principal_membership_id), sqlc.arg(key), sqlc.arg(request_hash),
  sqlc.arg(expires_at), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetIdempotencyRecord :one
SELECT * FROM idempotency_record
WHERE principal_membership_id = sqlc.arg(principal_membership_id) AND key = sqlc.arg(key);

-- name: CompleteIdempotencyRecord :one
UPDATE idempotency_record
SET response_status = sqlc.arg(response_status),
    response_body = sqlc.narg(response_body),
    completed_at = sqlc.arg(completed_at),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND completed_at IS NULL
RETURNING *;

-- name: DeleteExpiredIdempotencyRecords :execrows
DELETE FROM idempotency_record WHERE expires_at < sqlc.arg(before);
