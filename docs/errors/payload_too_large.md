# `payload_too_large`

**HTTP 413** · `type: https://docs.tod-serve.org/errors/payload_too_large`

The request body exceeded the limit for this operation.

## What causes it

A body larger than the operation accepts. A ToD report carries one log line, not a log file — the
limits are sized for the requests the design describes, so hitting one usually means a client is
sending something it did not mean to.

## What the client should do

Send less. If you are importing history, send it as many requests, each idempotent under its own
`Idempotency-Key`, rather than as one large one.
