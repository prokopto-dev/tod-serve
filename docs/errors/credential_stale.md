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
shared-application design: it shrank the replay window without closing it.
[ADR-0011](../adr/0011-operator-registered-discord-application.md) closed that hole outright by
making the Discord application per-instance, so a token minted for one operator's client id is
worthless at any other instance. This code remains on the non-browser `bearer_token` path as
defence in depth; it is no longer load-bearing.
