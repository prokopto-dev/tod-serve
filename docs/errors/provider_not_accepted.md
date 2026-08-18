# `provider_not_accepted`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/provider_not_accepted`

The provider you tried to authenticate with is enabled on this instance, but **this circle does not
accept it.**

## What causes it

- The circle's owner has not added it. A new circle auto-accepts every enabled provider with
  `verifiable_subject = 1`, and **`local` is never auto-added** — an owner has to reach for it.
- The owner removed it. Removing a provider stops *new* joins through it; it does not revoke
  existing memberships, so other people being in the circle via that provider proves nothing.

## What the client should do

Read `previewInvite` first — it lists exactly which providers this circle accepts, before anyone
commits to anything. Pick one of those. Only the circle owner (`circle.security.manage`) can change
the list.
