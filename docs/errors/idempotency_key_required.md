# `idempotency_key_required`

**HTTP 400** · `type: https://docs.tod-serve.org/errors/idempotency_key_required`

This `POST` creates domain state and carried no `Idempotency-Key` header.

## What causes it

The header is **required** on every POST that creates domain state, not optional. A retry after a
timeout is the normal case on a home server behind a domestic connection, and without a key the
server cannot tell a retry from a second report — which would put a duplicate kill into an
append-only log that is never edited.

## What the client should do

Generate a key per logical operation — a ULID is ideal — send it, and **reuse the same key for every
retry of that operation**. Uniqueness is `(membership, key)`, keyed on the membership rather than the
token, so rotating your token mid-retry still replays rather than duplicating.
