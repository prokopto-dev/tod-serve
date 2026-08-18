# `provider_disabled`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/provider_disabled`

The provider is accepted by the circle but **disabled at the instance level**, so nobody can
authenticate through it right now.

## What causes it

- An instance admin disabled the provider row.
- The circle still lists it, because disabling instance-side does not edit every circle's
  acceptance list. `previewInvite` reports `available: false` for exactly this state.

## What the client should do

Use another provider the circle accepts. Only an instance admin
(`instance.security.manage`, session + step-up) can re-enable it — a PAT cannot, by design.
