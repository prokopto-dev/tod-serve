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
| **A Discord `client_secret` at rest** | Each instance registers its own Discord application ([ADR-0011](docs/adr/0011-operator-registered-discord-application.md)), so it stores a client secret. A database read is therefore a Discord-application compromise, not merely an identity disclosure. Mitigated by `core.Secret`, and by `instance.security.manage` being step-up and PAT-forbidden. This replaced a worse weakness: under the old project-wide shared app, an access token was valid at *every* instance and replay was mitigated but never closed |
| **A removed Discord role does not revoke an issued token** | The per-circle guild gate is evaluated at join and at re-auth only. Somebody who loses a role, or leaves the guild, keeps working access until their token expires or an officer acts. Continuous re-checking needs a bot polling guild membership and is a named, deferred follow-up in [ROADMAP.md](ROADMAP.md) — not a silent gap. `revokeMember` takes effect on the principal's very next request |
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
- **A Discord access token is never persisted.** It is read inside the OAuth callback and
  discarded; only the derived subject, display name, guild ids and role ids survive, on a
  single-use 120-second ticket.
- **Invites are looked up by hash, never by prefix.** A prefix lookup is a brute-force oracle.
- **Revocation is checked on every request**, not by a cascade at revocation time, so there is no
  sweep that can fail halfway.

## Supported versions

Pre-1.0. Only `main` is supported. There is no working release to patch yet.
