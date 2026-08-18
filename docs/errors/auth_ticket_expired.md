# `auth_ticket_expired`

**HTTP 401** · `type: https://docs.tod-serve.org/errors/auth_ticket_expired`

The ticket was real but is past its **120-second** TTL.

## What causes it

- More than two minutes passed between the OAuth callback and `/join` or `/sessions` — usually a
  user who left the tab sitting on a confirmation screen.

## What the client should do

Start again from `createAuthorizationURL`. The window is deliberately short: the ticket carries a
verified subject and guild facts, so it is a credential, and a credential that lives longer than the
redirect it exists to bridge is a credential with no reason to still be valid.
