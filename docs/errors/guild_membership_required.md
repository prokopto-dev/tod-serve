# `guild_membership_required`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/guild_membership_required`

This circle requires membership of a specific Discord guild, and the guild list on your credential
does not include it.

## What causes it

- You are not in the guild the circle gates on.
- You authorized without the `guilds` scope, so the server could not read your guild list. Start
  the flow again from `createAuthorizationURL` rather than reusing an older authorization.
- You joined the guild after starting the flow — the ticket carries a snapshot taken at the
  callback.

## What the client should do

Join the guild, then re-run the authorization so a fresh ticket carries the new membership.

**Why a guild and not a channel:** Discord has no channel-membership API. Channel visibility is
derived from guild membership plus roles, so that is what the server can actually verify. Modelling
a channel-level rule we cannot check would be a guess dressed as a rule.

Membership itself is answered by `GET /users/@me/guilds`. That call returns **partial** guild
objects and no roles, which is why a role requirement needs a second call —
`GET /users/@me/guilds/{guild.id}/member` — and reports
[`guild_role_required`](guild_role_required.md) rather than this code.
