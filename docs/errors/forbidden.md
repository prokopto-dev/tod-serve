# `forbidden`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/forbidden`

You are authenticated, the circle is yours, and your **role** does not hold the permission this
operation requires.

## What causes it

Effective capability is `role permissions ∩ token scopes`. This is a failure of the left-hand side:
an observer calling `createTodReport`, a member calling `revokeMember`. The operation's
`x-tod-permission` in the OpenAPI document names the permission and the roles that hold it.

A wrong *circle* is [`not_found`](not_found.md), never this — see
[canonical §7](../design/00-canonical-conventions.md#cross-circle-access-returns-404-never-403).

## What the client should do

Ask an officer for a role that holds the permission. Retrying, re-authenticating and minting a
wider token all change nothing: a token can only narrow what the role already grants.
