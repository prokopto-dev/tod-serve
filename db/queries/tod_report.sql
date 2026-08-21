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
-- The report log, newest first. The id is a ULID, so it is also the cursor: time-ordered, opaque
-- and free, and an empty cursor is the first page rather than a sentinel a caller has to know.
--
-- `retracted` is computed rather than stored, because the retraction is a SEPARATE ROW and the
-- original is never touched. `include_retracted = 0` hides a retracted kill AND every retraction
-- row: a retraction the caller can see pointing at a report they cannot is a dangling reference
-- the client would have to explain.
SELECT r.*,
       EXISTS (SELECT 1 FROM tod_report x
               WHERE x.circle_id = r.circle_id AND x.retracts_report_id = r.id) AS retracted
FROM tod_report r
WHERE r.circle_id = sqlc.arg(circle_id)
  AND (CAST(sqlc.arg(after_id) AS TEXT) = '' OR r.id < sqlc.arg(after_id))
  AND (CAST(sqlc.narg(target_id) AS TEXT) IS NULL OR r.target_id = sqlc.narg(target_id))
  AND (CAST(sqlc.narg(died_after) AS INTEGER) IS NULL OR r.died_at >= sqlc.narg(died_after))
  AND (CAST(sqlc.narg(died_before) AS INTEGER) IS NULL OR r.died_at <= sqlc.narg(died_before))
  AND (CAST(sqlc.narg(reporter_membership_id) AS TEXT) IS NULL
       OR r.reporter_membership_id = sqlc.narg(reporter_membership_id))
  AND (CAST(sqlc.arg(include_retracted) AS INTEGER) = 1
       OR (r.kind = 'kill'
           AND NOT EXISTS (SELECT 1 FROM tod_report x
                           WHERE x.circle_id = r.circle_id AND x.retracts_report_id = r.id)))
ORDER BY r.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListTodReportsForCircle :many
-- Every report in the circle, grouped by target, for `tod-serve rebuild-states` and the nightly
-- verify job. One query rather than one per target: a rebuild that issued a query per target would
-- be the slowest thing the binary does, and it runs on a schedule nobody watches.
SELECT * FROM tod_report
WHERE circle_id = sqlc.arg(circle_id)
ORDER BY target_id, died_at, id;

-- name: ListTodReportsForTarget :many
-- Everything the derivation needs for one target, including retractions: consensus folds them, the
-- store does not, because a store that dropped rows would decide what counts as evidence.
SELECT * FROM tod_report
WHERE circle_id = sqlc.arg(circle_id) AND target_id = sqlc.arg(target_id)
ORDER BY died_at, id;

-- name: GetRetractionForReport :one
SELECT * FROM tod_report
WHERE circle_id = sqlc.arg(circle_id) AND retracts_report_id = sqlc.arg(retracts_report_id);

-- name: ListTodReportTargets :many
-- Every target this circle has reported anything about. It is what makes a read-miss rebuild
-- bounded by the circle's activity rather than by the catalogue: a target nobody has reported has
-- no cluster to derive and nothing worth a cache row, so the board answers it without a read.
SELECT DISTINCT target_id FROM tod_report
WHERE circle_id = sqlc.arg(circle_id)
ORDER BY target_id;
