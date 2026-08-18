# `precondition_required`

**HTTP 428** · `type: https://docs.tod-serve.org/errors/precondition_required`

The operation is a state transition and the request carried no `If-Match`.

## What causes it

`If-Match` is **required** on state transitions, not merely honoured when present. An optional
concurrency check is one that nobody sends, which means the first lost update is discovered by a
user rather than by the server.

## What the client should do

`GET` the resource, take the `ETag` from the response, and repeat the request with
`If-Match: <etag>`. A mismatch then answers [`precondition_failed`](precondition_failed.md) with the
current representation attached.
