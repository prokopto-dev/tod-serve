# `last_owner`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/last_owner`

The operation would leave the circle with no owner.

## What causes it

- Demoting the only owner.
- Revoking the only owner's membership.

## What the client should do

Promote another member to `owner` first, then repeat the operation. A circle with no owner has
nobody who can change its accepted identity providers or delete it — an unrecoverable state, which
is why this is refused rather than warned about.
