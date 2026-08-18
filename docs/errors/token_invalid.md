# `token_invalid`

**HTTP 401** · `type: https://docs.tod-serve.org/errors/token_invalid`

A bearer token was presented and it is not a live token on this instance.

## What causes it

- The token was revoked, by you at `revokeToken` or by an owner holding `token.revoke`.
- The token never existed here — a token from another instance, or a typo.

Revoked and unknown deliberately answer the same way. Distinguishing them would confirm that a
particular token string once existed, which is a fact worth nothing to its legitimate holder and
worth something to whoever found it.

## What the client should do

Stop retrying with this token. Re-authenticate: `POST /sessions` mints a fresh one for an existing
membership. The 8-character prefix of the rejected token is safe to quote to an officer — it is how
a leaked token is traced — and the secret half never is.
