# `auth_ticket_invalid`

**HTTP 401** · `type: https://docs.tod-serve.org/errors/auth_ticket_invalid`

The `provider_ticket` you presented is not redeemable.

## What causes it

- It has already been redeemed. **A ticket is single-use**, at either `/join` or `/sessions` —
  never both, and never twice.
- It never existed, or was truncated in transit.
- It was pruned. Tickets are short-lived rows and expiry sweeps remove them.

## What the client should do

Start again from `createAuthorizationURL`. Do not retry the same ticket — a retry after a network
timeout is the most common way to reach this, and it usually means your first attempt succeeded.
Check whether you already hold a token before re-running the flow.
