# `request_timeout`

**HTTP 408** · `type: https://docs.tod-serve.org/errors/request_timeout`

The request body did not arrive in time.

## What causes it

A connection that stalled mid-body, or a client that sent headers and then stopped. The read
deadline exists so that a half-open connection does not hold a handler open on a machine that is
also somebody's home server.

## What the client should do

Retry. If the operation creates domain state, retry with the **same** `Idempotency-Key` — the write
may or may not have landed, and that is exactly what the key is for.
