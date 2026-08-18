# `provider_scope_declined`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/provider_scope_declined`

You authorized successfully, but withheld a permission this circle's checks need. The server knows
because `GET /oauth2/@me` reports the scopes actually granted, not the ones requested.

## What causes it

- You unticked a permission on the provider's consent screen. For a Discord circle with a role
  gate that is `guilds.members.read`, which lets the server read your roles **in that one guild**.
- An operator's application is registered without the scope, so it cannot be granted even when you
  accept everything offered.

## What the client should do

Re-run the authorization and accept the requested permission, or ask the circle's officers to drop
the role requirement.

**This is deliberately not `guild_role_required`.** You may well hold the role — the server was
simply never allowed to look, and saying "you lack the role" when the truth is "we could not check"
would be a confident mistake pointing you at the wrong fix. One is resolved by granting a
permission; the other by asking an officer for a role. They are not interchangeable.

The scope is narrow on purpose: `guilds.members.read` reveals your membership and roles in the
**single guild that gates this circle**. The server never requests the broader `guilds` scope and
never learns what other Discord servers you are in.
