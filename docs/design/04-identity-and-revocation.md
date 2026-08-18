# Identity and revocation

**Status:** normative. **Tie-breaker:** [00-canonical-conventions.md](00-canonical-conventions.md).
**Decision record:** [ADR-0003](../adr/0003-pluggable-identity-providers.md).

An identity is a `(provider, subject)` pair. A membership binds to an `identity_id`, **never to a
bare Discord id**. That indirection is the whole reason an instance can offer more than one way in.

## 1. The provider registry

`identity_provider` is instance-level and mutable under `instance.security.manage` (session +
step-up).

| Column | Notes |
|---|---|
| `id` | ULID |
| `key` | wire identifier, unique: `discord`, `authentik`, `local` |
| `kind` | `discord \| oidc \| local` |
| `display_name` | "Sign in with Discord" |
| `enabled` | 0/1 |
| `verifiable_subject` | 0/1 — `CHECK ((kind = 'local') = (verifiable_subject = 0))` |
| `issuer`, `authorization_endpoint`, `jwks_uri`, `subject_claim` | OIDC only; `NULL` otherwise |
| `client_id`, `client_secret`, `redirect_uri`, `token_endpoint` | The operator's **own** OAuth application — see [ADR-0011](../adr/0011-operator-registered-discord-application.md). `CHECK ((kind = 'discord') = (client_id IS NOT NULL))` |

**`verifiable_subject` cannot be lied about.** It is a CHECK against `kind`, not an operator toggle.
Everything downstream about revocation strength hangs off it, so it must not be settable.

`client_secret` is a `core.Secret`: it renders as `***` in `String`, `MarshalJSON` and `LogValue`,
and `listIdentityProviders` returns `client_id` but **never** the secret. This is a new asset at
rest that the shared-app design did not have, and it is named as a cost in
[ADR-0011](../adr/0011-operator-registered-discord-application.md) rather than left to be found.

**Enforced by:** the `No secret is ever logged` invariant — a test marshals the whole config and
asserts no known secret value appears.

At most one `discord` row and at most one `local` row, by partial unique index. Any number of `oidc`.

| Kind | How the server verifies | Credential persisted? |
|---|---|---|
| `discord` | Server-side code exchange against the **operator's own** application, then `GET users/@me` and `GET users/@me/guilds`. Subject = the snowflake `id`; display name = `global_name ?? username`. | **Never.** The access token is read and discarded inside the callback request; only the derived facts reach `credential_ticket`. |
| `oidc` | Verify the **ID token** offline: signature against cached JWKS, `iss`, `aud = client_id`, `exp`, `nonce`. Subject = `subject_claim`, default `sub`. | No. |
| `local` | Nothing to verify. Subject is a server-minted ULID; display name is self-asserted. | n/a |

ID token rather than userinfo for OIDC: verifiable offline, no per-join round trip, and one fewer
operator-supplied URL on the SSRF surface.

## 2. Revocation strength is a property of the circle *and* of the membership

Two different questions, so two fields:

- **`circle.revocation_strength`** — *"can we keep people out?"* The weakest over the circle's
  accepted, enabled providers. Forward-looking.
- **`membership.revocation_strength`** — *"will revoking **this** person stick?"* From the provider
  behind that membership's identity.

A circle accepting both `discord` and `local` is `weak` overall, but its Discord members are
individually `durable`. Both are exposed, machine-readable, never prose-only:

```json
"revocation_strength": "weak",
"revocation_weak_reasons": ["unverifiable_provider"],
"weak_providers": ["local"]
```

Surfaced in `getCircle`, **`previewInvite` — before anyone joins** — `listMembers`, `getMember`, and
the `revokeMember` response.

**Why this is in the API rather than the documentation.** The dangerous outcome of a weak revocation
is not the re-entry. It is the officers' false belief that revocation worked. A field a client must
render is the only thing that reliably reaches them; a paragraph in the operations guide does not.

Additional mitigation: when a weakly-revocable member is revoked and `circle.revoke_invalidates_invites
= 1` (default on for weak circles), every outstanding invite is revoked in the same transaction and
the response says so.

## 3. Identity linking

**No automatic linking, ever. Manual linking, officer-gated, and only between verifiable identities.**

`identity_link(id, primary_identity_id, linked_identity_id, method, linked_by_membership_id,
linked_at)`, append-only, `method ∈ { officer_asserted, provider_verified }`.

Hard rule, trigger-enforced plus `TestIdentityLink_LocalProvider_Rejected`: **a link participant must
have `verifiable_subject = 1`.** A `local` identity can never be linked. Silently unifying an
unverified identity with a verified one is precisely the hole — it would let anyone who can assert a
display name inherit, or resurrect, another person's standing.

The only reason linking exists: **revoking a membership revokes it across the whole link set.** Two
identities that are one person must not be two doors.

*Rejected: linking by verified email across OIDC issuers.* Email is not a stable subject,
`email_verified` is a claim the issuer makes about itself, and it would let anyone receiving mail at
a domain assume an identity.

**A consequence accepted rather than hidden.** A person holding both a Discord and an OIDC identity
in one circle, unlinked, appears twice in the member list and their reports count as two distinct
reporters — which *inflates consensus confidence*. That is ugly. The fix is a deliberate officer
link. The member list flags `possible_duplicate: true` when two unlinked memberships share a
`display_name_norm`, and does not act on it.

*Rejected: one provider per circle.* It closes the duplicate hole completely and is much simpler —
and it breaks the exact case that motivated pluggable identity, a group where some people have
Discord and some do not.

## 4. Two configuration levels

| Level | Who | What |
|---|---|---|
| Instance | `instance.security.manage`, session + **step-up** | Which providers exist at all |
| Circle | `circle.security.manage`, owner only, session | Which of the enabled providers this circle accepts |

A new circle auto-accepts every enabled provider with `verifiable_subject = 1`. **`local` is never
auto-added** — an owner must reach for it.

Removing a provider from a circle stops *new* joins via it; it does **not** revoke existing
memberships. The alternative — mass-revoke on removal — is a footgun that will eventually delete a
guild's whole roster with one click.

If the instance disables a provider a circle accepts, that provider reports `available: false` in
`previewInvite` and joins return `409 provider_disabled`.

## 5. One join endpoint

```
POST /api/v1/join
{ "invite_code": "TODI-4KQ7M-9XPB2",
  "provider": "discord",
  "credential": { "kind": "provider_ticket", "ticket": "..." },
  "display_name": "Tankguy",
  "client": { "name": "nparse-plus-tod", "version": "1.2.0" } }
```

`credential` is a discriminated union on `kind`: `provider_ticket` (any browser flow — `discord` and
`oidc` alike), `bearer_token` (non-browser `discord`), `id_token` + `nonce` (non-browser `oidc`),
`none` (`local`). `display_name` is required for `local`, optional elsewhere.

### Where a `provider_ticket` comes from

[ADR-0011](../adr/0011-operator-registered-discord-application.md) makes the instance a
**confidential** OAuth client holding its own `client_secret`, so the code exchange happens
server-side and the browser never touches a provider token:

```
POST /api/v1/auth/authorization-url          createAuthorizationURL   public
  { provider, invite_code?, circle_id? } -> { authorization_url, expires_at }
  Writes auth_flow(state, pkce_verifier, provider_id, invite_code_hash?, circle_id?, expires_at).
  The PKCE verifier stays SERVER-side. Expired or unknown state -> 409 auth_flow_expired.

GET  /api/v1/auth/callback/{provider_key}    completeAuthorization    Hidden: true
  Exchanges the code, calls users/@me and users/@me/guilds (guilds.members.read for roles),
  DISCARDS the Discord access token, writes a single-use credential_ticket (TTL 120s) carrying
  subject, display_name, guild ids and role ids, then 302s to the SPA with the ticket.

POST /api/v1/join       credential: { "kind": "provider_ticket", "ticket": "..." }
POST /api/v1/sessions   same
```

`Hidden: true` on the callback is permitted for exactly this by
[canonical §7](00-canonical-conventions.md#7-http-conventions) — it is a browser redirect target,
not an operation an SDK should generate a method for.

**This preserves the one-join-endpoint rule of [ADR-0007](../adr/0007-one-join-endpoint.md)
exactly.** The new operations sit *before* the join; `/join` and `/sessions` still take one
credential union and still dispatch on provider. And because OIDC uses the same browser path, **the
SPA has one code path for both providers** — `id_token` and `bearer_token` survive only for clients
with no browser to redirect.

A ticket is redeemable **once**, at either `/join` or `/sessions`. A second redemption is
`401 auth_ticket_invalid`; one presented after 120 seconds is `401 auth_ticket_expired`.

**Enforced by:** `TestCredentialTicket_SecondRedemption_Refused`,
`TestCredentialTicket_After120s_Refused`, `TestDiscord_AccessToken_NeverPersisted`.

**Why one endpoint and not `/join/discord`, `/join/oidc`, `/join/local`.** The plugin holds
destinations across several hosts, each configured differently, and must discover the provider set at
runtime from `previewInvite`. With one endpoint the client is data-driven: read providers, pick one,
POST. With one route per provider, adding a provider becomes a *route* change — an OpenAPI change, an
SDK regeneration and a plugin release before any operator can use it. The entire point is that
operators choose providers, so the route surface must not depend on that choice.

**The cost, stated:** a `oneOf` with a discriminator is uglier in generated SDKs than three clean
bodies, and the union is validated in the service rather than purely in the schema. Validation errors
come back as `validation_failed` with `errors[].location = "body.credential.token"`, so they are
still specific.

`POST /sessions` takes the identical shape minus `invite_code`, plus `circle_id`.

## 6. `local` ships disabled

Enabling it requires `"acknowledge_weak_revocation": true` in the body, or `422
acknowledgement_required`.

**The failure mode if an operator enables it without understanding.** An officer revokes a leaker.
The leaker still has the invite link in Discord scrollback, or gets one from a friend inside, redeems
it as "Tanky", and is reading the same ToDs a minute later. The officers believe the problem is
handled. **The false confidence is the damage**, not the re-entry.

Secondary failure: `local` identities have no credential to re-present, so `POST /sessions` cannot
work for them and every lost token becomes a new invite. Invite hygiene degrades until someone leaves
a 30-day, 50-use invite lying around — the same hole from the other side. `local` therefore also
forces `max_uses = 1` on invites minted for it.

Shipping mitigations: disabled by default; explicit acknowledgement; never auto-accepted by a circle;
`weak` surfaced on the circle, the membership, the invite preview and the revoke response;
`revoke_invalidates_invites` defaulting on; a persistent banner in the SPA.

**Where `local` is genuinely correct:** a LAN binary with no outbound network at all, four friends, a
demo, and CI fixtures. Those are real, so it ships — it just ships honest.

## 7. The Discord trust boundary, after ADR-0011

This section used to record two unresolved risks.
**[ADR-0011](../adr/0011-operator-registered-discord-application.md) closed both**, by making the
Discord application per-instance and operator-registered rather than one project-wide app. They are
named here with their resolutions rather than deleted, because the next person to propose a shared
app needs to find out why there isn't one.

| Was | Now |
|---|---|
| **Cross-instance token replay.** One shared app meant a user's access token was valid at *every* instance, so a hostile instance could replay it elsewhere. PKCE did not help — it is a bearer token and instance-agnostic. Mitigated only by a 60-second freshness rule (`credential_stale`) | **Closed.** A token is minted for the operator's *own* client id and is worthless at any other instance. The token is also never handed to a client at all: it is exchanged server-side and discarded inside the callback |
| **Discord's developer terms.** One application relaying arbitrary third-party self-hosted servers' end-user tokens was not obviously within them, and a human had to read the ToS before `discord` could ship | **Closed.** There is no shared application. Each operator registers their own and agrees to Discord's terms directly, exactly as any other Discord app author does |
| **Unowned operational health.** A join storm made the shared app a heavily rate-limited client, and a ban hit every instance at once | **Closed.** Rate limits and bans are per-instance, and the operator owns theirs |

`credential_stale` remains in the error enum for the non-browser `bearer_token` path, but it is no
longer load-bearing: it was the mitigation for a hole that no longer exists.

### What is honestly still weak

Three costs, stated rather than discovered:

- **Operator setup friction.** `discord` does not work until an operator registers an application
  and pastes a client id, a client secret and a redirect URI. That is real work on the most common
  way in, and some operators will reach for `local` instead — which is the *weak* provider. The
  mitigation is documentation and a `tod-serve doctor` check, and neither makes the step go away.
- **A `client_secret` at rest.** The instance now holds one, which the shared-app design did not
  require. A database read is now a Discord-application compromise, not merely an identity
  disclosure. Mitigated by `core.Secret`, by `instance.security.manage` being step-up and
  PAT-forbidden, and by the no-secret-is-ever-logged test — mitigated, not removed.
- **Removing a Discord role does not revoke an already-issued PAT.** See §8. This is the one to read
  twice, because it is the gap most likely to be assumed closed.

## 8. The Discord access gate is per circle

Discord has **no channel-membership API**. A channel's visibility is derived from guild membership
plus roles, which is how the channel an officer is picturing is actually gated — so guild membership
plus roles is what the server can check, and therefore what it models. Inventing a channel-level
rule we cannot verify would be a confident mistake of exactly the kind this project is built
against.

The gate therefore lives on `circle_provider`, which is already circle-scoped:

| Column | Meaning |
|---|---|
| `discord_guild_id` | `TEXT NULL` — the guild this circle requires membership of. `NULL` means no guild gate |
| `discord_required_role_ids_json` | `TEXT NOT NULL DEFAULT '[]'` — **an empty list means "anyone in the guild"** |

**The instance owns the application; the circle owns the gate.** Two circles on one instance may
point at two different guilds, which is why this is not an instance setting. It is evaluated in
**both** `/join` and `/sessions`, against the guild ids and role ids carried on the 120-second
`credential_ticket` — never against a cached copy, and never against a client-supplied claim.
Failure is `403 guild_membership_required` or `403 guild_role_required`.

**Enforced by:** `TestGuildGate_EvaluatedOnJoinAndSessions`.

### The gap, stated plainly

**Removing someone's Discord role does not revoke an already-issued PAT.** The gate is checked at
join and at re-auth, and at no other moment. Somebody who joined legitimately and then lost the
role — or left the guild entirely — keeps working access until their token expires or an officer
acts.

Continuous enforcement would need a bot polling guild membership, which is a background job, a
second set of Discord rate limits, a new failure mode when Discord is unreachable, and a policy
decision about what to do when it is. That is a project, and it is named as a follow-up in
[ROADMAP.md](../../ROADMAP.md) rather than left as a silent gap.

**The mechanism that works immediately, on the very next request, is `revokeMember`** — membership
state is checked on every request (§2), so revocation takes effect without waiting for anything. If
an officer needs someone out *now*, that is the tool, and the Discord gate is not a substitute for
it.

An instance-wide equivalent exists for the harder case: `identity.blocked_at` refuses a banned
identity at join and at ticket redemption, so they cannot land in *any* circle on the instance —
including one whose officers have never heard of them. Per-circle revocation stays the normal tool;
the block is the operator's. **Enforced by:** `TestJoin_BlockedIdentity_Refused` —
`403 identity_blocked`.

## 9. SSRF

Discord's URL is fixed and fine. OIDC discovery and JWKS URLs are **operator-supplied**, the classic
pivot. Mitigated by [canonical §14](00-canonical-conventions.md#14-outbound-requests) plus
`instance.security.manage` being step-up and PAT-forbidden, so a leaked token cannot add a malicious
issuer.

The dialer must deny link-local and cloud-metadata addresses, not merely RFC1918.
