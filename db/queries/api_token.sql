-- api_token - opaque PATs bound to a membership (ADR-0005). The 8-character prefix is loggable and
-- is how a leaked token is found; the hash never leaves the database and the secret is never
-- stored at all.

-- name: CreateAPIToken :one
INSERT INTO api_token (
  id, membership_id, token_prefix, token_hash, name, scopes_json, expires_at,
  created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(membership_id), sqlc.arg(token_prefix), sqlc.arg(token_hash),
  sqlc.arg(name), sqlc.arg(scopes_json), sqlc.narg(expires_at),
  sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetAPITokenByHash :one
SELECT * FROM api_token WHERE token_hash = sqlc.arg(token_hash);

-- name: ListAPITokensForMembership :many
SELECT * FROM api_token WHERE membership_id = sqlc.arg(membership_id) ORDER BY id DESC;

-- name: RevokeAPIToken :one
UPDATE api_token
SET revoked_at = sqlc.arg(revoked_at),
    revoked_by_membership_id = sqlc.arg(revoked_by_membership_id),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND membership_id = sqlc.arg(membership_id) AND revoked_at IS NULL
RETURNING *;

-- name: TouchAPIToken :exec
UPDATE api_token
SET last_used_at = sqlc.arg(last_used_at), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);
