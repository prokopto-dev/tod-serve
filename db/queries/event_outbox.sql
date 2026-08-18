-- event_outbox - append-only, and the home of the one global sequence (canonical section 4). No UPDATE
-- and no DELETE: a delivered event that could be edited is a replay a client cannot trust.
--
-- `circle_id` is nullable because an instance-level event belongs to no circle, which is why this
-- table is instance-scoped. Every per-circle read still names it.

-- name: AppendEvent :one
INSERT INTO event_outbox (id, circle_id, kind, payload_json, created_at)
VALUES (
  sqlc.arg(id), sqlc.narg(circle_id), sqlc.arg(kind), sqlc.arg(payload_json), sqlc.arg(created_at)
)
RETURNING *;

-- name: ListCircleEventsSince :many
SELECT * FROM event_outbox
WHERE circle_id = sqlc.arg(circle_id) AND event_seq > sqlc.arg(since_seq)
ORDER BY event_seq
LIMIT sqlc.arg(row_limit);

-- name: GetLatestEventSeq :one
SELECT COALESCE(MAX(event_seq), 0) AS event_seq FROM event_outbox;
