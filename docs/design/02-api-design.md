# API design

**Status:** normative. **Tie-breaker:** [00-canonical-conventions.md](00-canonical-conventions.md).

Base path `/api/v1`. Path ids are ULIDs. **Permission** is the `x-tod-permission` extension;
**Scope** is `x-tod-scopes`; a dash means no PAT scope exists — session-only. Effective capability is
`role permissions ∩ token scopes`.

Cross-circle access returns **`404`, never `403`** — see
[canonical §7](00-canonical-conventions.md#cross-circle-access-returns-404-never-403).

## Discovery and identity

| Method | Path | OperationID | Permission | Scope | Does |
|---|---|---|---|---|---|
| GET | `/meta` | `getServerMeta` | public | — | Version, api versions, feature flags, whether self-service circle creation is on |
| GET | `/identity-providers` | `listIdentityProviders` | public | — | Enabled providers: key, kind, display name, `verifiable_subject`, and for OIDC the issuer, client id and authorization endpoint. Never a secret. Needed *before* auth. |
| POST | `/auth/authorization-url` | `createAuthorizationURL` | public | — | Start a browser OAuth flow. `{provider, invite_code?}` → `{authorization_url, expires_at}`. **Takes no `circle_id`** — see below. Stores `auth_flow(state, pkce_verifier, …)`; the **verifier never leaves the server**. Shares `previewInvite`'s hard rate limit. |
| GET | `/auth/callback/{provider_key}` | `completeAuthorization` | public | — | The OAuth redirect target. `Hidden: true`. Exchanges the code, checks the token's audience (`GET /oauth2/@me`), reads the provider's facts, **discards the provider access token**, mints a single-use `credential_ticket`, and `302`s to `<spa>/join#ticket=…` — **fragment, never query**. |
| GET | `/me` | `getCurrentPrincipal` | self | any | Membership, circle, role, effective permissions, token prefix, scopes, expiry |
| POST | `/invites/preview` | `previewInvite` | public | — | Code **in the body, never the path**. Returns circle name, server, granted role, accepted providers, `revocation_strength`. Hard rate limit. |
| POST | `/join` | `redeemInvite` | public | — | Redeem, verify credential, create identity + membership, mint a PAT. `Idempotency-Key` required. |
| POST | `/sessions` | `authenticateIdentity` | public | — | Re-auth an existing membership on a new device, no invite. `403 membership_revoked` if revoked; `404` if no membership. |
| GET | `/tokens` | `listMyTokens` | self | any | My devices only. Officers see nobody's. |
| DELETE | `/tokens/{token_id}` | `revokeToken` | self | — | Revoke my own device |

`previewInvite` takes the code in a POST body rather than `GET /invites/{code}` because a code is a
bearer credential and a path segment lands in access logs, browser history and referrers.

**An invite link carries its code in the URL *fragment*, never a path segment:**
`https://tod.example.com/join#TODI-4KQ7M-9XPB2`. A fragment is never sent to any server — not to
ours, not to a proxy, and not in a `Referer` — which is the same reason the code travels in a POST
body one paragraph up, applied to the link an officer actually pastes into Discord. The SPA reads
`location.hash`, POSTs it to `previewInvite`, and **clears `location.hash` immediately** so a
screenshot or a shared browser tab does not leak it. A one-time login link is this and nothing more:
an invite with `max_uses = 1`.

### One shared bucket for invite-code probing

`createAuthorizationURL` resolves the supplied invite so it can pick the OAuth scopes and the guild
to check ([04-identity §5](04-identity-and-revocation.md#5-one-join-endpoint)). That makes it a
**second oracle for invite-code validity**, next to the one `previewInvite`'s hard rate limit exists
to defend. Two buckets would simply hand an attacker twice the guessing budget, so:

**Both public routes that accept an invite code are metered from a single shared bucket, keyed on
the caller, not one bucket per route.** Exhaustion is the generic `429`. Adding a third route that
takes a code means joining that bucket, not minting another.

`createAuthorizationURL` must also **reveal no more about a code than `previewInvite` already
does** — it is held to that endpoint's disclosure as a ceiling, rather than being separately
reasoned about. It creates an `auth_flow` row only for a request that passes the limit, so a
rejected probe costs the instance nothing to store.

**Enforced by:** `TestInviteOracle_PreviewAndAuthorizationURL_ShareOneBucket`,
`TestCreateAuthorizationURL_RevealsNoMoreThanPreviewInvite` and
`TestAuthFlow_RateLimitedCaller_CreatesNoRows`.

### No public route resolves a caller-supplied `circle_id`

Metering the invite code is not enough on its own if the same route accepts a *second* input that
identifies a circle. `createAuthorizationURL` therefore takes **no `circle_id` at all**: answering
differently for a real circle than an unknown one — including through which scopes the returned URL
requests — would confirm a circle's existence to anybody who guessed or obtained an id, which
[canonical §7](00-canonical-conventions.md#cross-circle-access-returns-404-never-403) exists to
prevent, and it would sit outside the bucket above.

**A public route resolves a circle only from a secret the caller was given — an invite code — never
from an identifier they could guess.** Where a circle does come from an identifier, it is resolved
only after a credential has verified: `authenticateIdentity` takes `circle_id` *with* a credential
and returns `404` when there is no membership, exactly as every other circle-scoped operation does.

The re-auth flow gets its circles from the verified identity's own memberships inside the callback:
[04-identity §5](04-identity-and-revocation.md#a-circle-comes-from-a-secret-not-an-identifier).

**Enforced by:** `TestPublicRoutes_ResolveNoCircleFromCallerSuppliedId`, over the route registry.

### The credential union

`credential` is a discriminated union on `kind`, identical in `redeemInvite` and
`authenticateIdentity` — see [ADR-0007](../adr/0007-one-join-endpoint.md) and
[04-identity §5](04-identity-and-revocation.md#5-one-join-endpoint):

| `kind` | Carries | Used by |
|---|---|---|
| `provider_ticket` | `ticket` — from `completeAuthorization`, single-use, 120-second TTL, delivered in the redirect **fragment** | Any browser flow: `discord` **and** `oidc` |
| `bearer_token` | `token` — audience-checked against this instance's `client_id` before anything else | Non-browser `discord` clients |
| `id_token` | `id_token` + `nonce` | Non-browser `oidc` clients |
| `none` | nothing | `local` |

`provider_ticket` exists because [ADR-0011](../adr/0011-operator-registered-discord-application.md)
makes the instance a confidential OAuth client, so the token exchange must happen server-side. Both
browser providers therefore land on one ticket and **the SPA has a single code path**; `id_token`
and `bearer_token` remain for clients that have no browser to redirect.

## Circles

| Method | Path | OperationID | Permission | Scope |
|---|---|---|---|---|
| GET | `/circles` | `listCircles` | self | `circle:read` |
| POST | `/circles` | `createCircle` | `instance.circle.create` | — |
| GET | `/circles/{circle_id}` | `getCircle` | `circle.read` | `circle:read` |
| PATCH | `/circles/{circle_id}` | `updateCircle` | `circle.manage` | — |
| PUT | `/circles/{circle_id}/providers` | `setCircleProviders` | `circle.security.manage` | — |
| DELETE | `/circles/{circle_id}` | `deleteCircle` | `circle.delete` | — step-up |

`listCircles` returns only circles the caller is a member of; a PAT is bound to one membership, so it
returns one. **There is no "list all circles on this instance" operation, at any permission level.**
A circle's existence is part of what it is hiding.

`updateCircle` rejects `server` with `422 field_immutable`. The first circle is created by CLI at
first run — `tod-serve circle create --name … --server blue` — which prints an owner invite code.

## Members and invites

| Method | Path | OperationID | Permission | Scope |
|---|---|---|---|---|
| GET | `/circles/{cid}/members` | `listMembers` | `member.read` | `member:read` |
| GET | `/circles/{cid}/members/{mid}` | `getMember` | `member.read` | `member:read` |
| PATCH | `/circles/{cid}/members/{mid}` | `updateMember` | `member.manage` | — step-up |
| POST | `/circles/{cid}/members/{mid}/revoke` | `revokeMember` | `member.revoke` | — step-up |
| POST | `/circles/{cid}/members/{mid}/reinstate` | `reinstateMember` | `member.revoke` | — step-up |
| POST | `/circles/{cid}/service-members` | `createServiceMember` | `token.mint` | — step-up |
| GET | `/circles/{cid}/invites` | `listInvites` | `invite.read` | `invite:read` |
| POST | `/circles/{cid}/invites` | `createInvite` | `invite.create` | `invite:create` |
| DELETE | `/circles/{cid}/invites/{iid}` | `revokeInvite` | `invite.revoke` | — |

Tokens are minted by `/join`, `/sessions` and `createServiceMember` only. There is no "mint me an
arbitrary token" operation and no `admin:*` scope.

**PATs are bound to a membership, not a service account.** This is a documented divergence from DKP
[ADR-0011](https://github.com/prokopto-dev/dragonkillparty/blob/main/docs/adr/0011-opaque-pats-no-superadmin-token.md)
rule 1 — see [ADR-0005](../adr/0005-pats-bound-to-memberships.md). A bot gets a `kind='service'`
membership with a human `owner_membership_id`, so "the audit names a responsible human" survives and
there is still exactly one principal kind in the authz path.

`revokeMember` returns the membership representation — which carries `revocation_strength` — plus
`active_invite_count`, so the UI can say "you also have 2 live invites". No separate warnings channel
is invented; the representation already says it.

## ToD reports and derived state

| Method | Path | OperationID | Permission | Scope | Does |
|---|---|---|---|---|---|
| POST | `/circles/{cid}/tod-reports` | `createTodReport` | `tod.report` | `tod:report` | Append one immutable report. `Idempotency-Key` **required**. |
| GET | `/circles/{cid}/tod-reports` | `listTodReports` | `tod.read` | `tod:read` | Cursor; filters `target_id`, `died_after`, `died_before`, `reporter_membership_id`, `include_retracted` |
| GET | `/circles/{cid}/tod-reports/{rid}` | `getTodReport` | `tod.read` | `tod:read` | |
| POST | `/circles/{cid}/tod-reports/{rid}/retract` | `retractTodReport` | `tod.retract` / `tod.retract.any` | `tod:retract` | Writes a **new** retraction row. Never mutates. |
| GET | `/circles/{cid}/tods` | `listTargetStates` | `tod.read` | `tod:read` | **The board.** Filters `status`, `expansion`, `zone`, `contested`, `q`; sort `window_open_at`. `ETag` + `304`. |
| GET | `/circles/{cid}/tods/{tid}` | `getTargetState` | `tod.read` | `tod:read` | One target: state, window, evidence, alternatives |
| POST | `/circles/{cid}/quakes` | `reportQuake` | `tod.quake.report` | — | Officer-only. A false quake wipes the board. `Idempotency-Key`. |
| GET | `/circles/{cid}/quakes` | `listQuakes` | `tod.read` | `tod:read` | |
| GET | `/circles/{cid}/events` | `subscribeCircleEvents` | `tod.read` | `events:subscribe` | SSE: `tod.changed`, `report.created`, `quake.reported`, `member.revoked` |
| GET | `/circles/{cid}/events/replay` | `replayCircleEvents` | `tod.read` | `events:subscribe` | `?since_seq=` — the only place it is legal |
| GET | `/circles/{cid}/audit` | `listCircleAudit` | `audit.read` | — step-up | |

### `createTodReport`

```json
{ "target_id": "01K3TGT...",
  "target_name": "Vulak`Aerr",
  "server": "blue",
  "died_at": "2026-08-18T02:14:07.000000Z",
  "source": "log_line",
  "source_line": "[Mon Aug 18 02:14:07 2026] Vulak`Aerr has been slain by Tankguy!",
  "source_character": "Tankguy",
  "log_character": "Tankgal",
  "killed_by_guild": "Riot",
  "self_confidence": "certain",
  "client_clock_offset_seconds": -3 }
```

Exactly one of `target_id` / `target_name` is required. `target_name` runs the resolve ladder, and
`422 ambiguous_target` carries `meta.candidates[]` — **so the plugin can send the parsed name and
never has to hold a catalogue.** `server` must match the circle's or `422 server_mismatch`.

### `getTargetState`

```json
{ "target": { "id": "01K3TGT...", "name": "Vulak`Aerr", "zone": "Temple of Veeshan",
              "expansion": "velious", "category": "ntov" },
  "server": "blue",
  "status": "in_window",
  "died_at": "2026-08-18T02:14:07.000000Z",
  "window": { "kind": "variance", "open_at": "...", "close_at": "...", "spawn_at": null,
              "progress_bp": 3742, "seconds_until_open": -16214, "seconds_until_close": 26986 },
  "timer_source": "circle_override",
  "confidence": "high",
  "contested": false,
  "contest_reason": null,
  "alternatives": [],
  "evidence": { "report_count": 4, "distinct_reporter_count": 3, "log_line_count": 2,
                "spread_seconds": 190, "revoked_reporter_count": 1, "report_ids": ["01K..."] },
  "reporters": [ { "membership_id": "01K...", "display_name": "Tankguy", "revoked": false },
                 { "membership_id": "01K...", "display_name": "Sneakco", "revoked": true } ],
  "as_of": "2026-08-19T00:31:12.412000Z" }
```

`reporters[]` is present only for principals holding `tod.read.attribution`. Revoked reporters render
with `revoked: true` and **their reports still count** — the revocation rule, made visible.

## Raid-target catalogue

| Method | Path | OperationID | Permission | Scope |
|---|---|---|---|---|
| GET | `/raid-targets` | `listRaidTargets` | `catalogue.read` | `catalogue:read` |
| GET | `/raid-targets/{tid}` | `getRaidTarget` | `catalogue.read` | `catalogue:read` |
| POST | `/raid-targets/resolve` | `resolveRaidTarget` | `catalogue.read` | `catalogue:read` |
| POST | `/raid-targets` | `createRaidTarget` | `catalogue.manage` | — |
| PATCH | `/raid-targets/{tid}` | `updateRaidTarget` | `catalogue.manage` | — |
| PUT | `/raid-targets/{tid}/timers/{server}` | `putRaidTargetTimer` | `catalogue.manage` | — |
| GET · PUT · DELETE | `/circles/{cid}/timer-overrides[/{tid}]` | `listCircleTimerOverrides` · `putCircleTimerOverride` · `deleteCircleTimerOverride` | `circle.manage` | — |

The catalogue is instance-wide, not circle-scoped: a mob's existence is a game fact.
`listRaidTargets?server=blue` folds that server's timer into each row.

`resolveRaidTarget` ladder: exact `name` → `name_norm` → alias → alias_norm → prefix → substring.
Returns `{ target, match_kind, candidates[] }`; ties are `422 ambiguous_target`. **An exact hit is
never ranked below a substring hit** — the same discipline merchant-mode's `NameMatcher` uses, for
the same reason.

## Instance administration

| Method | Path | OperationID | Permission |
|---|---|---|---|
| GET · POST · PATCH · DELETE | `/admin/identity-providers[/{pid}]` | `listAdminIdentityProviders` · `createIdentityProvider` · `updateIdentityProvider` · `deleteIdentityProvider` | `instance.security.manage` — step-up |
| GET | `/admin/doctor` · `/admin/jobs` | `getDoctorReport` · `listJobs` | `ops.read` |
| GET | `/healthz` · `/readyz` | `getLiveness` · `getReadiness` | public |
| GET | `/metrics` | `getMetrics` | `TOD_METRICS_TOKEN` |

## Error codes

Beyond the generic set. `type` is `https://docs.tod-serve.org/errors/<code>` — the last segment **is**
the code, and a test asserts every code has a page.

```
membership_revoked (403)         invite_invalid (404)          invite_expired (409)
invite_exhausted (409)           invite_revoked (409)          provider_not_accepted (409)
provider_disabled (409)          provider_unverifiable (422)   credential_invalid (401)
credential_expired (401)         credential_stale (401)        identity_provider_unreachable (503)
acknowledgement_required (422)   server_mismatch (422)         died_at_in_future (422)
died_at_too_old (422)            report_immutable (409)        already_retracted (409)
retract_not_permitted (403)      unknown_target (422)          ambiguous_target (422)
last_owner (409)                 field_immutable (422)         link_requires_verifiable_identity (422)
guild_membership_required (403)  guild_role_required (403)     auth_ticket_invalid (401)
auth_ticket_expired (401)        auth_flow_expired (409)       identity_blocked (403)
credential_audience_mismatch (401)
```

A `provider_ticket` is a bearer credential, so it reaches the SPA in the redirect **fragment** and
never in a query string — the same rule, and the same reason, as the invite link above.
`credential_audience_mismatch` is the check that makes a per-instance Discord application actually
close cross-instance replay; see
[04-identity §7](04-identity-and-revocation.md#7-the-discord-trust-boundary-after-adr-0011).
