-- tod_report - APPEND-ONLY, the core log. There is no UPDATE and no DELETE here, in Go, in SQL or
-- in a migration: corrections are new rows and a retraction is a row with retracts_report_id set.
-- The triggers abort anything else.

-- name: CreateTodReport :one
INSERT INTO tod_report (
  id, circle_id, target_id, kind, died_at, reported_at, reporter_membership_id, source,
  self_confidence, source_line, source_character, log_character, killed_by_guild,
  client_clock_offset_seconds, retracts_report_id
) VALUES (
  sqlc.arg(id), sqlc.arg(circle_id), sqlc.arg(target_id), sqlc.arg(kind), sqlc.arg(died_at),
  sqlc.arg(reported_at), sqlc.arg(reporter_membership_id), sqlc.arg(source),
  sqlc.arg(self_confidence), sqlc.narg(source_line), sqlc.narg(source_character),
  sqlc.narg(log_character), sqlc.narg(killed_by_guild), sqlc.narg(client_clock_offset_seconds),
  sqlc.narg(retracts_report_id)
)
RETURNING *;

-- name: GetTodReport :one
SELECT * FROM tod_report WHERE circle_id = sqlc.arg(circle_id) AND id = sqlc.arg(id);

-- name: GetTodReportByNaturalKey :one
-- The second line of defence behind Idempotency-Key: the same reporter cannot lodge the same kill
-- twice even if the header is botched. A duplicate is a replay - 200 with the existing report.
SELECT * FROM tod_report
WHERE circle_id = sqlc.arg(circle_id)
  AND target_id = sqlc.arg(target_id)
  AND reporter_membership_id = sqlc.arg(reporter_membership_id)
  AND died_at = sqlc.arg(died_at)
  AND kind = 'kill';

-- name: ListTodReports :many
-- The id is a ULID, so it is also the cursor: time-ordered, opaque, and free.
SELECT * FROM tod_report
WHERE circle_id = sqlc.arg(circle_id) AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: ListTodReportsForTarget :many
-- Everything the derivation needs for one target, including retractions: consensus folds them, the
-- store does not, because a store that dropped rows would decide what counts as evidence.
SELECT * FROM tod_report
WHERE circle_id = sqlc.arg(circle_id) AND target_id = sqlc.arg(target_id)
ORDER BY died_at, id;

-- name: GetRetractionForReport :one
SELECT * FROM tod_report
WHERE circle_id = sqlc.arg(circle_id) AND retracts_report_id = sqlc.arg(retracts_report_id);
