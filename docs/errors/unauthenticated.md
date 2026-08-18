# `unauthenticated`

**HTTP 401** · `type: https://docs.tod-serve.org/errors/unauthenticated`

No credential reached the server, or what reached it was not a credential at all.

## What causes it

- No `Authorization` header and no `__Host-tod_session` cookie on an operation that is not public.
- An `Authorization` header that is not `Bearer <token>`, or a bearer value that is not shaped like
  a token this instance mints.
- **A token in the query string.** `Authorization: Bearer` is the only transport, with no exception
  and no compat shim — a token in a URL lands in access logs, browser history and `Referer` headers.
  The request is rejected here rather than accepted with a warning.

## What the client should do

Send the token in `Authorization: Bearer tods_pat_…`, or the session in its cookie. If you were
putting it in a query parameter, move it; that will never be accepted.
