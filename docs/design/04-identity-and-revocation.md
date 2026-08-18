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
| `issuer`, `client_id`, `authorization_endpoint`, `jwks_uri`, `subject_claim` | OIDC only; `NULL` otherwise |

**`verifiable_subject` cannot be lied about.** It is a CHECK against `kind`, not an operator toggle.
Everything downstream about revocation strength hangs off it, so it must not be settable.

At most one `discord` row and at most one `local` row, by partial unique index. Any number of `oidc`.

| Kind | How the server verifies | Credential persisted? |
|---|---|---|
| `discord` | `GET https://discord.com/api/users/@me` with the presented bearer. Subject = the snowflake `id`; display name = `global_name ?? username`. | **Never.** Verified and discarded inside the request. |
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
  "credential": { "kind": "bearer_token", "token": "..." },
  "display_name": "Tankguy",
  "client": { "name": "nparse-plus-tod", "version": "1.2.0" } }
```

`credential` is a discriminated union on `kind`: `bearer_token` (discord), `id_token` + `nonce`
(oidc), `none` (local). `display_name` is required for `local`, optional elsewhere.

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

## 7. The Discord relay's trust boundary

Two unresolved risks, recorded here and in
[ADR-0003](../adr/0003-pluggable-identity-providers.md) rather than discovered later.

**Cross-instance token replay.** The same Discord access token is valid at *every* tod-serve
instance, because there is one project-wide app. A hostile instance can take a user's token and
present it to a different instance to impersonate them there. **PKCE does not help** — the access
token is bearer and instance-agnostic.

The real fixes are (a) a project-run verification service issuing a signed, audience-bound assertion
the instance verifies offline, which reintroduces exactly the central infrastructure the
self-hostable property was meant to avoid, or (b) telling privacy-sensitive circles to use `oidc`,
where the ID token's `aud` is the instance's own client id and replay is structurally impossible.

The mitigation available today: **require the plugin to mint a fresh token immediately before joining
and reject credentials older than 60 seconds** (`credential_stale`). It does not stop a hostile
instance replaying inside that window, but it shrinks it enormously.

This is an unfixable weakness of the one-shared-app decision. It needs an explicit written accept,
not a silent inheritance.

**Discord's developer terms.** One application, used by arbitrary third-party self-hosted servers,
receiving end-user access tokens, may not be within Discord's developer terms. **A human must read
the ToS before the `discord` provider ships.** If it is not permitted, `oidc` becomes the recommended
default and nothing else in this design changes — which is the payoff for making identity pluggable
in the first place.

**Unowned:** a join storm across many instances makes the shared app a heavily rate-limited Discord
client, and a ban affects every instance simultaneously. Nobody owns that today.

## 8. SSRF

Discord's URL is fixed and fine. OIDC discovery and JWKS URLs are **operator-supplied**, the classic
pivot. Mitigated by [canonical §14](00-canonical-conventions.md#14-outbound-requests) plus
`instance.security.manage` being step-up and PAT-forbidden, so a leaked token cannot add a malicious
issuer.

The dialer must deny link-local and cloud-metadata addresses, not merely RFC1918.
