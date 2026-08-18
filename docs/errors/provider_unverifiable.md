# `provider_unverifiable`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/provider_unverifiable`

The operation requires an identity whose subject the server can **verify**, and this provider's
cannot be.

## What causes it

- You are using `local`, whose subject is a server-minted ULID attached to a self-asserted name.
  There is nothing to verify and `verifiable_subject = 0` is a `CHECK` against `kind`, not a
  setting somebody forgot to turn on.

## What the client should do

Use `discord` or `oidc` for anything that has to survive a revocation. This is not a configuration
problem to work around: an operation that needs durable identity genuinely cannot accept an
unverifiable one.
