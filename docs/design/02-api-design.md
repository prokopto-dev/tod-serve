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
| GET | `/me` | `getCurrentPrincipal` | self | any | Membership, circle, role, effective permissions, token prefix, scopes, expiry |
| POST | `/invites/preview` | `previewInvite` | public | — | Code **in the body, never the path**. Returns circle name, server, granted role, accepted providers, `revocation_strength`. Hard rate limit. |
| POST | `/join` | `redeemInvite` | public | — | Redeem, verify credential, create identity + membership, mint a PAT. `Idempotency-Key` required. |
| POST | `/sessions` | `authenticateIdentity` | public | — | Re-auth an existing membership on a new device, no invite. `403 membership_revoked` if revoked; `404` if no membership. |
| GET | `/tokens` | `listMyTokens` | self | any | My devices only. Officers see nobody's. |
| DELETE | `/tokens/{token_id}` | `revokeToken` | self | — | Revoke my own device |

`previewInvite` takes the code in a POST body rather than `GET /invites/{code}` because a code is a
bearer credential and a path segment lands in access logs, browser history and referrers.

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
```
