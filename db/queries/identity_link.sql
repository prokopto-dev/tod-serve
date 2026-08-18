-- identity_link - append-only. No UPDATE and no DELETE, ever: revoking a membership revokes it
-- across the whole link set, and a link that could be quietly removed would reopen the second door
-- it exists to close.

-- name: CreateIdentityLink :one
INSERT INTO identity_link (
  id, primary_identity_id, linked_identity_id, method, linked_by_membership_id, linked_at
) VALUES (
  sqlc.arg(id), sqlc.arg(primary_identity_id), sqlc.arg(linked_identity_id), sqlc.arg(method),
  sqlc.arg(linked_by_membership_id), sqlc.arg(linked_at)
)
RETURNING *;

-- name: ListIdentityLinksFor :many
SELECT * FROM identity_link
WHERE primary_identity_id = sqlc.arg(identity_id) OR linked_identity_id = sqlc.arg(identity_id)
ORDER BY id;
