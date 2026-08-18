# `conflict`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/conflict`

The request is well-formed and contradicts the current state of the resource.

## What causes it

The generic case, used where no narrower code in this directory says it better — a uniqueness
constraint the request would violate, or a state transition that is not legal from where the
resource is now. Where a narrower code exists it is used instead, so a client can branch: an
exhausted invite is [`invite_exhausted`](invite_exhausted.md), a second retraction is
[`already_retracted`](already_retracted.md).

## What the client should do

Re-read the resource and decide. A blind retry produces the same answer, because nothing about the
request was transient.
