-- tod_meta - instance key/value: schema version, pepper generation, event head.

-- name: GetMeta :one
SELECT * FROM tod_meta WHERE key = sqlc.arg(key);

-- name: SetMeta :exec
INSERT INTO tod_meta (key, value, updated_at)
VALUES (sqlc.arg(key), sqlc.arg(value), sqlc.arg(updated_at))
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

-- name: ConsumeMeta :one
-- Compare-and-swap on a value the caller has already read. It is what makes the one-time owner
-- grant single-use: the second redemption finds the row saying something other than what it read,
-- matches nothing, and gets no row back. A plain SetMeta would overwrite and succeed twice.
UPDATE tod_meta
SET value = sqlc.arg(value), updated_at = sqlc.arg(updated_at)
WHERE key = sqlc.arg(key) AND value = sqlc.arg(expected)
RETURNING *;
