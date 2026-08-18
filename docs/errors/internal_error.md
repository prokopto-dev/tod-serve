# `internal_error`

**HTTP 500** · `type: https://docs.tod-serve.org/errors/internal_error`

Something failed on the server that the server did not anticipate.

## What causes it

A bug. The response carries no detail beyond this code, because the detail an unexpected failure
carries is exactly the detail that leaks internals; the specifics are in the instance's logs,
correlated by the request id echoed in `meta.request_id`.

## What the client should do

Retry once, with the same `Idempotency-Key` if the operation had one — the failure may have been
transient, and the key makes retrying safe even if the write actually landed. If it persists, send
`meta.request_id` to whoever runs the instance; it is the only thing that finds the log line.
