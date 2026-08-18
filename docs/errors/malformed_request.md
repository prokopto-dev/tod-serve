# `malformed_request`

**HTTP 400** · `type: https://docs.tod-serve.org/errors/malformed_request`

The request could not be read at all.

## What causes it

Truncated or invalid JSON, a body that is not what `Content-Type` promised, or a path or query
parameter that could not be decoded. Nothing was validated, because nothing could be parsed.

This is distinct from [`validation_failed`](validation_failed.md): there the request parsed and a
field was wrong, so `errors[].location` can name it. Here there is no structure to point at.

## What the client should do

Fix the encoding. A generated SDK does not produce this; a client that hits it is building the body
by hand or truncating it somewhere in transit.
