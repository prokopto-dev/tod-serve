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
-- A tombstoned circle does not exist to a reader. deleteCircle cannot remove the row -- the
-- append-only tables referencing it forbid that -- so "deleted" is a predicate every read carries
-- rather than an absence the database enforces.
SELECT * FROM circle WHERE id = sqlc.arg(circle_id) AND deleted_at IS NULL;

-- name: SoftDeleteCircle :one
-- The tombstone deleteCircle writes. `deleted_at IS NULL` makes a second delete return no row
-- rather than moving the timestamp, so the moment a circle stopped existing is the first one.
UPDATE circle
SET deleted_at = sqlc.arg(deleted_at), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(circle_id) AND deleted_at IS NULL
RETURNING *;

-- name: ListCirclesForIdentity :many
-- tenancy: keyed on a VERIFIED identity's own memberships, never on a caller-supplied circle id.
-- This is the lookup the OAuth callback makes at step 4 and the one listCircles serves; both take
-- the identity from something the caller proved, so there is no id here to enumerate.
SELECT c.* FROM circle c
JOIN membership m ON m.circle_id = c.id
WHERE m.identity_id = sqlc.arg(identity_id) AND m.revoked_at IS NULL AND c.deleted_at IS NULL
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

-- name: ListLiveCircles :many
-- tenancy: SAFE because it takes no caller-supplied input of any kind and its result never reaches
-- a response body. It backs `tod-serve rebuild-states` and the nightly verify job, which have no
-- caller and no tenant: a maintenance sweep that could only see one circle would leave every other
-- circle's cache unverified, which is the whole thing the job exists to stop.
--
-- THIS QUERY MUST NEVER BACK A CALLER-FACING ROUTE. The moment it does it is an instance-wide
-- circle enumeration -- the existence oracle canonical section 7 exists to close, because a
-- circle's existence is part of what it is hiding. That is not left to this comment:
-- TestCircleEnumeration_IsReachableOnlyFromTheProjection is the gate.
--
-- `deleted_at IS NULL` like every other read here: a tombstoned circle must not come back onto the
-- recompute path.
SELECT * FROM circle WHERE deleted_at IS NULL ORDER BY id;

-- name: ListLiveCirclesOnServer :many
-- tenancy: SAFE for the same reason as ListLiveCircles above, and under the same constraint. Its
-- only argument is a server, which is chosen by an instance-realm write rather than by a caller
-- naming a circle, and its result never reaches a response body -- it is an internal invalidation
-- sweep. A catalogue timer is instance-wide and per server, so writing one moves the window for
-- every circle pinned to that server, and the fan-out is the point.
--
-- THIS QUERY MUST NEVER BACK A CALLER-FACING ROUTE, and
-- TestCircleEnumeration_IsReachableOnlyFromTheProjection is what holds it to that rather than this
-- sentence. `deleted_at IS NULL` for the same reason as above.
SELECT * FROM circle
WHERE deleted_at IS NULL AND server = sqlc.arg(server)
ORDER BY id;
