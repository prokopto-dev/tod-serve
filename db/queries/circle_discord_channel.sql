-- circle_discord_channel - a Discord channel bound to ONE circle. ADR-0017.
--
-- The binding is BOTH halves of the answer: it disambiguates a guild that names several circles,
-- and it is the stored, per-channel opt-in that makes a visible reply something an officer decided
-- rather than something this server guessed. Discord has no channel-membership API, so nobody --
-- this server included -- can enumerate who reads a visible message.

-- name: BindCircleDiscordChannel :one
-- Create or replace the binding for one channel. The conflict target is the CHANNEL, because the
-- primary key is the channel alone: rebinding a channel that belonged to a tombstoned circle is a
-- replace, and rebinding one that belongs to a live circle is refused in Go before this runs --
-- silently redirecting a channel would move a disclosure decision nobody made.
INSERT INTO circle_discord_channel (
  discord_channel_id, circle_id, discord_guild_id, allow_visible, created_by_membership_id,
  created_at, updated_at
) VALUES (
  sqlc.arg(discord_channel_id), sqlc.arg(circle_id), sqlc.arg(discord_guild_id),
  sqlc.arg(allow_visible), sqlc.arg(created_by_membership_id), sqlc.arg(created_at),
  sqlc.arg(updated_at)
)
ON CONFLICT (discord_channel_id) DO UPDATE SET
  circle_id = excluded.circle_id,
  discord_guild_id = excluded.discord_guild_id,
  allow_visible = excluded.allow_visible,
  created_by_membership_id = excluded.created_by_membership_id,
  updated_at = excluded.updated_at
RETURNING *;

-- name: GetCircleDiscordChannel :one
-- tenancy: this is the RESOLVE, and it is the one query here that cannot name a circle_id -- it is
-- what PRODUCES one. An interaction arrives carrying a channel and a guild and no trustworthy
-- circle: accepting a circle id from that payload is the cross-circle-ids-in-bodies class #29
-- closed. So the channel is the key, and every circle-scoped read downstream is filtered by the
-- circle_id this row returns.
--
-- The guild is compared by the CALLER rather than here, so a mismatch is distinguishable from an
-- unbound channel. Both refuse, but only one of them means an officer should go and bind it.
--
-- It carries the circle's deleted_at because a tombstoned circle keeps its bindings -- nothing
-- deletes them, exactly as nothing deletes circle_provider's rows -- so the resolve has to see
-- that the circle is gone rather than answer for it.
SELECT cdc.*, c.deleted_at AS circle_deleted_at
FROM circle_discord_channel cdc
JOIN circle c ON c.id = cdc.circle_id
WHERE cdc.discord_channel_id = sqlc.arg(discord_channel_id);

-- name: GetCircleDiscordChannelInCircle :one
SELECT * FROM circle_discord_channel
WHERE circle_id = sqlc.arg(circle_id) AND discord_channel_id = sqlc.arg(discord_channel_id);

-- name: ListCircleDiscordChannels :many
-- "Which channels does this circle disclose into?" is the question an officer asks before a raid
-- and an audit asks afterwards. ix_circle_discord_channel_circle_id is what stops it being a scan.
SELECT * FROM circle_discord_channel
WHERE circle_id = sqlc.arg(circle_id)
ORDER BY discord_channel_id;

-- name: UnbindCircleDiscordChannel :execrows
-- Unbinding stops the NEXT message. It unsays nothing: whatever was posted while the binding was
-- live is Discord's now, and Discord keeps it.
DELETE FROM circle_discord_channel
WHERE circle_id = sqlc.arg(circle_id) AND discord_channel_id = sqlc.arg(discord_channel_id);
