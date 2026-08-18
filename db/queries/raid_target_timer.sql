-- raid_target_timer - the per-server respawn window. These numbers do not ship; they load from the
-- separate tod-serve-p99-seed repository, and an unseeded instance reports no_timer.

-- name: PutRaidTargetTimer :one
INSERT INTO raid_target_timer (
  target_id, server, window_kind, window_open_offset_seconds, window_close_offset_seconds,
  fixed_grace_seconds, cluster_epsilon_seconds, source, note, created_at, updated_at
) VALUES (
  sqlc.arg(target_id), sqlc.arg(server), sqlc.arg(window_kind),
  sqlc.narg(window_open_offset_seconds), sqlc.narg(window_close_offset_seconds),
  sqlc.arg(fixed_grace_seconds), sqlc.narg(cluster_epsilon_seconds), sqlc.narg(source),
  sqlc.arg(note), sqlc.arg(created_at), sqlc.arg(updated_at)
)
ON CONFLICT (target_id, server) DO UPDATE SET
  window_kind = excluded.window_kind,
  window_open_offset_seconds = excluded.window_open_offset_seconds,
  window_close_offset_seconds = excluded.window_close_offset_seconds,
  fixed_grace_seconds = excluded.fixed_grace_seconds,
  cluster_epsilon_seconds = excluded.cluster_epsilon_seconds,
  source = excluded.source,
  note = excluded.note,
  updated_at = excluded.updated_at
RETURNING *;

-- name: GetRaidTargetTimer :one
SELECT * FROM raid_target_timer
WHERE target_id = sqlc.arg(target_id) AND server = sqlc.arg(server);

-- name: ListRaidTargetTimersForServer :many
SELECT * FROM raid_target_timer WHERE server = sqlc.arg(server) ORDER BY target_id;
