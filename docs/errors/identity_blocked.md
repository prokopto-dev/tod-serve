# `identity_blocked`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/identity_blocked`

This identity is blocked **instance-wide**. It cannot join any circle on this instance.

## What causes it

- An instance operator set `identity.blocked_at` on your identity. This is checked at join *and* at
  ticket redemption, so a different circle is not a different door — including a circle whose
  officers have never heard of you.

## What the client should do

Nothing on the client resolves this. Only the instance operator can lift it, and it is a deliberate
act rather than a side effect of a circle-level decision: per-circle revocation (`revokeMember`) is
the normal tool and stays independent of this. If you were blocked in error, that is a conversation
with whoever runs the instance.
