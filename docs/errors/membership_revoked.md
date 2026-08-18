# `membership_revoked`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/membership_revoked`

Your membership of this circle exists, but it has been revoked. The circle is real and you were
once in it — this is not a tenancy `404`.

## What causes it

- An officer called `revokeMember` on your membership. Membership state is checked on **every**
  request, so this takes effect immediately rather than when your token expires.
- You redeemed a fresh invite into a circle you were revoked from. There is no second membership
  row and no delete-then-insert path, so redemption lands on the existing revoked row.

## What the client should do

Stop retrying — no credential and no new invite will change this. Ask an officer; reinstatement is
an explicit, audited `POST .../reinstate`. If you believe you were revoked in error, that is a
conversation, not a request to repeat.
