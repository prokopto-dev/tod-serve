# `credential_expired`

**HTTP 401** · `type: https://docs.tod-serve.org/errors/credential_expired`

The credential verified structurally but is past its own expiry.

## What causes it

- An `id_token` whose `exp` has passed.
- An access token the provider has already expired.

## What the client should do

Re-authenticate. This one is routine and always recoverable by getting a new credential.
