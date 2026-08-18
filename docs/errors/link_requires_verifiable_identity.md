# `link_requires_verifiable_identity`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/link_requires_verifiable_identity`

Both participants in an `identity_link` must have `verifiable_subject = 1`, and one of yours does
not.

## What causes it

- One side is a `local` identity. **A `local` identity can never be linked** — trigger-enforced,
  plus `TestIdentityLink_LocalProvider_Rejected`.

## What the client should do

There is no way to link this pair, and that is the point. Linking exists so that revoking a
membership revokes it across the whole link set — two identities that are one person must not be two
doors. Silently unifying an unverified identity with a verified one would let anyone who can assert
a display name inherit, or resurrect, another person's standing.
