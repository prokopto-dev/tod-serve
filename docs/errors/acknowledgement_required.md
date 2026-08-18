# `acknowledgement_required`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/acknowledgement_required`

The operation has a consequence the server refuses to let you accept implicitly. You must say so in
the request body.

## What causes it

- Enabling the `local` provider without `"acknowledge_weak_revocation": true`. `local` cannot
  durably revoke anyone: a revoked person holding any other invite returns as a new member, and the
  officers believe the problem is handled. **The false confidence is the damage**, not the
  re-entry.

## What the client should do

Re-send with the acknowledgement field set — after reading what you are acknowledging. The flag
exists to make the cost land on somebody who chose it, not to be an extra field to fill in.
