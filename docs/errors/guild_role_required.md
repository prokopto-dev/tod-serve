# `guild_role_required`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/guild_role_required`

You are in the required guild, but you do not hold any of the roles this circle requires.

## What causes it

- You lack every role in `discord_required_role_ids_json`. An **empty** list means "anyone in the
  guild", so if you are seeing this, the list is not empty.
- The role was granted after you started the flow; the ticket is a snapshot taken at the callback.

## What the client should do

Ask a Discord admin for the role, then re-run the authorization.

**Worth knowing:** this gate is evaluated at join and at re-auth **only**. Losing a role later does
not revoke a PAT that has already been issued — see
[04-identity §8](../design/04-identity-and-revocation.md). Continuous re-checking is a named,
deferred follow-up, not a silent gap. If somebody needs to be out **now**, `revokeMember` is the
mechanism that takes effect on their very next request.
