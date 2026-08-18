-- invite_redemption - append-only: who redeemed what, when. No UPDATE, no DELETE.

-- name: CreateInviteRedemption :one
INSERT INTO invite_redemption (id, circle_id, invite_id, membership_id, identity_id, created_at)
VALUES (
  sqlc.arg(id), sqlc.arg(circle_id), sqlc.arg(invite_id), sqlc.arg(membership_id),
  sqlc.narg(identity_id), sqlc.arg(created_at)
)
RETURNING *;

-- name: ListInviteRedemptions :many
SELECT * FROM invite_redemption
WHERE circle_id = sqlc.arg(circle_id) AND invite_id = sqlc.arg(invite_id)
ORDER BY id;
