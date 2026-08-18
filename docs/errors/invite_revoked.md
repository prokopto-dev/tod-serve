# `invite_revoked`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/invite_revoked`

An officer revoked the invite before you redeemed it.

## What causes it

- `revokeInvite` was called on it.
- A weakly-revocable member was revoked while `circle.revoke_invalidates_invites = 1` — which is the
  default for weak circles — and every outstanding invite went with them in the same transaction.
  That is deliberate: revoking a person who can return under a new name is not a revocation if their
  invite link is still live in Discord scrollback.

## What the client should do

Ask an officer. If a batch of invites died at once, the second cause above is almost certainly why,
and it was not an accident.

A browser flow can surface this at the **callback** rather than at `/join`: if the invite dies while
the user is on the provider's consent screen, the callback mints no ticket and redirects to
`<spa>/join#error=invite_revoked`. The check there is an early-out — `/join`
re-checks at redemption and is the authority.
