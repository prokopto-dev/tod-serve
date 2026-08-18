# Security

## Reporting

Do not open a public issue. Use
[GitHub private vulnerability reporting](https://github.com/prokopto-dev/tod-serve/security/advisories/new).

Expect an acknowledgement within a week. This is a volunteer project with one maintainer; that is
the honest number, not a target we will quietly miss.

## What this software is trusted with

A circle's time-of-death data is competitive intelligence. On Project 1999, knowing when a raid
target died — and therefore when it will spawn — is the thing rival guilds most want and the thing a
circle exists to protect. Treat a data-disclosure bug here as you would treat one in a private forum,
not as one in a scoreboard.

## Known and accepted weaknesses

Recorded here so nobody has to discover them. Each links to the decision that accepted it.

| Weakness | Detail |
|---|---|
| **The host operator reads everything** | Whoever runs an instance can read every circle on it. No design at this weight class changes that. Run your own binary for a circle you would rather nobody else could read — see [ADR-0002](docs/adr/0002-circle-is-the-tenant.md) |
| **Cross-instance Discord token replay** | One project-wide Discord application means a user's access token is valid at *every* tod-serve instance, so a hostile instance can replay it against another and impersonate them there. PKCE does not help — it is a bearer token and instance-agnostic. Mitigated, not closed, by a 60-second freshness requirement. `oidc` is structurally immune because its `aud` is the instance's own client id. See [ADR-0003](docs/adr/0003-pluggable-identity-providers.md) |
| **`local` revocation is advisory** | A circle accepting the `local` provider cannot durably revoke anyone: a revoked person with any other invite returns as a new member. This is why `local` ships disabled and enabling it requires an explicit acknowledgement, and why `revocation_strength` is a machine-readable field a client must render rather than a paragraph in a guide |
| **Operator-supplied OIDC URLs are an SSRF surface** | Discovery and JWKS endpoints are configured by the instance operator. Mitigated by an outbound allowlist, a dialer denying private, link-local, loopback and cloud-metadata addresses, and by `instance.security.manage` being step-up and PAT-forbidden so a leaked token cannot add a malicious issuer |
| **A false quake wipes the board** | `tod.quake.report` is officer-only for this reason. It is recoverable — the report log is append-only and nothing is destroyed — but every window in the circle is wrong until it is retracted |

## Design guarantees

- **No all-powerful token exists.** Effective capability is `role permissions ∩ token scopes`, there
  is no `admin:*` scope, and operations altering authentication or authorization state are session +
  step-up only with no scope at all.
- **Cross-circle access returns `404`, never `403`.** A `403` would confirm that a circle exists and
  that the caller found a valid id.
- **Tokens never appear in a URL.** Query-string tokens are rejected with `401`, with no exception —
  there is no compat shim here.
- **A Discord access token is never persisted.** It is verified inside the request and discarded.
- **Invites are looked up by hash, never by prefix.** A prefix lookup is a brute-force oracle.
- **Revocation is checked on every request**, not by a cascade at revocation time, so there is no
  sweep that can fail halfway.

## Supported versions

Pre-1.0. Only `main` is supported. There is no working release to patch yet.
