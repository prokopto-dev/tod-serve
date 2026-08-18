-- target_state_cache - materialised consensus, and NEVER authority. Every row here is droppable;
-- if you find yourself reading it to make a decision the derivation should make, that is the bug.

-- name: PutTargetState :one
INSERT INTO target_state_cache (
  circle_id, target_id, computed_at, latest_report_id, report_count, status, confidence,
  contested, contest_reason, change_reason, died_at, window_open_at, window_close_at, spawn_at,
  distinct_reporter_count, log_line_count, spread_seconds, revoked_reporter_count,
  created_at, updated_at
) VALUES (
  sqlc.arg(circle_id), sqlc.arg(target_id), sqlc.arg(computed_at), sqlc.narg(latest_report_id),
  sqlc.arg(report_count), sqlc.arg(status), sqlc.arg(confidence), sqlc.arg(contested),
  sqlc.narg(contest_reason), sqlc.narg(change_reason), sqlc.narg(died_at),
  sqlc.narg(window_open_at), sqlc.narg(window_close_at), sqlc.narg(spawn_at),
  sqlc.arg(distinct_reporter_count), sqlc.arg(log_line_count), sqlc.narg(spread_seconds),
  sqlc.arg(revoked_reporter_count), sqlc.arg(created_at), sqlc.arg(updated_at)
)
ON CONFLICT (circle_id, target_id) DO UPDATE SET
  computed_at = excluded.computed_at,
  latest_report_id = excluded.latest_report_id,
  report_count = excluded.report_count,
  status = excluded.status,
  confidence = excluded.confidence,
  contested = excluded.contested,
  contest_reason = excluded.contest_reason,
  change_reason = excluded.change_reason,
  died_at = excluded.died_at,
  window_open_at = excluded.window_open_at,
  window_close_at = excluded.window_close_at,
  spawn_at = excluded.spawn_at,
  distinct_reporter_count = excluded.distinct_reporter_count,
  log_line_count = excluded.log_line_count,
  spread_seconds = excluded.spread_seconds,
  revoked_reporter_count = excluded.revoked_reporter_count,
  updated_at = excluded.updated_at
RETURNING *;

-- name: GetTargetState :one
SELECT * FROM target_state_cache
WHERE circle_id = sqlc.arg(circle_id) AND target_id = sqlc.arg(target_id);

-- name: ListTargetStates :many
SELECT * FROM target_state_cache
WHERE circle_id = sqlc.arg(circle_id)
ORDER BY window_open_at, target_id;

-- name: InvalidateTargetState :execrows
-- Invalidation is a DELETE, not a flag: a stale row with a "stale" bit is still a row somebody
-- reads by accident, and this table is droppable by construction.
DELETE FROM target_state_cache
WHERE circle_id = sqlc.arg(circle_id) AND target_id = sqlc.arg(target_id);

-- name: InvalidateCircleTargetStates :execrows
-- What a quake, a timer change or `tod-serve rebuild-states` does to a whole circle.
DELETE FROM target_state_cache WHERE circle_id = sqlc.arg(circle_id);
