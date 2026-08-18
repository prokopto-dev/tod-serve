# `rate_limited`

**HTTP 429** · `type: https://docs.tod-serve.org/errors/rate_limited`

Too many requests from this caller.

## What causes it

The generic limit, or the hard one. `previewInvite` and `createAuthorizationURL` both reveal whether
an invite code is live, so they are metered from **one shared bucket keyed on the caller** rather
than a bucket each — two buckets would simply hand a code-guesser twice the budget. Exhaustion is
this generic answer, deliberately: a more specific one would itself be a signal.

## What the client should do

Honour `Retry-After` and back off. If you are enumerating invite codes, stop; the bucket is sized so
that guessing is not worth the wait.
