# `idempotency_key_reused`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/idempotency_key_reused`

This `Idempotency-Key` was used before, by you, for a **different** request.

## What causes it

A client bug: a key held across two logically different operations, or a key derived from something
that is not unique per operation. The server hashes the request alongside the key precisely so that
replaying a key with new content is refused rather than silently answered with the old response —
which would report success for something that never happened.

## What the client should do

Use a fresh key for a different request. If the request genuinely is the same one, compare it
byte for byte with what you sent before; something changed.
