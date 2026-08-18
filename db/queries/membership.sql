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
