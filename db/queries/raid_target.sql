-- raid_target - the catalogue. Instance-wide, not circle-scoped: a mob's existence is a game fact.
-- Matching is on name_norm, normalised in Go, never on a collation.

-- name: CreateRaidTarget :one
INSERT INTO raid_target (
  id, name, name_norm, zone, zone_norm, expansion, category, is_quake_target, state,
  created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(name), sqlc.arg(name_norm), sqlc.arg(zone), sqlc.arg(zone_norm),
  sqlc.arg(expansion), sqlc.arg(category), sqlc.arg(is_quake_target), sqlc.arg(state),
  sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetRaidTarget :one
SELECT * FROM raid_target WHERE id = sqlc.arg(id);

-- name: GetRaidTargetByNameNorm :one
SELECT * FROM raid_target WHERE name_norm = sqlc.arg(name_norm);

-- name: ListRaidTargets :many
SELECT * FROM raid_target WHERE state = sqlc.arg(state) ORDER BY name_norm;

-- name: UpdateRaidTarget :one
UPDATE raid_target
SET name = sqlc.arg(name), name_norm = sqlc.arg(name_norm),
    zone = sqlc.arg(zone), zone_norm = sqlc.arg(zone_norm),
    expansion = sqlc.arg(expansion), category = sqlc.arg(category),
    is_quake_target = sqlc.arg(is_quake_target), state = sqlc.arg(state),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING *;
