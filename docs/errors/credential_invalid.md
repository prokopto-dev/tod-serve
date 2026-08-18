# `credential_invalid`

**HTTP 401** · `type: https://docs.tod-serve.org/errors/credential_invalid`

The credential you presented did not verify.

## What causes it

- A `bearer_token` the provider rejected, or one for a different application.
- An `id_token` that failed a check: signature against the cached JWKS, `iss`, `aud` (which must be
  **this instance's** client id), `exp`, or `nonce`.
- A malformed `credential` object, or one whose `kind` does not match the provider.

## What the client should do

Re-authenticate and present a fresh credential. Do not retry the same one — nothing about it will
verify on a second attempt. If `aud` is the failing check, you are almost certainly pointed at a
different instance than the one that issued the flow.
