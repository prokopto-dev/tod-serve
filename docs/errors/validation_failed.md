# `validation_failed`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/validation_failed`

The request parsed and one or more fields are not acceptable.

## What causes it

A missing required field, a value outside its enum, an id that is not a 26-character ULID, or a
discriminated union whose `kind` does not match what it carries.

**`errors[]` names each offending field in `location`** — `body.credential.token`,
`query.limit` — because a union validated in the service rather than purely in the schema still owes
the caller a specific answer. See [ADR-0007](../adr/0007-one-join-endpoint.md) for why the credential
is a union at all.

## What the client should do

Read `errors[].location` and fix the named field. Retrying an unchanged request will fail
identically.
