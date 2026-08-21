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

-- name: ListInstanceGrantChainTail :many
-- The tail of the hash chain, so the next decision can name its predecessor: the row whose hash no
-- other row claims as its `prev_hash`. One chain for the whole table, because an instance grant
-- belongs to no circle and there is nothing to partition it by.
--
-- IT IS DERIVED FROM THE CHAIN AND NEVER FROM `ORDER BY id`. A ULID is monotonic within one
-- generator and `tod-serve instance grant` builds a fresh one per invocation, so two invocations
-- inside one millisecond mint from random entropy and the later row can sort BELOW the earlier
-- one. An id-ordered head then keeps returning the earlier row forever, the next append reuses a
-- `prev_hash` that is already claimed, and `ux_instance_grant_chain` refuses it: the ledger becomes
-- permanently unappendable, which is an instance nobody can administer.
--
-- MANY rather than one, for the same reason GetInstanceGrantDecision is: a `:one` query is a
-- QueryRow, which scans the first row and discards the rest, so a forked chain would be resolved
-- by silently picking a branch. The caller asserts the count instead.
SELECT * FROM instance_grant AS g
WHERE NOT EXISTS (SELECT 1 FROM instance_grant AS s WHERE s.prev_hash = g.hash);

-- name: InstanceGrantExists :one
-- Whether the ledger holds any row at all, so an empty chain tail can be told apart from a chain
-- with no tail. The first is the first append; the second is a cycle, which needs a hand-written
-- INSERT and must not be answered by starting a second chain beside the one already there.
SELECT EXISTS (SELECT 1 FROM instance_grant) AS any_row;

-- name: GetInstanceGrantDecision :many
-- The CURRENT decision for one identity and one permission: the row nothing supersedes.
--
-- ux_instance_grant_supersedes and ux_instance_grant_head make that row unique by construction, so
-- this needs no ORDER BY and no tie-break. It returns MANY rather than one on purpose: a :one
-- query is a QueryRow, which scans the first row and discards the rest, so a forked chain would be
-- resolved by silently picking a branch. The caller asserts the count instead.
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
--
-- Ordered by `decided_at` and only then by id, because id order is per-generator: two console
-- invocations inside one millisecond mint from random entropy, and ordering by id alone would
-- report them in the wrong order. `decided_at` is Micros, so the tie-break is reached only for two
-- decisions in the same MICROsecond, where the ledger genuinely does not know which came first.
SELECT * FROM instance_grant ORDER BY decided_at, id;
