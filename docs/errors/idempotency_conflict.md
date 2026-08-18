# `idempotency_conflict`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/idempotency_conflict`

A request with this `Idempotency-Key` is still in flight.

## What causes it

You retried before the original finished — a timeout shorter than the server's work, or two
processes sharing a key. The record exists and has no response yet, so there is nothing to replay
and starting the work twice is exactly what the key exists to prevent.

## What the client should do

Wait and retry the **same** request with the **same** key. It will replay the original response once
that request completes. Do not mint a new key: that is how one retry becomes two reports.
