-- tod_meta - instance key/value: schema version, pepper generation, event head.

-- name: GetMeta :one
SELECT * FROM tod_meta WHERE key = sqlc.arg(key);

-- name: SetMeta :exec
INSERT INTO tod_meta (key, value, updated_at)
VALUES (sqlc.arg(key), sqlc.arg(value), sqlc.arg(updated_at))
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;
