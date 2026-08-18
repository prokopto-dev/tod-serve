-- circle - the tenant root. Its own `id` IS the tenant key, so every query names it as
-- sqlc.arg(circle_id): the parameter a caller passes is a circle id, and spelling it that way
-- keeps the generated Go honest about what it is.
--
-- `server` is absent from UpdateCircle on purpose. It is immutable after creation - a BEFORE
-- UPDATE trigger enforces it and the edge answers 422 field_immutable - and a query that offered
-- to set it would be a second opinion about that.

-- name: CreateCircle :one
INSERT INTO circle (
  id, name, name_norm, description, server, timezone,
  min_reporters_to_supersede, revoke_invalidates_invites, state, created_at, updated_at
) VALUES (
  sqlc.arg(circle_id), sqlc.arg(name), sqlc.arg(name_norm), sqlc.arg(description),
  sqlc.arg(server), sqlc.arg(timezone), sqlc.arg(min_reporters_to_supersede),
  sqlc.arg(revoke_invalidates_invites), sqlc.arg(state), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetCircle :one
SELECT * FROM circle WHERE id = sqlc.arg(circle_id);

-- name: ListCirclesForIdentity :many
-- tenancy: keyed on a VERIFIED identity's own memberships, never on a caller-supplied circle id.
-- This is the lookup the OAuth callback makes at step 4 and the one listCircles serves; both take
-- the identity from something the caller proved, so there is no id here to enumerate.
SELECT c.* FROM circle c
JOIN membership m ON m.circle_id = c.id
WHERE m.identity_id = sqlc.arg(identity_id) AND m.revoked_at IS NULL
ORDER BY c.id;

-- name: UpdateCircle :one
UPDATE circle
SET name = sqlc.arg(name), name_norm = sqlc.arg(name_norm), description = sqlc.arg(description),
    timezone = sqlc.arg(timezone),
    min_reporters_to_supersede = sqlc.arg(min_reporters_to_supersede),
    revoke_invalidates_invites = sqlc.arg(revoke_invalidates_invites),
    state = sqlc.arg(state), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(circle_id)
RETURNING *;
