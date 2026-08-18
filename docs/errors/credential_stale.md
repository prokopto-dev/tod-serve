# `credential_stale`

**HTTP 401** · `type: https://docs.tod-serve.org/errors/credential_stale`

The credential is valid and unexpired, but was minted too long ago — more than 60 seconds before
you presented it.

## What causes it

- A non-browser client cached a credential and reused it later. The freshness rule requires minting
  one immediately before joining.

## What the client should do

Mint a fresh credential immediately before the call and do not cache it.

**A note on why this exists.** It was the mitigation for cross-instance token replay under the old
shared-application design: it shrank the replay window without closing it. What closes that hole is
the audience check introduced by
[ADR-0011](../adr/0011-operator-registered-discord-application.md) — `GET /oauth2/@me` must report
this instance's own `client_id`, or the token is refused with
[`credential_audience_mismatch`](credential_audience_mismatch.md). Note that per-instance
registration *alone* would not have been enough: `GET /users/@me` honours any valid bearer token
whichever application minted it.

This code is therefore no longer the primary defence, but it is not redundant either: it still
bounds how long a **stolen same-instance** token is useful, which the audience check does not
address.
