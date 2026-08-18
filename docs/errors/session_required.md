# `session_required`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/session_required`

A personal access token reached an operation that only a browser session may perform.

## What causes it

The operation's permission is in the **capability floor** —
[canonical §6](../design/00-canonical-conventions.md#the-capability-floor). Those are the operations
that alter authentication, authorization or bulk-export state, they carry no PAT scope at all, and
there is no `admin:*` scope and no all-powerful token. A leaked automation token must not be able to
seize a circle, so the floor is enforced at the edge rather than by hoping nobody mints a wide token.

## What the client should do

Perform this operation from the web console, signed in. There is no scope to add and no token to
mint — this is not a narrowing you can widen. `invite.create` is deliberately outside the floor, so
a bot posting an invite link is still a token-shaped job.
