-- instance_grant - APPEND-ONLY, hash-chained. No UPDATE and no DELETE: a revocation is a new row
-- naming the row it supersedes, so the decision to take a permission away is as durable as the
-- decision to give it. ADR-0012.

-- name: AppendInstanceGrant :one
INSERT INTO instance_grant (
  id, identity_id, permission, decision, supersedes_id, decided_by_identity_id, reason,
  prev_hash, hash, decided_at
) VALUES (
  sqlc.arg(id), sqlc.arg(identity_id), sqlc.arg(permission), sqlc.arg(decision),
  sqlc.narg(supersedes_id), sqlc.narg(decided_by_identity_id), sqlc.arg(reason),
  sqlc.narg(prev_hash), sqlc.arg(hash), sqlc.arg(decided_at)
)
RETURNING *;

-- name: GetLatestInstanceGrant :one
-- The tail of the hash chain, so the next decision can name its predecessor. One chain for the
-- whole table: an instance grant belongs to no circle, so there is nothing to partition it by.
SELECT * FROM instance_grant ORDER BY id DESC LIMIT 1;

-- name: GetInstanceGrantDecision :one
-- The CURRENT decision for one identity and one permission: the row nothing supersedes.
--
-- ux_instance_grant_supersedes and ux_instance_grant_head make that row unique by construction, so
-- this needs no ORDER BY and no tie-break. A LIMIT here would hide a forked chain rather than let
-- the caller find out about it.
SELECT * FROM instance_grant AS g
WHERE g.identity_id = sqlc.arg(identity_id)
  AND g.permission = sqlc.arg(permission)
  AND NOT EXISTS (SELECT 1 FROM instance_grant AS s WHERE s.supersedes_id = g.id);

-- name: ListInstanceGrantDecisionsForIdentity :many
-- Every current decision for one identity, granted and revoked alike. The caller keeps the granted
-- ones; filtering here would put the value of instance_grant.decision in two places.
SELECT * FROM instance_grant AS g
WHERE g.identity_id = sqlc.arg(identity_id)
  AND NOT EXISTS (SELECT 1 FROM instance_grant AS s WHERE s.supersedes_id = g.id)
ORDER BY g.permission;

-- name: ListInstanceGrantDecisions :many
-- Every current decision on the instance, for the console listing. Ordered by identity then
-- permission so two runs of `tod-serve instance grants` diff cleanly.
SELECT * FROM instance_grant AS g
WHERE NOT EXISTS (SELECT 1 FROM instance_grant AS s WHERE s.supersedes_id = g.id)
ORDER BY g.identity_id, g.permission;

-- name: ListInstanceGrantHistory :many
-- Every decision ever recorded, oldest first: the audit read. Nothing prunes this.
SELECT * FROM instance_grant ORDER BY id;
