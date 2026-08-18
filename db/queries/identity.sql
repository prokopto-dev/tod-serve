-- identity - a (provider, subject) pair, instance-wide.

-- name: CreateIdentity :one
INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
VALUES (
  sqlc.arg(id), sqlc.arg(provider_id), sqlc.arg(subject), sqlc.arg(display_name),
  sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetIdentity :one
SELECT * FROM identity WHERE id = sqlc.arg(id);

-- name: GetIdentityByProviderSubject :one
SELECT * FROM identity
WHERE provider_id = sqlc.arg(provider_id) AND subject = sqlc.arg(subject);

-- name: BlockIdentity :one
-- The instance operator's decision about their whole instance, refused at join AND at ticket
-- redemption, so a second circle is not a second door.
UPDATE identity
SET blocked_at = sqlc.arg(blocked_at),
    blocked_by_membership_id = sqlc.arg(blocked_by_membership_id),
    block_reason = sqlc.narg(block_reason),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UnblockIdentity :one
UPDATE identity
SET blocked_at = NULL, blocked_by_membership_id = NULL, block_reason = NULL,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING *;
