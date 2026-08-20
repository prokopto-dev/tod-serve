-- membership - (circle, identity) -> role and revocation.
--
-- There is NO delete-membership query, at any permission level, and there never will be: the
-- partial unique index is the entire revocation mechanism, and a delete-then-insert path would let
-- a revoked person rejoin as a fresh row.

-- name: CreateMembership :one
INSERT INTO membership (
  id, circle_id, identity_id, kind, owner_membership_id, display_name, display_name_norm,
  role, admitted_by_invite_id, joined_at, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(circle_id), sqlc.narg(identity_id), sqlc.arg(kind),
  sqlc.narg(owner_membership_id), sqlc.arg(display_name), sqlc.arg(display_name_norm),
  sqlc.arg(role), sqlc.narg(admitted_by_invite_id), sqlc.arg(joined_at),
  sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetMembership :one
SELECT * FROM membership WHERE circle_id = sqlc.arg(circle_id) AND id = sqlc.arg(id);

-- name: GetMembershipByID :one
-- tenancy: keyed on the membership a verified credential is already bound to, and this lookup is
-- what RESOLVES the caller's circle -- so it cannot also filter by one. A PAT carries a membership
-- id and nothing else (ADR-0005); every circle-scoped query downstream of this one is filtered by
-- the circle_id this row returns, which is the only reading of the tenancy rule that terminates.
--
-- It carries the circle's deleted_at because this is the one read on EVERY request. Membership
-- state is checked here rather than by cascading at revocation time, and a deleted circle is the
-- same kind of fact: without it, the members of a deleted circle would keep acting in it until
-- their tokens expired, which is the cascade-and-forget failure ADR-0005 exists to avoid.
SELECT m.*, c.deleted_at AS circle_deleted_at
FROM membership m
JOIN circle c ON c.id = m.circle_id
WHERE m.id = sqlc.arg(id);

-- name: GetMembershipByIdentity :one
SELECT * FROM membership
WHERE circle_id = sqlc.arg(circle_id) AND identity_id = sqlc.arg(identity_id);

-- name: ListMemberships :many
SELECT * FROM membership WHERE circle_id = sqlc.arg(circle_id) ORDER BY id;

-- name: ListMembershipsForIdentity :many
-- tenancy: keyed on a VERIFIED identity, never on a caller-supplied circle id. The OAuth callback
-- uses it to decide which guilds need facts, and it must see every circle the identity is in.
SELECT * FROM membership WHERE identity_id = sqlc.arg(identity_id) ORDER BY circle_id;

-- name: UpdateMembership :one
UPDATE membership
SET display_name = sqlc.arg(display_name), display_name_norm = sqlc.arg(display_name_norm),
    role = sqlc.arg(role), updated_at = sqlc.arg(updated_at)
WHERE circle_id = sqlc.arg(circle_id) AND id = sqlc.arg(id)
RETURNING *;

-- name: RevokeMembership :one
UPDATE membership
SET revoked_at = sqlc.arg(revoked_at),
    revoked_by_membership_id = sqlc.arg(revoked_by_membership_id),
    revoke_reason = sqlc.narg(revoke_reason),
    updated_at = sqlc.arg(updated_at)
WHERE circle_id = sqlc.arg(circle_id) AND id = sqlc.arg(id) AND revoked_at IS NULL
RETURNING *;

-- name: ReinstateMembership :one
-- Explicit and audited, requiring member.revoke. Reinstatement is the only way back in, which is
-- what makes "there is no second membership row" a rule rather than an inconvenience.
UPDATE membership
SET revoked_at = NULL, revoked_by_membership_id = NULL, revoke_reason = NULL,
    updated_at = sqlc.arg(updated_at)
WHERE circle_id = sqlc.arg(circle_id) AND id = sqlc.arg(id)
RETURNING *;
