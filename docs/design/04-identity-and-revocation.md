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
| `client_id`, `client_secret`, `redirect_uri`, `token_endpoint` | The operator's **own** OAuth application — see [ADR-0011](../adr/0011-operator-registered-discord-application.md). `CHECK ((kind = 'local') = (client_id IS NULL))` |

**A `discord` row also needs its `client_secret`**, and that is checked rather than left to fail
later. The instance is a confidential client, so it performs the token exchange itself and
`discord.New` refuses to build a client without one — meaning a secretless `discord` row saved
cleanly, reported as enabled, and produced a `500` on every sign-in. It is `discord` only: an
`oidc` provider serving non-browser `id_token` clients legitimately has none, because the secret is
used only on the browser path's exchange. **Enforced by:**
`TestAddProvider_DiscordWithNoClientSecret_IsRefusedAtConfigurationTime` and
`TestAddProvider_OIDCWithNoClientSecret_IsStillAccepted`, which is the half that stops the check
growing into a false positive.

**`redirect_uri` is not free text: it must be this instance's own callback URL.** There is exactly
one string that works — `<public url>` + the path the route registry serves `completeAuthorization`
at + the provider `key` — and the server derives it rather than accepting one. Writing a row that
names anything else is refused at configuration time (`422 validation_failed` on
`body.redirect_uri`, carrying the string that would have worked), and `createAuthorizationURL`
refuses to start a flow with one before the browser leaves.

**Why this is a check and not a note in the operations guide.** The two ways of getting it wrong
fail in two places, and neither of them is here:

- The row disagrees with what is registered at the provider. The user reaches Discord and is shown
  `invalid_request` on Discord's own error page — a message about our configuration, rendered by
  somebody else.
- The row *agrees* with what is registered, and both name a host this instance is no longer at,
  which is what moving a deployment to a new domain does. Discord is satisfied, the user signs in
  and consents, and the browser is redirected to a dead origin. **No request reaches this server,
  so nothing on this server logs a failure** — the operator's only symptom is that sign-in does
  nothing. That is the silent one, and it is the reason the check exists at all.

Comparison folds what RFC 3986 folds and nothing more: scheme and host are case-insensitive and a
default port is dropped, so `https://TOD.EXAMPLE.COM:443/…` is not a finding; the path is
case-sensitive and a trailing slash is part of it, because Discord compares one literally. Folding
more would pass a configuration the provider then rejects; folding less would report a correct one
as broken, and a check with false positives is one somebody switches off.

**Enforced by:**
`TestCreateAuthorizationURL_ARedirectURIForAnotherDeployment_IsRefusedBeforeARedirectExists`,
`TestAddProvider_ARedirectURIForAnotherDeployment_IsRefusedWithTheOneThatWorks`,
`TestCanonicalRedirectURI_FoldsWhatTheSpecificationFolds_AndNothingElse`, and
`TestCallbackBaseURL_IsDerivedFromTheRouteRegistry`, which asserts the expected string is derived
from the route registry rather than written down a second time. `tod-serve doctor` reports a
mismatch before anybody tries to sign in.

**`verifiable_subject` cannot be lied about.** It is a CHECK against `kind`, not an operator toggle.
Everything downstream about revocation strength hangs off it, so it must not be settable.

`client_secret` is a `core.Secret`: it renders as `***` in `String`, `MarshalJSON` and `LogValue`,
and `listIdentityProviders` returns `client_id` but **never** the secret. This is a new asset at
rest that the shared-app design did not have, and it is named as a cost in
[ADR-0011](../adr/0011-operator-registered-discord-application.md) rather than left to be found.

**Enforced by:** the `No secret is ever logged` invariant — a test marshals the whole config and
asserts no known secret value appears.

**Every provider that talks to a third party carries a `client_id`; `local` talks to nobody and
carries none.** This CHECK read `((kind = 'discord') = (client_id IS NOT NULL))` until it was found
to contradict the `oidc` row of the table below: `aud = client_id` **is** the OIDC audience check,
and the old predicate made an `oidc` row with a client id *unrepresentable*, so `oidc` could not be
configured at all. With no audience to compare against, an ID token minted for a different relying
party at the same issuer would verify — and §7's claim that `oidc` is structurally immune to the
replay hole would quietly stop being true. Corrected in migration `000003`.

At most one `discord` row and at most one `local` row, by partial unique index. Any number of `oidc`.

| Kind | How the server verifies | Credential persisted? |
|---|---|---|
| `discord` | **`GET /oauth2/@me` first**, rejecting unless `application.id` equals this instance's `client_id` (§7) and reading the granted scopes. Then `GET /users/@me` for the subject (the snowflake `id`) and display name (`global_name ?? username`), and `GET /users/@me/guilds/{guild.id}/member` — under `guilds.members.read`, for the gated guild only — whose `404`/`200` answers membership and whose `roles` answers the role gate. | **Never.** The access token is read and discarded inside the request; only the derived facts reach `credential_ticket`. |
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

**`instance.security.manage` is instance-realm: no circle role grants it, and no PAT reaches it at
any scope.** It comes from an `instance_grant` on the caller's identity
([ADR-0012](../adr/0012-instance-grants-are-a-capability-ledger.md)), which is written by
`tod-serve instance grant` at the console — a grant names an identity, an identity is created by
joining a circle, and a fresh database has neither, so the console goes first. `tod-serve init`
prints the route from a fresh database to an administrable instance.

The instance level is `/admin/identity-providers`. `client_secret` is write-only there: it goes in
and never comes back out, and the representation says only whether one is `client_secret_set`.
`key` and `kind` are immutable — `422 field_immutable` — because `kind` decides
`verifiable_subject`, and changing it would restate what revocation means for every circle already
accepting the provider, silently. Deleting a provider is refused once anybody has joined through
it: the foreign keys are `NO ACTION`, and **disabling** it is the operation that stops new joins.

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
  { provider, invite_code? }  ->  { authorization_url, expires_at }
  0. REFUSE unless the provider's redirect_uri is this instance's own callback URL. [section 1]
     Before any row is written and before the browser leaves: a flow that cannot come back
     is one whose failure this instance never sees.
  NOTE: no circle_id. This route never takes a circle identifier. [see below]
  Writes auth_flow(state, pkce_verifier, provider_id, invite_code_hash?, circle_id?, expires_at)
  -- circle_id populated ONLY by resolving invite_code, never from caller input.
  The PKCE verifier stays SERVER-side. Expired or unknown state -> 409 auth_flow_expired.
  Scopes: identify; plus guilds.members.read when the invite's circle gates on a guild, or --
  with no invite -- when ANY enabled circle on this instance does. That is an instance-level
  fact and names no particular circle. NOT the `guilds` scope: see below.

GET  /api/v1/auth/callback/{provider_key}    completeAuthorization    Hidden: true
  1. Exchange the code (client_id + client_secret + pkce_verifier).
  2. GET /oauth2/@me  -> REJECT unless application.id == our client_id.   [see section 7]
                      -> ALSO read the GRANTED scopes. A required scope the user declined is
                         403 provider_scope_declined, NOT a role failure. [see below]
  3. GET /users/@me   -> subject, display_name.  THE IDENTITY IS NOW KNOWN.
  4. Determine which guilds need facts:
       with an invite  -> that invite's circle, if it gates on a guild;
       without one     -> the circles THIS IDENTITY already has a membership in, if they gate.
     Either way the set comes from a secret or from the verified identity -- never from a
     caller-supplied id, so there is nothing here to enumerate.
  5. GET /users/@me/guilds/{guild.id}/member, once per guild from step 4.
       200 -> the subject is in that guild; the member object carries `roles`.
       404 -> the subject is NOT in that guild.
     No call to /users/@me/guilds. One endpoint answers membership AND roles. [see below]
  6. Re-read the invite, if there was one. If it is no longer live, mint NOTHING.
  7. DISCARD the access token. Write a single-use credential_ticket (TTL 120s) carrying
     subject, display_name and guild_roles_json: gated guild id -> {member, roles}, with
     `member: false` recording a 404 and the ENTRY ABSENT when the call never settled.
     Section 8 needs those to be two different facts.
  8. 302 to  <spa>/join#ticket=<ticket>  on success,  <spa>/join#error=<code>  on failure.
     Always the FRAGMENT, never the query. [see below]

POST /api/v1/join       credential: { "kind": "provider_ticket", "ticket": "..." }
POST /api/v1/sessions   { circle_id, credential: {...} }  -- circle resolved AFTER the
                        credential verifies; no membership is still 404, per canonical section 7.
```

### A circle comes from a secret, not an identifier

`createAuthorizationURL` deliberately **takes no `circle_id`.** An earlier draft accepted one so the
re-auth flow could pick scopes up front, and that was a circle-existence oracle: a public,
pre-authentication route that answers differently for a real circle than an unknown one lets anybody
with a guessed or leaked id confirm that a rival guild runs a circle here — including through the
scope set, since `guilds.members.read` would appear only for a gated circle. That contradicts
[canonical §7](00-canonical-conventions.md#cross-circle-access-returns-404-never-403), which hides a
circle's *existence* precisely because it is competitive intelligence, and it sat outside the invite
rate limit besides.

The rule that replaces it:

> A public route resolves a circle only from a **secret the caller was given** — an invite code —
> never from an **identifier the caller could guess**. Where the circle comes from an identifier, it
> is resolved only *after* a credential has verified.

An invite code is a capability: holding it is evidence somebody handed it to you, it is looked up by
hash, and it is metered. A `circle_id` is neither secret nor scarce — it appears in URLs and
screenshots, and a former member remembers theirs.

So the re-auth path does not name a circle until it can prove who is asking. The callback learns
the identity at step 3 and then looks up **that identity's own** memberships — a lookup keyed on
something the caller proved, not something they supplied. `/sessions` still takes `circle_id`,
but it takes it *with* a credential and answers `404` when there is no membership — the existing
tenancy behaviour, unchanged.

**Enforced by:** `TestPublicRoutes_ResolveNoCircleFromCallerSuppliedId`, an architectural test over
the route registry — so a future public route that adds a circle parameter is a red test rather than
a review catch.

**Which guild, and why the flow can know it.** With an invite, `createAuthorizationURL` resolves
the circle behind the `invite_code` and reads `circle_provider.discord_guild_id` from it, recording
the result on `auth_flow`. It has to: the scope set is decided *before* the browser leaves, and
`guilds.members.read` is requested only where a guild gate actually exists. Without an invite there
is no circle to resolve yet — per the rule above — so the scope decision falls back to the
instance-level "does any enabled circle gate on a guild", and the **callback** picks the guilds once
step 3 has established who is asking.

### One endpoint, one scope

Discord splits these across two scopes, and they are not interchangeable:

| Call | Scope it needs | Returns |
|---|---|---|
| `GET /users/@me/guilds` | **`guilds`** | Partial guild objects — membership only, **no roles** |
| `GET /users/@me/guilds/{guild.id}/member` | **`guilds.members.read`** | The member object for one guild, **including `roles`**; `404` if the subject is not in it |

**This flow calls only the second, and therefore requests only `guilds.members.read`.** An earlier
draft called the guild list as well while advertising neither `guilds` nor a reason — so the call
would have failed on scope and the gate would have fallen closed against members who were genuinely
in the guild.

The guild list turned out to be unnecessary anyway. The member endpoint answers **both** questions
for the guild that actually matters: `404` is "not a member" (`guild_membership_required`), `200`
carries `roles` for the role check. That is one round trip fewer, one scope fewer on the consent
screen, and — the part worth keeping — **it learns only about the gated guild instead of harvesting
the subject's entire Discord guild list**, which is nothing this product needs to know.

Requesting `guilds` would be asking for more data to answer a narrower question.

### A declined scope is not a role failure

`GET /oauth2/@me` returns the **granted** scopes alongside `application.id`, so the callback already
holds the truth in the call it makes for the audience check. If a scope this flow needs is missing —
the user unticked it at the consent screen — the answer is `403 provider_scope_declined`.

It is emphatically **not** `guild_role_required`. Telling somebody they lack a role when the truth
is that we were never permitted to look is a confident mistake, and the two point at completely
different fixes: grant the permission, versus go ask an officer for a role you may already have.

**Enforced by:** `TestAuthorizationURL_GuildGatedCircle_RequestsGuildsMembersRead` and
`TestGuildGate_DeclinedScope_ReportsScopeDeclinedNotRoleFailure`.

**This reads an invite's circle before redemption, and that is deliberate.**
[Canonical §9](00-canonical-conventions.md#9-tenancy--this-project-diverges-from-dkp) permits
resolving an invite's circle to *parameterise authorization*; what it forbids is **binding**
`auth_flow` or `credential_ticket` to that circle as a tenancy key. The recorded `circle_id` is
advisory. **Redemption re-derives the circle from the invite and is the authority**, so a flow that
resolved one circle can never cause a join into a different one.

**It also makes this a second invite-code oracle, and that has a cost.** A public endpoint that
behaves differently for a live code than a dead one tells an attacker which codes are live — which
is precisely what `previewInvite`'s hard rate limit exists to make expensive. Standing up an
unmetered second route would have quietly repealed that defence.

So **both public routes that accept an invite code draw on one shared rate-limit bucket keyed on the
caller**, not a bucket each; two buckets is just twice the guessing budget. `createAuthorizationURL`
is additionally held to `previewInvite`'s disclosure as a **ceiling** — it may reveal no more about
a code than `previewInvite` already does — and it writes an `auth_flow` row only for a request that
passes the limit, so a rejected probe stores nothing. See
[02-api-design](02-api-design.md#one-shared-bucket-for-invite-code-probing).

*What this does not close.* The returned `authorization_url` names its scopes, and
`guilds.members.read` is requested only when the circle gates on a guild — so the URL still
distinguishes a gated circle from an ungated one to somebody who already holds a live code. Closing
that would mean requesting the maximal scope set for everyone, which puts a permission on every
consent screen that most circles never need. **Rate-limited disclosure to a holder of a valid code
is the accepted trade; over-permissioning every user to hide it is not.**

**Enforced by:** `TestInviteOracle_PreviewAndAuthorizationURL_ShareOneBucket`,
`TestCreateAuthorizationURL_RevealsNoMoreThanPreviewInvite`,
`TestAuthFlow_RateLimitedCaller_CreatesNoRows`.

### If the invite dies mid-flow

A user can sit on Discord's consent screen for minutes, so the invite can be revoked, expire or be
exhausted between `createAuthorizationURL` and the callback, or between the callback and `/join`.

| Moment | What happens |
|---|---|
| Between authorization and callback | The callback re-reads the invite. If it is no longer live it mints **no ticket** and redirects to `<spa>/join#error=invite_revoked` (or `invite_expired`, `invite_exhausted`) |
| Between callback and redemption | The ticket is valid but `/join` rejects with the same codes. The ticket is single-use and simply expires unredeemed |
| The circle *adds* a guild gate mid-flow | The authorization never requested `guilds.members.read`, so the ticket carries no member object — and an absent fact is a rejection, so the join fails closed rather than sliding past an ungated check. Restarting the flow requests the scope and succeeds |

**The callback's invite check is an early-out, not the gate.** It exists so the server does not mint
a credential for an invite that is already dead; `/join` re-checks at redemption and is what
actually decides. Anything else would make a 120-second-old snapshot authoritative over the live
row, which is the same mistake as trusting `target_state_cache`.

Everything the callback hands back rides in the **fragment** — `#ticket=…` on success,
`#error=<code>` on failure — so there is one rule for the redirect rather than one per outcome.

**A missing fact is a rejection, never a skip.** If the ticket carries no member object for a guild
the circle gates on — the call failed, or the identity held no membership the callback could have
derived the guild from — the gate returns `403 guild_role_required`. (When the *reason* is a scope
the user declined, the callback says so instead with `403 provider_scope_declined`, because that
failure has a different fix.) It does **not** treat absent roles as "no roles required".
The tempting shortcut here silently disables the gate for everyone, which is the confident-mistake
failure this project is built against.

### The ticket rides in the fragment

A `provider_ticket` is a bearer credential that mints a PAT, so it obeys the same rule as an invite
code and for the same reason. The callback redirects to `<spa>/join#ticket=…`, **never** to
`?ticket=…`: a query string lands in access logs, `Referer` headers and proxy logs, and
[canonical §7](00-canonical-conventions.md#7-http-conventions) says no token appears in a URL **with
no exception**. The SPA reads `location.hash`, POSTs the ticket, and **clears `location.hash`
immediately**.

*The callback's own query string is not an exception to that rule.* It carries `code` and `state`,
and neither is a credential for this API: `code` is single-use, PKCE-bound and exchanged server-side
within the same request, and `state` is a CSRF nonce whose only meaning is a row in `auth_flow`.

**Enforced by:** `TestNoTokenInURL_CallbackRedirectUsesFragment`, alongside the existing
query-string-token rejection test.

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

This section used to record three unresolved risks, all of them consequences of one project-wide
Discord application. **[ADR-0011](../adr/0011-operator-registered-discord-application.md) closed all
three** — though not all in the same way, and the replay one needed more than the ADR first claimed.
They are named here with their resolutions rather than deleted, because the next person to propose a
shared app needs to find out why there isn't one.

| Was | Now |
|---|---|
| **Cross-instance token replay.** One shared app meant a user's access token was valid at *every* instance, so a hostile instance could replay it elsewhere. PKCE did not help — it is a bearer token and instance-agnostic. Mitigated only by a 60-second freshness rule (`credential_stale`) | **Closed — by the audience check below, not by registration alone.** See the correction that follows: this one needed more than a per-instance `client_id` |
| **Discord's developer terms.** One application relaying arbitrary third-party self-hosted servers' end-user tokens was not obviously within them, and a human had to read the ToS before `discord` could ship | **Closed.** There is no shared application. Each operator registers their own and agrees to Discord's terms directly, exactly as any other Discord app author does |
| **Unowned operational health.** A join storm made the shared app a heavily rate-limited client, and a ban hit every instance at once | **Closed.** Rate limits and bans are per-instance, and the operator owns theirs |

### The audience check, and why registration alone is not enough

**Per-instance registration does not by itself make a Discord user access token unusable
elsewhere.** It is tempting to think it does, and the first draft of this design said so. It is
wrong. A Discord access token is a bearer token, and `GET /users/@me` honours **any** valid token
regardless of which application minted it. So a hostile instance holding a user's token can still
present it to another instance's verifier and be told which user it belongs to. The code *exchange*
is client-bound; the resource request that follows is not.

The browser flow is safe on its own — a second instance never receives a raw token, because it runs
its own exchange with its own `client_secret`. But the non-browser `bearer_token` credential accepts
a token the caller supplies, and that is the open door.

**So audience binding is explicit, and it is the mechanism:**

> Before any other call, verification of a Discord access token performs `GET /oauth2/@me` and
> rejects unless `application.id` equals this instance's configured `client_id`.
> Failure is `401 credential_audience_mismatch`.

This applies to **every** Discord access token the instance verifies — the one it just minted in the
callback, where it is redundant, and the one a client hands it on the `bearer_token` path, where it
is load-bearing. One uniform rule, because a rule with a carve-out is a rule somebody implements on
the wrong side.

That is the same job OIDC's `aud` does, which is why `oidc` never had this problem. `oidc` remains
structurally immune with no extra call.

**Enforced by:** `TestDiscord_ForeignApplicationToken_Refused`, which presents a token whose
`application.id` is not ours on **both** the callback and the `bearer_token` path and asserts `401`.

`credential_stale` was to remain on the `bearer_token` path as defence in depth: no longer the
*primary* mitigation — the audience check is — but still bounding how long a stolen same-instance
token is useful, which the audience check does not address.

**It is not implemented, and this is the honest reason.** The 60-second rule needs the token's
AGE, and `GET /oauth2/@me` reports `expires` but no issue time. Age is derivable only by assuming
Discord's token lifetime and subtracting — an assumption that stops being true silently, the day
Discord changes it, leaving a check that reports `credential_stale` for fresh tokens or accepts old
ones depending on which direction the change went. A freshness rule that is wrong in an unknown
direction is worse than no freshness rule, because it is *believed*. So the audience check carries
the whole `bearer_token` path, which §7's last bullet already said it does, and this is recorded
here rather than left as a code comment somebody has to go looking for.

**What would close it:** a token-age signal from Discord, or minting our own short-lived credential
for non-browser clients instead of accepting theirs — which is the browser flow, and is why
`bearer_token` exists on sufferance.

### What is honestly still weak

Four costs, stated rather than discovered:

- **Operator setup friction.** `discord` does not work until an operator registers an application
  and pastes a client id, a client secret and a redirect URI. That is real work on the most common
  way in, and some operators will reach for `local` instead — which is the *weak* provider. The
  mitigation is documentation and a `tod-serve doctor` check, and neither makes the step go away.
  What has been removed is the part of that friction that used to fail *silently*: the redirect URI
  is derived and checked (§1), so the commonest setup mistake is now a sentence naming the string
  to paste rather than a sign-in that lands nowhere.
  [docs/operations/discord-app.md](../operations/discord-app.md) is written for the operator who is
  part-way through registering an application rather than starting one, because that is the state
  somebody stuck is actually in.
- **A `client_secret` at rest.** The instance now holds one, which the shared-app design did not
  require. A database read is now a Discord-application compromise, not merely an identity
  disclosure. Mitigated by `core.Secret`, by `instance.security.manage` being step-up and
  PAT-forbidden, and by the no-secret-is-ever-logged test — mitigated, not removed.
- **Removing a Discord role does not revoke an already-issued PAT.** See §8. This is the one to read
  twice, because it is the gap most likely to be assumed closed.
- **The `bearer_token` path still accepts a token the caller supplies**, so its safety rests
  entirely on the audience check above rather than on the shape of the flow. The browser path does
  not need that trust. If the audience check is ever weakened or skipped, cross-instance replay is
  back — which is why it is a named test rather than a code review habit.

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
| `discord_required_role_ids_json` | `TEXT NOT NULL DEFAULT '[]'` — **an empty list means "anyone in the guild"**, and a non-empty one admits anybody holding **any** listed role |

**The instance owns the application; the circle owns the gate.** Two circles on one instance may
point at two different guilds, which is why this is not an instance setting. It is evaluated in
**both** `/join` and `/sessions`, against the guild and role ids carried on the 120-second
`credential_ticket` — never against a cached copy, and never against a client-supplied claim.
Failure is `403 guild_membership_required` or `403 guild_role_required`.

Those facts come from `GET /users/@me/guilds/{guild.id}/member` under `guilds.members.read`,
fetched during the callback for the gated guild of the invite's circle or — on the re-auth path — of
the circles the verified identity already belongs to, and carried on the ticket as
`guild_roles_json`. **One call answers both halves:** `404` is `guild_membership_required`, and the
`roles` on a `200` decide `guild_role_required`. The flow never requests the broader `guilds` scope
or the guild list it returns — see §5.

**Any one listed role admits, not all of them.** The list *widens* as it grows — `[]` is "anyone in
the guild", `["raider", "officer"]` is "anyone in the guild who is a raider or an officer" — so
"empty means anyone" is the same rule at its most permissive rather than a special case bolted on
the front. Requiring all of them would mean a circle naming two roles admitted only the people who
hold both, which is usually nobody, and the failure would be indistinguishable from a broken gate.

**If the required facts are absent, the gate rejects.** No member object means no evaluation, and no
evaluation means no entry: `403 guild_role_required`. An implementation that read an absent role
list as an empty one would disable the gate for every user while appearing to enforce it.

**`guild_roles_json` therefore records three states per guild, not two.** An entry that is *absent*
means the call was never made or did not complete; an entry with `"member": false` means Discord
answered `404`, which is a fact we hold. Collapsing those two makes `guild_membership_required`
unreportable, because the gate can no longer tell "not in the guild" from "we never looked". The
shape is `{"<guild id>": {"member": true, "roles": ["..."]}}`.

**Enforced by:** `TestEvaluateGate_EveryOutcome` and `TestFacts_RoundTripThroughTheTicketColumn` in
`internal/identity/discord`.

**Enforced by:** `TestGuildGate_EvaluatedOnJoinAndSessions` and
`TestGuildGate_MissingRoleFacts_Refused`.

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
