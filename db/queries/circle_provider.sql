-- circle_provider - which instance providers this circle accepts, and the Discord guild gate.
-- An empty discord_required_role_ids_json means "anyone in the guild"; a NULL discord_guild_id
-- means the circle does not gate on a guild at all.

-- name: PutCircleProvider :one
INSERT INTO circle_provider (
  circle_id, provider_id, discord_guild_id, discord_required_role_ids_json, created_at, updated_at
) VALUES (
  sqlc.arg(circle_id), sqlc.arg(provider_id), sqlc.narg(discord_guild_id),
  sqlc.arg(discord_required_role_ids_json), sqlc.arg(created_at), sqlc.arg(updated_at)
)
ON CONFLICT (circle_id, provider_id) DO UPDATE SET
  discord_guild_id = excluded.discord_guild_id,
  discord_required_role_ids_json = excluded.discord_required_role_ids_json,
  updated_at = excluded.updated_at
RETURNING *;

-- name: GetCircleProvider :one
SELECT * FROM circle_provider
WHERE circle_id = sqlc.arg(circle_id) AND provider_id = sqlc.arg(provider_id);

-- name: ListCircleProviders :many
SELECT * FROM circle_provider WHERE circle_id = sqlc.arg(circle_id) ORDER BY provider_id;

-- name: DeleteCircleProvider :execrows
-- Stops NEW joins via that provider. It does not revoke existing memberships: mass-revoke on
-- removal is a footgun that eventually deletes a guild's whole roster with one click.
DELETE FROM circle_provider
WHERE circle_id = sqlc.arg(circle_id) AND provider_id = sqlc.arg(provider_id);

-- name: AnyCircleGatesOnAGuild :one
-- tenancy: an INSTANCE-level fact that names no circle, and must not. createAuthorizationURL
-- with no invite code has no circle to resolve - resolving one from a caller-supplied id is the
-- existence oracle canonical section 7 closes - so the OAuth scope decision falls back to "does
-- any circle on this instance gate on a guild at all". The answer is one bit about the instance
-- and identifies nothing. Adding a circle_id here would require the public caller to supply one,
-- which is the whole thing this query exists to avoid.
-- A tombstoned circle's gate does not count. This bit decides whether createAuthorizationURL asks
-- for guilds.members.read when there is no invite, and a deleted circle keeping it set would put a
-- permission on every consent screen that nothing left on this instance uses -- which is exactly
-- what "the authorization request asks for every scope the callback then uses, and no more" is
-- there to prevent.
SELECT EXISTS (
  SELECT 1 FROM circle_provider cp
  JOIN circle c ON c.id = cp.circle_id
  WHERE cp.discord_guild_id IS NOT NULL AND c.deleted_at IS NULL
) AS gated;
