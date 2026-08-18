# `identity_provider_unreachable`

**HTTP 503** · `type: https://docs.tod-serve.org/errors/identity_provider_unreachable`

The server could not reach the identity provider to complete verification. **This is the server
saying it does not know, rather than guessing** — a failed reachability check is never treated as a
failed authentication.

## What causes it

- The provider is down, or rate-limiting this instance.
- A network or DNS failure on the instance's side.
- For OIDC, a discovery or JWKS URL that resolves to an address the outbound dialer denies —
  private, link-local, loopback or cloud-metadata. That denial is an SSRF control and it is
  working as designed; the fix is the configured URL, not the dialer.

## What the client should do

Retry with backoff; this one is genuinely transient more often than not. If it persists, the
instance operator should check `/admin/doctor`. Note that rate limits are **per-instance** — one
operator's join storm cannot exhaust anyone else's budget.
