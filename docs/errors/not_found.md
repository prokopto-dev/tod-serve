# `not_found`

**HTTP 404** · `type: https://docs.tod-serve.org/errors/not_found`

The resource does not exist, or it is not yours to see.

## What causes it

- A genuine wrong id.
- **A circle-scoped resource in a circle you are not a member of.** This is deliberate and it is the
  load-bearing tenancy rule: cross-circle access answers `404`, never `403`, because a `403` would
  confirm that the circle exists and that the id is real. ToDs are competitive intelligence and a
  circle's *existence* is part of what it is hiding, so probing ids must not be a way to map the
  instance —
  [canonical §7](../design/00-canonical-conventions.md#cross-circle-access-returns-404-never-403).

Within a circle you *are* in, insufficient permission is [`forbidden`](forbidden.md). The
distinction is exactly: wrong tenant is `404`, right tenant and insufficient permission is `403`.

## What the client should do

Check the id. If you believe the resource exists and belongs to a circle you are in, check which
circle your token is bound to — a PAT is bound to one membership, so it can only ever see one.
