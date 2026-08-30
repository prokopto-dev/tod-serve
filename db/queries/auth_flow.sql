-- auth_flow - one in-flight browser OAuth authorization. Rows are litter, not history: they are
-- swept on expiry, which is why this table has a DELETE and tod_report does not.

-- name: CreateAuthFlow :one
INSERT INTO auth_flow (
  id, state, pkce_verifier, provider_id, invite_code_hash, circle_id,
  expires_at, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(state), sqlc.arg(pkce_verifier), sqlc.arg(provider_id),
  sqlc.narg(invite_code_hash), sqlc.narg(circle_id), sqlc.arg(expires_at),
  sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetAuthFlowByState :one
-- tenancy: keyed on the unguessable server-minted `state`, never on a caller-supplied circle.
-- Looked up by the unguessable server-minted state, never by circle. That is why a missing
-- `WHERE circle_id = ?` cannot leak across tenants here (canonical section 9).
SELECT * FROM auth_flow WHERE state = sqlc.arg(state);

-- name: ConsumeAuthFlow :one
-- tenancy: keyed on the unguessable server-minted `state`, like the read above.
-- `consumed_at IS NULL` in the WHERE is the race-free half; the BEFORE UPDATE trigger is what
-- makes a second consumption unrepresentable even if a caller forgets it.
UPDATE auth_flow
SET consumed_at = sqlc.arg(consumed_at), updated_at = sqlc.arg(updated_at)
WHERE state = sqlc.arg(state) AND consumed_at IS NULL
RETURNING *;

-- name: DeleteExpiredAuthFlows :execrows
-- tenancy: the expiry sweeper. It is instance-wide by definition -- litter in every circle is
-- still litter -- and it selects on time alone.
DELETE FROM auth_flow WHERE expires_at < sqlc.arg(before);
