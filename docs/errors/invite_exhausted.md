# `invite_exhausted`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/invite_exhausted`

The invite reached `max_uses`. It was valid, and somebody else used the last one.

## What causes it

- Every use was consumed. `uses <= max_uses` is a `CHECK`, so this is a database-level fact rather
  than a race the server lost.
- It was minted by a PAT, which forces `max_uses = 1`. A single-use invite that has been used once
  is exhausted — that is the whole point of the narrowing.
- It was minted for the `local` provider, which also forces `max_uses = 1`.

## What the client should do

Ask for a new invite. If you expected more uses, check whether it was minted by a bot rather than a
session.
