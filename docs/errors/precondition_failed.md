# `precondition_failed`

**HTTP 412** · `type: https://docs.tod-serve.org/errors/precondition_failed`

You sent `If-Match` and the resource has changed since the `ETag` you sent.

## What causes it

Somebody else wrote to the resource between your read and your write. This is the lost-update
defence working: without it your change would silently overwrite theirs.

**The response carries the current representation in `meta.current`,** so the round trip that would
normally follow — re-read, merge, retry — costs no extra request, and a client can show the user
what actually changed rather than "please try again".

## What the client should do

Read `meta.current`, merge your intent into it, and retry with the `ETag` from that response. Do not
retry with the stale `ETag`, and do not drop `If-Match` to make the error go away — that turns a
detected conflict into an undetected one.
