-- raid_target_alias - `VA`, `Naggy`, `Vox`, `Trak`.

-- name: CreateRaidTargetAlias :one
INSERT INTO raid_target_alias (id, target_id, alias, alias_norm, created_at, updated_at)
VALUES (
  sqlc.arg(id), sqlc.arg(target_id), sqlc.arg(alias), sqlc.arg(alias_norm),
  sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetRaidTargetByAliasNorm :one
SELECT t.* FROM raid_target t
JOIN raid_target_alias a ON a.target_id = t.id
WHERE a.alias_norm = sqlc.arg(alias_norm);

-- name: ListRaidTargetAliases :many
SELECT * FROM raid_target_alias WHERE target_id = sqlc.arg(target_id) ORDER BY alias_norm;

-- name: ListAllRaidTargetAliases :many
-- Every alias in the catalogue, for the resolve ladder and for rendering a page of targets without
-- one query per row.
SELECT * FROM raid_target_alias ORDER BY alias_norm;

-- name: DeleteRaidTargetAliases :execrows
-- An alias set is replaced wholesale on update. raid_target_alias is mutable, not append-only:
-- an alias is a spelling, not an observation.
DELETE FROM raid_target_alias WHERE target_id = sqlc.arg(target_id);
