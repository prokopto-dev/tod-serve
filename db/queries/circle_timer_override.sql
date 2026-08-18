-- circle_timer_override - this circle disagrees with the catalogue. Resolution order is circle
-- override -> catalogue timer -> unknown.

-- name: PutCircleTimerOverride :one
INSERT INTO circle_timer_override (
  circle_id, target_id, window_kind, window_open_offset_seconds, window_close_offset_seconds,
  fixed_grace_seconds, cluster_epsilon_seconds, note, created_by_membership_id,
  created_at, updated_at
) VALUES (
  sqlc.arg(circle_id), sqlc.arg(target_id), sqlc.arg(window_kind),
  sqlc.narg(window_open_offset_seconds), sqlc.narg(window_close_offset_seconds),
  sqlc.arg(fixed_grace_seconds), sqlc.narg(cluster_epsilon_seconds), sqlc.arg(note),
  sqlc.arg(created_by_membership_id), sqlc.arg(created_at), sqlc.arg(updated_at)
)
ON CONFLICT (circle_id, target_id) DO UPDATE SET
  window_kind = excluded.window_kind,
  window_open_offset_seconds = excluded.window_open_offset_seconds,
  window_close_offset_seconds = excluded.window_close_offset_seconds,
  fixed_grace_seconds = excluded.fixed_grace_seconds,
  cluster_epsilon_seconds = excluded.cluster_epsilon_seconds,
  note = excluded.note,
  updated_at = excluded.updated_at
RETURNING *;

-- name: GetCircleTimerOverride :one
SELECT * FROM circle_timer_override
WHERE circle_id = sqlc.arg(circle_id) AND target_id = sqlc.arg(target_id);

-- name: ListCircleTimerOverrides :many
SELECT * FROM circle_timer_override WHERE circle_id = sqlc.arg(circle_id) ORDER BY target_id;

-- name: DeleteCircleTimerOverride :execrows
DELETE FROM circle_timer_override
WHERE circle_id = sqlc.arg(circle_id) AND target_id = sqlc.arg(target_id);
