# `already_retracted`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/already_retracted`

That report has already been retracted. A report may be retracted at most once — enforced by a
unique index on `retracts_report_id`, so it is a schema fact rather than a check that can be missed.

## What causes it

- A duplicate retraction, often a retry whose first attempt actually succeeded.
- Two officers retracting the same report at once.

## What the client should do

Treat it as done. **A retraction of a retraction is not supported** — if the original report was
right after all, post a fresh report rather than trying to undo the undo.
