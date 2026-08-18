# `invite_expired`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/invite_expired`

The invite was real and is now past its `expires_at`. **There are no eternal invites** — the column
is `NOT NULL` by design.

## What causes it

- Simply time. The invite reached its expiry before anyone redeemed it.
- It was minted by a PAT, which hard-caps `expires_in` at 24 hours regardless of what the request
  asked for. If the response that created it carried `capped_by: "pat"`, this is why.

## What the client should do

Ask for a new invite. Nothing on the client can recover this one, and an expired invite is not
re-openable by design.
