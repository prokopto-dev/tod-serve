# `token_expired`

**HTTP 401** · `type: https://docs.tod-serve.org/errors/token_expired`

The token is genuine and its `expires_at` has passed.

## What causes it

A token minted with an expiry reached it. Tokens minted by a PAT are hard-narrowed to 24 hours,
so an automation that mints its own credentials meets this routinely and by design.

## What the client should do

Re-authenticate. `POST /sessions` mints a fresh token for an existing membership without needing a
new invite. This is distinct from [`token_invalid`](token_invalid.md) precisely so that a client can
tell "renew" from "stop": expiry is expected and scriptable, revocation is not.
