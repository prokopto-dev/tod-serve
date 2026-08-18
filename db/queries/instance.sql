-- instance - the singleton. Every query names id = 1 rather than trusting the CHECK, so a read
-- that somehow found a second row would return nothing rather than an arbitrary one.

-- name: GetInstance :one
SELECT * FROM instance WHERE id = 1;

-- name: CreateInstance :one
INSERT INTO instance (
  id, name, public_url, timezone, self_service_circle_creation, created_at, updated_at
) VALUES (
  1, sqlc.arg(name), sqlc.arg(public_url), sqlc.arg(timezone),
  sqlc.arg(self_service_circle_creation), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: UpdateInstance :one
UPDATE instance
SET name = sqlc.arg(name),
    public_url = sqlc.arg(public_url),
    timezone = sqlc.arg(timezone),
    self_service_circle_creation = sqlc.arg(self_service_circle_creation),
    updated_at = sqlc.arg(updated_at)
WHERE id = 1
RETURNING *;
