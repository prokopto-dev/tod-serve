-- quake_event - APPEND-ONLY. An earthquake repops every raid target on the server at once, and
-- modelling it as N kill reports would be a lie nobody observed.

-- name: CreateQuakeEvent :one
INSERT INTO quake_event (
  id, circle_id, occurred_at, reported_at, reported_by_membership_id, source, note
) VALUES (
  sqlc.arg(id), sqlc.arg(circle_id), sqlc.arg(occurred_at), sqlc.arg(reported_at),
  sqlc.arg(reported_by_membership_id), sqlc.arg(source), sqlc.arg(note)
)
RETURNING *;

-- name: ListQuakeEvents :many
SELECT * FROM quake_event WHERE circle_id = sqlc.arg(circle_id) ORDER BY occurred_at DESC;

-- name: GetLatestQuakeEvent :one
-- The truncation point: for a quake target, every report with died_at before this moves to history
-- and cannot form the current cluster.
SELECT * FROM quake_event
WHERE circle_id = sqlc.arg(circle_id)
ORDER BY occurred_at DESC
LIMIT 1;
