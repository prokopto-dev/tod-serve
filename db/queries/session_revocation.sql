-- session_revocation - browser sessions that have signed out. A session is signed rather than
-- stored, so this table holds only the ones somebody ended, and only until they would have expired.
--
-- Instance-scoped (canonical section 9): a session id is instance-unique and the row names no
-- circle. Every route passes the CALLER'S OWN session id, read off the verified cookie.

-- name: RevokeSession :exec
-- Signing out twice is one row, not an error: there is one fact to record, and a second sign-out
-- of an already-ended session must not fail. The retry moves updated_at and nothing else, so
-- created_at stays the moment the session actually stopped working.
INSERT INTO session_revocation (session_id, expires_at, created_at, updated_at)
VALUES (sqlc.arg(session_id), sqlc.arg(expires_at), sqlc.arg(created_at), sqlc.arg(updated_at))
ON CONFLICT (session_id) DO UPDATE SET updated_at = excluded.updated_at;

-- name: CountSessionRevocations :one
-- Asked on every session-authenticated request, by primary key. A count rather than the row: the
-- only question is whether this session was signed out, so nothing else is worth selecting.
SELECT COUNT(*) FROM session_revocation WHERE session_id = sqlc.arg(session_id);

-- name: DeleteExpiredSessionRevocations :execrows
-- A revocation stops meaning anything once the session it names has expired, because the codec
-- refuses an expired cookie on its own.
DELETE FROM session_revocation WHERE expires_at < sqlc.arg(before);
