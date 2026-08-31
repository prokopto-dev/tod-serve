-- instance_setting_change - APPEND-ONLY, hash-chained. No UPDATE and no DELETE: a correction is a
-- new row, because this is the only record that an instance-wide policy was ever different.
--
-- It exists because audit_log.circle_id is NOT NULL and an instance policy belongs to no circle,
-- which is the wall ADR-0012 hit for instance_grant and the same answer: the ledger is its own
-- audit record. instance_grant could not hold these rows either - it is keyed on
-- (identity, permission) and answers "who may do what", not "what did somebody change".

-- name: AppendInstanceSettingChange :one
INSERT INTO instance_setting_change (
  id, setting, old_value, new_value, changed_by_identity_id, reason, prev_hash, hash, changed_at
) VALUES (
  sqlc.arg(id), sqlc.arg(setting), sqlc.arg(old_value), sqlc.arg(new_value),
  sqlc.narg(changed_by_identity_id), sqlc.arg(reason),
  sqlc.narg(prev_hash), sqlc.arg(hash), sqlc.arg(changed_at)
)
RETURNING *;

-- name: ListInstanceSettingChangeChainTail :many
-- The tail of the hash chain, so the next row can name its predecessor: the row whose hash no
-- other row claims as its `prev_hash`. One chain for the whole table, because an instance setting
-- belongs to no circle and there is nothing to partition it by.
--
-- IT IS DERIVED FROM THE CHAIN AND NEVER FROM `ORDER BY id`, for the reason spelled out in
-- instance_grant.sql: a ULID is monotonic within one generator, two writers inside one millisecond
-- can mint out of order, and an id-ordered head reuses a `prev_hash` that is already claimed -
-- which the unique index then refuses forever.
--
-- MANY rather than one: a `:one` query is a QueryRow, which scans the first row and discards the
-- rest, so a forked chain would be resolved by silently picking a branch. The caller asserts the
-- count instead.
SELECT * FROM instance_setting_change AS c
WHERE NOT EXISTS (
  SELECT 1 FROM instance_setting_change AS s WHERE s.prev_hash = c.hash
);

-- name: InstanceSettingChangeExists :one
-- Whether the ledger holds any row at all, so an empty chain tail can be told apart from a chain
-- with no tail. The first is the first append; the second is a cycle, which needs a hand-written
-- INSERT and must not be answered by starting a second chain beside the one already there.
SELECT EXISTS (SELECT 1 FROM instance_setting_change) AS any_row;

-- name: ListInstanceSettingChanges :many
-- Every change ever recorded, NEWEST first: this is what an administrator reads to answer "who
-- turned this on". Nothing prunes it.
--
-- Ordered by `changed_at` and only then by id, because id order is per-generator: two writers
-- inside one millisecond mint from random entropy, and ordering by id alone would report them in
-- the wrong order. `changed_at` is Micros, so the tie-break is reached only for two changes in the
-- same MICROsecond, where the ledger genuinely does not know which came first.
SELECT * FROM instance_setting_change ORDER BY changed_at DESC, id DESC;
