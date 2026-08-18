# `method_not_allowed`

**HTTP 405** · `type: https://docs.tod-serve.org/errors/method_not_allowed`

The path exists and does not accept this HTTP method.

## What causes it

Usually a hand-written request: `PUT` where the operation is `PATCH`, or `POST` to a collection that
is read-only. Reports are appended and never edited, so `PATCH /tod-reports/{report_id}` is this
rather than a permission failure — corrections are new rows.

## What the client should do

Read `Allow` on the response, or the operation's entry in the OpenAPI document. Generated SDK
methods do not produce this error; a client that hits it is building requests by hand.
