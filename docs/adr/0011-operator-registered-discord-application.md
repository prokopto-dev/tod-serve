# ADR-0011 — Each operator registers their own Discord application

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

## Context and problem statement

[ADR-0003](0003-pluggable-identity-providers.md) assumed **one project-wide Discord application**
shared by every self-hosted instance. That single assumption is the sole cause of both release
blockers this project carried: Discord's developer terms may not permit one application relaying
arbitrary third-party servers' end-user tokens, and an access token minted by a shared app is valid
at *every* instance, so a hostile instance can replay it against another.

A third requirement forces the question now. The owner's deployment gates a circle on membership of
one Discord guild, and **reading guild membership requires the `guilds` scope on an application the
operator controls** — there is no way to ask a shared app for it on someone else's behalf.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Keep the project-wide shared app | Nothing for an operator to register; one OAuth consent screen to design and document | Both blockers stay open, neither has a fix that keeps the self-hostable property, and guild membership is unreadable — so the owner's actual deployment is impossible |
| B — Per-instance, operator-registered application | No shared app to violate anyone's terms, and a per-instance `client_id` gives token verification an **audience to check against**, which is what actually closes replay. Guild scopes become available | Every operator must register a Discord application and paste a client id and secret before `discord` works at all. The instance now stores a `client_secret` at rest |
| C — Both, operator's choice | An operator who wants zero setup gets it; one who wants the guild gate registers an app | Two Discord code paths, two trust stories, and the weak one is the default. The blockers stay open for whoever takes the easy path, and "which mode am I in" becomes a support question forever |

## Decision outcome

**Chosen: B.** C is A with extra steps: a shared app that still exists is a shared app that still
has to satisfy Discord's terms and still replays across instances, and defaulting people into the
weaker path while documenting the stronger one is precisely the confident-mistake failure mode this
project is built against. One Discord path, and it is the safe one.

The instance is a **confidential OAuth client**. `identity_provider` gains `client_id`,
`client_secret` (`core.Secret` — never serialised, never logged), `redirect_uri` and
`token_endpoint`, with `CHECK ((kind = 'discord') = (client_id IS NOT NULL))`.

Two new instance-scoped tables carry the flow, both short-lived and prunable: `auth_flow` holds the
`state` and the **server-side** PKCE verifier; `credential_ticket` is single-use with a 120-second
TTL and carries the verified subject, display name, guild ids and role ids. The browser never holds
the PKCE verifier and never sees a Discord token.

`POST /auth/authorization-url` (`createAuthorizationURL`) and `GET /auth/callback/{provider_key}`
(`completeAuthorization`, `Hidden: true`, permitted for the OAuth callback by
[canonical §7](../design/00-canonical-conventions.md#7-http-conventions)) are the only new routes.
`credential.kind` gains `provider_ticket`; [ADR-0007](0007-one-join-endpoint.md)'s one-join-endpoint
rule is preserved exactly — `/join` and `/sessions` still take one credential union. The ticket
reaches the SPA in the redirect **fragment**, never the query, because it is a bearer credential.

**Registration alone does not close replay, and saying so would be the confident mistake this
project is built against.** `GET /users/@me` honours any valid bearer token whichever application
minted it, so a per-instance `client_id` only helps if something checks it. Verification therefore
calls **`GET /oauth2/@me` first and rejects unless `application.id` is ours**
(`401 credential_audience_mismatch`) — on the `bearer_token` path, where it is load-bearing, and in
the callback, where it is redundant. That is the audience binding OIDC gets free from `aud`.

Membership and roles both come from `GET /users/@me/guilds/{guild.id}/member` under
`guilds.members.read`, for the gated guild only — one call, one scope, and the subject's other
guilds are never learned. Absent facts **reject**; a *declined* scope says so
(`provider_scope_declined`) rather than posing as a role failure.

Mechanisms: `TestDiscord_ForeignApplicationToken_Refused`,
`TestDiscord_AccessToken_NeverPersisted`, `TestCredentialTicket_SecondRedemption_Refused`,
`TestCredentialTicket_After120s_Refused`, `TestGuildGate_EvaluatedOnJoinAndSessions`,
`TestGuildGate_MissingRoleFacts_Refused`, `TestNoTokenInURL_CallbackRedirectUsesFragment`.

### Consequences

- Good, because both release blockers close: the terms one by construction, the replay one via the
  audience check a per-instance `client_id` makes possible at all.
- Good, because guild membership and role ids become readable, which is the only way the per-circle
  Discord gate can exist at all.
- Good, because Discord and OIDC now share one browser flow, so the SPA has a single code path and
  `id_token` remains only for non-browser clients.
- **Bad, because every operator must register a Discord application** and paste a client id, a
  secret and a redirect URI before `discord` works. That is real setup friction on the most common
  way in, and some operators will pick `local` instead — which is the weak provider.
- **Bad, because the instance now holds a `client_secret` at rest**, which the shared-app design did
  not require. A database read is now a Discord-app compromise, not merely an identity disclosure.
- **Bad, because the operator now owns an OAuth consent screen**, its branding, its verification
  status and its rate limits. We can document that; we cannot do it for them.
- **Bad, because closing replay now depends on one remembered call.** Skip `GET /oauth2/@me` and the
  hole silently reopens with nothing else catching it — which is why it is a named test and not a
  review habit, and why the `bearer_token` path exists on sufferance rather than by preference.

### Reversal cost

A release. The columns and both tables are additive, so reverting means re-adding a shared app,
re-accepting both blockers, and losing the guild gate — a product decision, not a migration problem.
