# `last_owner`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/last_owner`

The operation would leave the circle with no owner.

## What causes it

- Revoking the only owner's membership.

**Not a demotion, any more.** `updateMember` refuses a role change against your own row or against
somebody who outranks you — [`forbidden`](forbidden.md) — so demoting an owner already needs another
owner to be the one asking, and this cannot arise. Revocation is the one thing an owner may still do
to themselves, so it is where the circle can still be talked down to its last one.

## What the client should do

Promote another member to `owner` first, then repeat the operation. A circle with no owner has
nobody who can change its accepted identity providers or delete it — an unrecoverable state, which
is why this is refused rather than warned about.
