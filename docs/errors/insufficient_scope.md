# `insufficient_scope`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/insufficient_scope`

Your **role** holds the permission and your **token** does not carry a scope that reaches it.

## What causes it

Effective capability is `role permissions ∩ token scopes`. This is a failure of the right-hand side.
An officer whose device token carries only `tod:read` gets this on `createTodReport`, and the fix is
a different token rather than a different role — which is exactly why this is not
[`forbidden`](forbidden.md).

## What the client should do

Mint a token carrying the scope the operation declares in `x-tod-scopes`. If the operation declares
**no** scope at all it is in the capability floor and no token reaches it at any scope; that answer
is [`session_required`](session_required.md), not this one.
