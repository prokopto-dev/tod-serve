# API design

**Status:** normative. **Tie-breaker:** [00-canonical-conventions.md](00-canonical-conventions.md).

Base path `/api/v1`. Path ids are ULIDs. **Permission** is the `x-tod-permission` extension;
**Scope** is `x-tod-scopes`; a dash means no PAT scope exists — session-only. Effective capability is
`role permissions ∩ token scopes`.

A dash may be followed by **`step-up:routine`** or **`step-up:sensitive`**, which is how recently
that session must have proved its identity — [canonical §6](00-canonical-conventions.md#step-up-is-a-second-question-and-it-is-graded).
The tier is always spelled out: a bare `step-up` meaning "the strict one unless somebody says
otherwise" is the implicit default that drifts. A dash with no annotation is session-only and asks
for no recency proof, which `listCircleAudit` is: no token reaches a circle's audit log, and
reading your own circle's audit log is not a privilege escalation.

Cross-circle access returns **`404`, never `403`** — see
[canonical §7](00-canonical-conventions.md#cross-circle-access-returns-404-never-403).

**`/healthz`, `/readyz` and `/metrics` sit at the root, outside `/api/v1`.** They are not API
operations: a container `HEALTHCHECK`, a load balancer and a scrape config are configured once and
must not need editing when the API version moves, and `/metrics` binds a
[separate listener](00-canonical-conventions.md#13-metrics) that has no API base path to be under.
Every other operation in this document is relative to `/api/v1`.

**Every operation below is a row in the route registry in `internal/api`,** which is the substrate
`TestTenancy_CrossCircle_EveryOperationDenies` walks. `TestRouteRegistry_MatchesTheAPIDesign` parses
these tables and compares them to that registry in both directions, so an operation added to one and
not the other is a red test rather than a review catch.

## Discovery and identity

| Method | Path | OperationID | Permission | Scope | Does |
|---|---|---|---|---|---|
| GET | `/meta` | `getServerMeta` | public | — | Version, api versions, feature flags, whether self-service circle creation is on |
| GET | `/setup` | `getSetupState` | `TOD_SETUP_TOKEN` | — | What first-run setup has to work with: whether it is still `available`, the `instance` row, the providers, the circles and how many raid targets the catalogue holds. Answers `404` for an unset or wrong token — the same answer for both — and `409` once an identity administers this instance. [ADR-0016](../adr/0016-first-run-setup-is-an-env-token-and-a-derived-window.md) |
| POST | `/setup` | `runSetup` | `TOD_SETUP_TOKEN` | — | Create the instance, its first identity provider and its first circle, seed the raid-target catalogue, and return a **one-time owner code** plus the `/join#TODI-…` path to redeem it at. Every step is create-if-absent and a second circle is refused, so a retry converges. `Idempotency-Key` required. |
| GET | `/identity-providers` | `listIdentityProviders` | public | — | Enabled providers: key, kind, display name, `verifiable_subject`, and for OIDC the issuer, client id and authorization endpoint. Never a secret. Needed *before* auth. |
| POST | `/auth/authorization-url` | `createAuthorizationURL` | public | — | Start a browser OAuth flow. `{provider, invite_code?}` → `{authorization_url, expires_at}`. **Takes no `circle_id`** — see below. Stores `auth_flow(state, pkce_verifier, …)`; the **verifier never leaves the server**. Shares `previewInvite`'s hard rate limit. |
| GET | `/auth/callback/{provider_key}` | `completeAuthorization` | public | — | The OAuth redirect target. `Hidden: true`. Exchanges the code, checks the token's audience (`GET /oauth2/@me`), reads the provider's facts, **discards the provider access token**, mints a single-use `credential_ticket`, and `302`s to `<spa>/join#ticket=…` — **fragment, never query**. |
| GET | `/me` | `getCurrentPrincipal` | self | any | Membership, circle, role, effective permissions, token prefix, scopes, expiry |
| POST | `/invites/preview` | `previewInvite` | public | — | Code **in the body, never the path**. Returns circle name, server, granted role, accepted providers, `revocation_strength`. Hard rate limit. |
| POST | `/join` | `redeemInvite` | public | — | Redeem, verify credential, create identity + membership, mint a PAT. `Idempotency-Key` required. |
| POST | `/sessions` | `authenticateIdentity` | public | — | Re-auth an existing membership on a new device, no invite. `403 membership_revoked` if revoked; `404` if no membership. |
| POST | `/sessions/step-up` | `stepUpSession` | self | — | Re-prove my identity for the session I already hold. **Mints no token and creates no device**: same session id, same expiry, fresh `stepped_up_at`. Takes no `circle_id` — the circle is the session's. `401 credential_invalid` if the credential proves a different identity from the session's. |
| DELETE | `/sessions` | `signOut` | self | — | End **my own** browser session and clear the cookie. The session id comes off the verified cookie, so no caller can name somebody else's. Writes `session_revocation`, so a cookie copied before the sign-out is refused too. **Ends this session only** and revokes no personal access token — the response says how many are still live. |
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

**Enforced by:** `TestInviteOracle_PreviewAndAuthorizationURL_ShareOneBucket` and
`TestCreateAuthorizationURL_RevealsNoMoreThanPreviewInvite` — which drives a live first-run owner
grant as well as an unissued code, because an unissued code is the one shape both routes already
agreed on. The "no `auth_flow` row
for a rejected probe" half **holds by composition rather than by its own assertion**: the limiter
runs ahead of the handler that would write the row, which
`TestInviteOracle_ARateLimitedCaller_ReachesNoHandler` proves. This paragraph named
`TestAuthFlow_RateLimitedCaller_CreatesNoRows` for that end-to-end check, and no such test has ever
existed.

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
| POST | `/circles` | `createCircle` | `instance.circle.create` | — step-up:sensitive |
| GET | `/circles/{circle_id}` | `getCircle` | `circle.read` | `circle:read` |
| PATCH | `/circles/{circle_id}` | `updateCircle` | `circle.manage` | — step-up:routine |
| PUT | `/circles/{circle_id}/providers` | `setCircleProviders` | `circle.security.manage` | — step-up:sensitive |
| DELETE | `/circles/{circle_id}` | `deleteCircle` | `circle.delete` | — step-up:sensitive |

`listCircles` returns only circles the caller is a member of; a PAT is bound to one membership, so it
returns one. **There is no "list all circles on this instance" operation, at any permission level.**
A circle's existence is part of what it is hiding.

`updateCircle` rejects `server` with `422 field_immutable`. It additionally requires
`circle.security.manage` — the owner-only key, not the `circle.manage` the rest of the operation
needs — for **`revoke_invalidates_invites`**, which decides whether revoking a weakly-revocable
member also kills the circle's live invites. Canonical §6's test for a security key is whether a
change "changes its revocation guarantee", and switching that off does exactly that: it leaves a
revoked leaker a live invite still sitting in Discord scrollback. It is the one per-field
permission in this API, and the cost is stated where it is paid — the architectural tests walk the
route registry and are blind to a rule inside a handler, so
`TestUpdateCircle_RevokeInvalidatesInvites_NeedsTheOwnerSecurityKey` is its gate. The first circle is created by CLI at
first run — `tod-serve circle create --name … --server blue` — which prints an owner code.

**That code is not an `invite`, and it cannot be.** `invite` carries `CHECK (role <> 'owner')`, so
an invite granting ownership is unrepresentable — which is the point: an invite is time-boxed,
role-capped and mintable by a bot token, and a leaked one must never seize a circle. The owner
grant is a different thing with different properties: printed once on the operator's own terminal
by a command that already holds the database, stored as a hash in `tod_meta`, single-use by
compare-and-swap, and expiring. It resolves through the same lookup as an invite, so `previewInvite`
and `/join` have one code path and a client cannot tell the two apart.

**`deleteCircle` writes a TOMBSTONE, and that is the resolution of a conflict rather than a
shortcut.** This document used to say it deletes "the circle and every report in it";
[canonical §10](00-canonical-conventions.md#10-the-report-log--non-negotiable) makes `tod_report`,
`quake_event`, `invite_redemption` and `audit_log` append-only by database trigger, and with
`foreign_keys` ON a circle holding any of those rows cannot be deleted at all — every circle
acquires an audit row on its first membership change. The invariant wins, and the report log
outliving the circle is not a compromise: it is the whole trust argument for deriving state rather
than storing it, and [canonical §11](00-canonical-conventions.md#11-retention) says ToD reports are
never pruned.

So `circle.deleted_at` is set and the rows stay. Every read carries `deleted_at IS NULL`, the
unique index on `(name_norm, server)` is partial so the name is released, an invite naming a
deleted circle answers `404 invite_invalid` — the same answer an unissued code gets — and
`internal/auth` refuses a credential bound to a membership in a deleted circle on its very next
request, which is the same rule revocation uses and for the same reason.

**This is not a way to make data go away for somebody who asks.** That is a different operation
with a different name, and it is not this one.

## Members and invites

| Method | Path | OperationID | Permission | Scope |
|---|---|---|---|---|
| GET | `/circles/{circle_id}/members` | `listMembers` | `member.read` | `member:read` |
| GET | `/circles/{circle_id}/members/{member_id}` | `getMember` | `member.read` | `member:read` |
| PATCH | `/circles/{circle_id}/members/{member_id}` | `updateMember` | `member.manage` | — step-up:sensitive |
| POST | `/circles/{circle_id}/members/{member_id}/revoke` | `revokeMember` | `member.revoke` | — step-up:sensitive |
| POST | `/circles/{circle_id}/members/{member_id}/reinstate` | `reinstateMember` | `member.revoke` | — step-up:sensitive |
| POST | `/circles/{circle_id}/service-members` | `createServiceMember` | `token.mint` | — step-up:sensitive |
| GET | `/circles/{circle_id}/invites` | `listInvites` | `invite.read` | `invite:read` |
| POST | `/circles/{circle_id}/invites` | `createInvite` | `invite.create` | `invite:create` |
| DELETE | `/circles/{circle_id}/invites/{invite_id}` | `revokeInvite` | `invite.revoke` | — step-up:routine |

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
| POST | `/circles/{circle_id}/tod-reports` | `createTodReport` | `tod.report` | `tod:report` | Append one immutable report. `Idempotency-Key` **required**. |
| GET | `/circles/{circle_id}/tod-reports` | `listTodReports` | `tod.read` | `tod:read` | Cursor; filters `target_id`, `died_after`, `died_before`, `reporter_membership_id`, `include_retracted` |
| GET | `/circles/{circle_id}/tod-reports/{report_id}` | `getTodReport` | `tod.read` | `tod:read` | |
| POST | `/circles/{circle_id}/tod-reports/{report_id}/retract` | `retractTodReport` | `tod.retract` / `tod.retract.any` | `tod:retract` | Writes a **new** retraction row. Never mutates. |
| GET | `/circles/{circle_id}/tods` | `listTargetStates` | `tod.read` | `tod:read` | **The board.** Filters `status`, `expansion`, `zone`, `contested`, `q`; sort `window_open_at`. `ETag` + `304`. |
| GET | `/circles/{circle_id}/tods/{target_id}` | `getTargetState` | `tod.read` | `tod:read` | One target: state, window, evidence, alternatives |
| POST | `/circles/{circle_id}/quakes` | `reportQuake` | `tod.quake.report` | — | Officer-only. A false quake wipes the board. `Idempotency-Key`. |
| GET | `/circles/{circle_id}/quakes` | `listQuakes` | `tod.read` | `tod:read` | |
| GET | `/circles/{circle_id}/events` | `subscribeCircleEvents` | `tod.read` | `events:subscribe` | SSE: `tod.changed`, `report.created`, `quake.reported`, `member.revoked` |
| GET | `/circles/{circle_id}/events/replay` | `replayCircleEvents` | `tod.read` | `events:subscribe` | `?since_seq=` — the only place it is legal |
| GET | `/circles/{circle_id}/audit` | `listCircleAudit` | `audit.read` | — | |

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

### `listTargetStates` — the board

Every **active** raid target, including the ones nobody has reported, because a board that hid the
targets with no ToD would be a board that could not tell you what you are not tracking. Sorted by
`window_open_at`, soonest first, with **everything that has no window after everything that does**:
nulls-first would put every unseeded target — which on a fresh instance is all of them — above the
ones a raid leader is actually waiting on.

The cursor is the last row's `target_id` and the next page is what follows it **in that sort
order**, resolved server-side over a bounded set. It is still the opaque ULID cursor canonical §4
requires; what it is not is a key the sort is on, so a row whose window moves between two pages can
be skipped or repeated. The catalogue is a hundred-odd rows fixed by the game rather than by usage,
which is what makes that trade cheap enough to take.

A board row carries the state, the window, the timer's provenance and the **evidence counts** — but
not `report_ids[]` and not `alternatives[]`. Both are the current cluster's detail, neither is in
`target_state_cache`, and rebuilding them for every target on every poll would mean clustering a
circle's whole report log to render a list. `getTargetState` has them.

**Every ETag-returning GET revalidates, in one shape**, and it is a middleware rather than a per-handler habit:
one wrapper turns any `200` whose entity tag the caller already holds into a `304`, which is why
`getCircle` and `getMember` answer one too. There is deliberately no second way to produce one: a handler
that rolled its own sent `Content-Type: application/json` describing a body it did not send, which
RFC 9110 §15.4.5 forbids and a caching proxy notices before we do. The registry's `ConditionalRead`
flag is what puts that `304` in the document, so the two must agree — asserted from both ends,
behaviourally over every driven route and structurally over every row, headers included.

A catalogue timer is instance-wide and **per server**, so `putRaidTargetTimer` and
`tod-serve seed timers --file` move the window for every circle pinned to that server that has not
overridden it — and leave alone every circle that has. The recomputation fans out over those
circles **inside the write's own transaction**, so its failure does not merely fail the request: it
rolls the moved window back with it, and there is no instant — not even one a crash could land in —
where the row moved and the boards derived from it did not.
[ADR-0013](../adr/0013-the-timer-invalidation-joins-the-writing-transaction.md) has the shape and
what it costs. The seed command is the only such write with no route, so the architectural gate
over the registry cannot see it and `TestSeedTimers_RecomputesEveryBoardTheWindowsMoved` covers it
instead.

**`status` and every countdown are re-derived on each read** and are never served from the cache.
A stored `pre_window` is stale the instant the window opens with no write in between, so the cache
holds the *point estimate* and each read renders §6 and §7 from it against `as_of` — through
`consensus.Project`, which is the same two functions `Derive` calls rather than a second copy of
them. What the cache actually buys is not reading and clustering the log.

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
  "attribution_visible": true,
  "reporters": [ { "membership_id": "01K...", "display_name": "Tankguy", "revoked": false },
                 { "membership_id": "01K...", "display_name": "Sneakco", "revoked": true } ],
  "as_of": "2026-08-19T00:31:12.412000Z" }
```

`reporters[]` is present only for principals holding `tod.read.attribution`. Revoked reporters render
with `revoked: true` and **their reports still count** — the revocation rule, made visible.

**`attribution_visible` is what says whether the principal holds it, and `reporters[]` never is.**
The two are separate fields because they answer separate questions: one is about the caller, the
other is about how much there is to name. A client reading the permission off `reporters[]` being
absent gets it wrong for every target nobody has reported yet — which on a fresh instance is all of
them, and which is what the console did until
[issue #52](https://github.com/prokopto-dev/tod-serve/issues/52). An empty `reporters[]` under
`attribution_visible: true` means "nobody has reported this target", and it is never a refusal.

## Raid-target catalogue

| Method | Path | OperationID | Permission | Scope |
|---|---|---|---|---|
| GET | `/raid-targets` | `listRaidTargets` | `catalogue.read` | `catalogue:read` |
| GET | `/raid-targets/{target_id}` | `getRaidTarget` | `catalogue.read` | `catalogue:read` |
| POST | `/raid-targets/resolve` | `resolveRaidTarget` | `catalogue.read` | `catalogue:read` |
| POST | `/raid-targets` | `createRaidTarget` | `catalogue.manage` | — step-up:routine |
| PATCH | `/raid-targets/{target_id}` | `updateRaidTarget` | `catalogue.manage` | — step-up:routine |
| PUT | `/raid-targets/{target_id}/timers/{server}` | `putRaidTargetTimer` | `catalogue.manage` | — step-up:routine |
| GET | `/circles/{circle_id}/timer-overrides` | `listCircleTimerOverrides` | `circle.manage` | — step-up:routine |
| PUT | `/circles/{circle_id}/timer-overrides/{target_id}` | `putCircleTimerOverride` | `circle.manage` | — step-up:routine |
| DELETE | `/circles/{circle_id}/timer-overrides/{target_id}` | `deleteCircleTimerOverride` | `circle.manage` | — step-up:routine |

The catalogue is instance-wide, not circle-scoped: a mob's existence is a game fact.
`listRaidTargets?server=blue` folds that server's timer into each row.

`resolveRaidTarget` ladder: exact `name` → `name_norm` → alias → alias_norm → prefix → substring.
Returns `{ target, match_kind, candidates[] }`; ties are `422 ambiguous_target`. **An exact hit is
never ranked below a substring hit** — the same discipline merchant-mode's `NameMatcher` uses, for
the same reason.

`match_kind` is a closed set, strongest first, and `id` is the rung `createTodReport` reports when
the caller sent a `target_id` and no matching happened:

```
id  name  name_normalised  alias  alias_normalised  prefix  substring
```

The first rung with any hit wins. It is never fallen through: a rung matching more than one target
is the ambiguity, because falling through is exactly how a substring outranks the exact hit above
it. The two fuzzy rungs skip `retired` targets — a retired mob stays addressable by its exact name
so a backdated report still names the right row, and must not be what a half-typed name resolves to
or the second candidate that turns a live target's match into a tie.

`meta.candidates[]` is capped, and the detail says so when it cut the list. A one-letter query
matches most of the catalogue, and a problem body carrying all of it is one no client reads.

`listRaidTargets` also takes `expansion`, `zone`, `q` and `include_retired`. `q` runs the substring
rung of the ladder above, over names **and** aliases, so a search box and a plugin cannot disagree
about what a name matches. `getRaidTarget` returns `timers[]` — every per-server window the
instance holds, which is `[]` on an unseeded one.

`putCircleTimerOverride` both creates and replaces, and the two need different preconditions. A
create has no prior tag to send, so `If-Match: *` is borrowed as "and it must NOT exist"; a replace
has one, so nothing but that tag will do and `*` is refused with `412`. Honouring the wildcard in
both directions would let an officer overwrite another officer's update having read nothing.

Every operation that moves a respawn window — `putRaidTargetTimer`, `putCircleTimerOverride`,
`deleteCircleTimerOverride`, deletion included, because falling back to the catalogue moves the
window too — pushes the change at the projection. It is the one `change_reason` the report log
cannot show: nothing is appended when a timer moves.

The catalogue timer folded into a `listRaidTargets?server=` row is the **instance-wide** one, named
`catalogue_timer` rather than `timer` deliberately: a circle override sits above it, and only the
board's own resolution applies that. Feeding this field to the derivation would ignore every
override a circle had set.

## Discord

The design is [ADR-0017](../adr/0017-discord-interactions-in-the-binary.md) and the rules the
endpoint is held to are
[04-identity §9](04-identity-and-revocation.md#9-discord-interactions-what-is-disclosed-and-where).
The operator's side is [discord-bot.md](../operations/discord-bot.md).

| Method | Path | OperationID | Permission | Scope |
|---|---|---|---|---|
| GET | `/circles/{circle_id}/discord-channels` | `listCircleDiscordChannels` | `circle.security.manage` | — step-up |
| PUT | `/circles/{circle_id}/discord-channels/{discord_channel_id}` | `bindCircleDiscordChannel` | `circle.security.manage` | — step-up |
| DELETE | `/circles/{circle_id}/discord-channels/{discord_channel_id}` | `unbindCircleDiscordChannel` | `circle.security.manage` | — step-up |
| POST | `/integrations/discord/interactions` | `handleDiscordInteraction` | `X-Signature-Ed25519` | — |

The three binding operations carry **`circle.security.manage`**, the same key as
`setCircleProviders`, because a binding is the same kind of decision: it changes who can see the
circle's data, not what the circle is called. They are in the capability floor, so no token reaches
them at any scope and the session has to be recently re-authenticated.

`bindCircleDiscordChannel` both creates and replaces, with `putCircleTimerOverride`'s precondition
rule: `If-Match: *` is "and it must NOT exist", and `*` on an existing binding is refused with
`412`. It matters more here than it does for a timer — the field being overwritten is whether this
channel may be posted into visibly, and an officer overwriting an update they have not seen is an
officer silently reversing somebody else's disclosure decision.

**A bind naming a channel that already belongs to a live circle answers `409` and names no circle.**
Redirecting it silently would move a disclosure decision a different circle's officer made, and the
members reading that channel would be the last to know; naming the circle would confirm to an
officer of one circle that another exists. `unbindCircleDiscordChannel` answers `404` for a channel
this circle does not hold, which is law 5 through the query's own `WHERE`.

### `handleDiscordInteraction`

**It authenticates a sender, not a principal.** Discord POSTs an interaction signed with the
application's Ed25519 key over `X-Signature-Timestamp` concatenated with the raw body; this server
verifies that signature and nothing else at the edge. Who typed the command is a fact *inside* the
verified payload, and it is resolved in the handler: Discord user → `identity` → `membership` in the
circle the channel binding resolved to → **that** principal's permissions. The bot holds no
credential of its own, so there is nothing for a confused deputy to spend.

**Every refusal is one `401`** — a missing header, a malformed one, a wrong key, an edited body, a
a timestamp outside the window, and an instance with no public key configured. That is two
rules at once: an unverified interaction is an unauthenticated write, so telling a forger which part
was wrong tells them what to fix — and Discord's own endpoint validation POSTs a deliberately
invalid signature when an operator saves the URL and will not accept an endpoint that answers
anything else.

**The circle is derived and never a parameter.** The route carries no `{circle_id}`, and no command
has an option that could hold one: the resolve is `channel_id → circle_discord_channel → circle`,
and the interaction's `guild_id` must equal the binding's. **Nothing keys on the server**, which is
not merely unnecessary but wrong: `membership` has no per-server uniqueness and
`ux_circle_name_norm_server` makes a name unique only within a server, so one guild can bind two
channels to two circles on blue and one person can be in both. That is why law 5 for this route is
`TestDiscordInteraction_ACrossCircleTarget_IsAnsweredAsAbsent` rather than
`TestTenancy_CrossCircle_EveryOperationDenies`, which walks routes that name a circle in their path.

**It declares `CreatesState: false` although `/tod report` appends to the log**, and that is a
stated cost rather than an oversight. `CreatesState` is what makes `Idempotency-Key` required, and
Discord does not send one — it POSTs an interaction once, the reply is the body of that response,
and there is no client-side retry to replay. What stands in its place is `ux_tod_report_natural`:
the same reporter cannot lodge the same kill twice, so a repeated command is a **replay**, and the
reply says "already recorded" rather than pretending a second row was appended.

**That index keys on `died_at`, so `died_at` cannot be a clock reading.** It is the instant the
interaction was **signed** at — inside what the Ed25519 signature covers, and therefore identical on
every replay of the same captured request. A clock reading gave the two attempts different keys, and
the same bytes replayed ninety seconds later wrote a second report ninety seconds apart.
`Verifier.Verify` returns the verified instant and the middleware carries it on the context, so a
handler has nothing else to reach for. `TestDiscordInteraction_AReplayedInteraction_AppendsOneRow`
is the gate and it advances the clock between attempts.

The signature window is **asymmetric** for the same reason: five minutes into the past bounds how
long a captured request stays useful, and `120` seconds into the future is `tod.FutureTolerance` and
the `CHECK (died_at <= reported_at + 120000000)` behind it — a wider future half would verify an
interaction whose write the database then refuses.

The response body is Discord's interaction-callback shape with `as_of` beside it. Discord ignores
the extra member; exempting the one route whose body somebody else designed would be a second answer
to "does every response carry `as_of`", and the reply renders instants that need a clock to be read
against.

**Every reply is written into the response to Discord's own POST, and there is no deferred one.**
Discord's `DEFERRED_CHANNEL_MESSAGE_WITH_SOURCE` buys more than three seconds by promising a
follow-up on a webhook — an **outbound** request, which law 6 confines to `internal/identity`. So
the three-second budget is hard here: an answer that cannot be computed inside it is one a member
sees as "the application did not respond". `/tod board` is the command with any real work in it, and
it reads the board through the same snapshot the API's own board does. If that budget ever stops
holding, the fix is a narrower command rather than a `NET001` exception — deferring would put an
outbound HTTP client in the reply path of every interaction to save one of them.

**Commands are registered by the operator, not by this binary.** Registering them is an outbound
request to Discord, and [law 6](../../AGENTS.md) confines outbound HTTP to `internal/identity`
through one guarded client. `tod-serve discord commands` prints the exact body Discord's
`PUT /applications/{id}/commands` takes, generated from the same catalogue the dispatcher switches
on. **That is also why no bot token is configured on this instance at all:** the only thing it would
have been for is a request this binary may not make.

## Instance administration

**Every permission in this table is instance-realm: no circle role grants one, and no PAT reaches
one at any scope.** They come from an `instance_grant` on the caller's identity
([ADR-0012](../adr/0012-instance-grants-are-a-capability-ledger.md)), written by `tod-serve
instance grant` at the console — which is also the recovery path, and the reason an instance needs
no `last_owner` rule. Four of the five are in the capability floor and need a re-authenticated
session; `ops.read` is not.

On `/admin/identity-providers`, `client_secret` is **write-only**: it goes in and never comes back
out, and the representation says only whether one is `client_secret_set`. `key` and `kind` are
immutable (`422 field_immutable`) because `kind` decides `verifiable_subject`, and a delete is
refused (`409 conflict`) once anybody has joined through the provider — **disabling** it is the
operation that stops new joins.

`/admin/instance` is the instance's own policy: its name, its timezone, and its stated answer to
**who may create a circle here**. That last one is what `/meta` publishes and what a client reads to
decide whether to offer the option — it is **published, not yet enforced**, because `createCircle`
declares `instance.circle.create` in the route registry unconditionally and the middleware checks it
before any handler runs. A route whose permission depends on a row is something the registry cannot
express today, which is the whole of law 1.
`TestCreateCircle_SelfServiceOn_StillRequiresTheInstanceGrant` pins the gap, so it is a stated fact
rather than an inference from a field name.

The two operations carry `instance.security.manage` rather than `instance.owner` deliberately.
`instance.owner` expands to the whole instance realm
([ADR-0015](../adr/0015-instance-owner-implies-the-instance-realm.md)), so requiring it would make
the only way to delegate this one switch a grant that also hands over the identity providers, the
catalogue and the ops dashboard — a narrower route reachable only through a wider grant. Every
owner holds `instance.security.manage` through that same expansion, so nothing an owner could do is
lost.

**Every change is recorded in `instance_setting_change`**, which is append-only and hash-chained.
`audit_log.circle_id` is `NOT NULL` and an instance-wide policy belongs to no circle, so the ledger
is its own audit record — the same answer
[ADR-0012](../adr/0012-instance-grants-are-a-capability-ledger.md) gave for `instance_grant`,
rather than a reason to skip the audit — see
[ADR-0020](../adr/0020-instance-settings-are-mutable-with-a-change-ledger.md).
`getInstanceSettings` returns the whole ledger beside the settings; `updateInstanceSettings`
returns the rows it just wrote.

**`public_url` is read-only here and is refused with `422 field_immutable`.** It must match the
redirect URI registered with every identity provider character for character; a mismatch is a
sign-in that completes at the provider and lands somewhere else, leaving no evidence on the instance
it was meant to reach. It is resolved at startup from `$TOD_PUBLIC_URL` before the row is consulted,
so a change here would take effect at some later restart. Changing it means changing that variable,
re-registering the redirect URI, and restarting — three steps `tod-serve doctor` checks together.
`instance_setting_change.setting` cannot hold `public_url`, so there is no second way in.

Neither operation requires `Idempotency-Key`: nothing is appended to the domain, and `If-Match` is
what makes a retry safe — the second attempt carries the tag of the state the first one replaced
and is refused with `412`.

**That precondition is decided inside the transaction that writes**, not against an earlier read.
Compared at handler entry, two administrators holding one tag both pass and both then commit,
appending a ledger row on a precondition that had stopped holding — and a believed audit row is
worse than a missing `412`. The comparison is handed to the service, which runs it between its own
read and its own `UPDATE`.

`getInstanceSettings` reads the settings, the revision and the ledger from ONE read snapshot. As
separate statements a writer committing between them returns the old settings beside the new
revision — a tag describing a state that never existed, which refuses the caller's next write with
`412` although nobody changed anything after their read.

The tag covers a `revision` — the settings ledger's chain head — as well as the values and
`updated_at`. `updated_at` is a clock reading rather than a revision: two commits can share a
microsecond, and if the second restores what the first replaced then every other field returns to
its old value and the tag repeats. A revalidating client would be told `304` with its copy two
ledger rows behind. A chain hash covers each row's own ULID and `ux_instance_setting_change_hash`
forbids a duplicate, so it cannot.

| Method | Path | OperationID | Permission | Scope |
|---|---|---|---|---|
| GET | `/admin/instance` | `getInstanceSettings` | `instance.security.manage` | — step-up:sensitive |
| PATCH | `/admin/instance` | `updateInstanceSettings` | `instance.security.manage` | — step-up:sensitive |
| GET | `/admin/identity-providers` | `listAdminIdentityProviders` | `instance.security.manage` | — step-up:sensitive |
| POST | `/admin/identity-providers` | `createIdentityProvider` | `instance.security.manage` | — step-up:sensitive |
| PATCH | `/admin/identity-providers/{provider_id}` | `updateIdentityProvider` | `instance.security.manage` | — step-up:sensitive |
| DELETE | `/admin/identity-providers/{provider_id}` | `deleteIdentityProvider` | `instance.security.manage` | — step-up:sensitive |
| GET | `/admin/doctor` | `getDoctorReport` | `ops.read` | — |
| GET | `/admin/jobs` | `listJobs` | `ops.read` | — |
| GET | `/healthz` | `getLiveness` | public | — |
| GET | `/readyz` | `getReadiness` | public | — |
| GET | `/metrics` | `getMetrics` | `TOD_METRICS_TOKEN` | — |

## Error codes

`type` is `https://docs.tod-serve.org/errors/<code>` — the last segment **is** the code, and
`TestErrorCodes_EveryCode_HasADocumentationPage` asserts every code below has a page in
`docs/errors/`, in **both** directions: a page for a code nobody can emit is as wrong as a code
with no page.

The generic set. These are the failures the edge itself produces — authentication, authorization,
concurrency, idempotency and validation — before any domain code runs:

```
malformed_request (400)          unauthenticated (401)            token_invalid (401)
token_expired (401)              forbidden (403)                  insufficient_scope (403)
session_required (403)           step_up_required (403)           not_found (404)
method_not_allowed (405)         not_acceptable (406)             request_timeout (408)
conflict (409)                   precondition_failed (412)        payload_too_large (413)
unsupported_media_type (415)     validation_failed (422)          precondition_required (428)
idempotency_key_required (400)   idempotency_key_reused (422)     idempotency_conflict (409)
rate_limited (429)               internal_error (500)             service_unavailable (503)
```

Three of those splits exist because the two halves have **different fixes**, and a client that
cannot tell them apart retries the one that will never succeed:

- **`forbidden` vs `insufficient_scope`.** The role does not hold the permission — ask an officer —
  versus the token does not carry the scope — mint a token that does.
- **`session_required` vs `step_up_required`.** A PAT reached a capability-floor operation, which no
  token reaches at any scope — open a browser — versus a session that has not re-authenticated
  recently enough — re-authenticate.
- **`idempotency_key_reused` vs `idempotency_conflict`.** The same key arrived with a **different**
  request, which is a client bug — versus a request with that key is still in flight, which is a
  retry that should simply wait.

And the domain set, beyond the generic one above:

```
membership_revoked (403)         invite_invalid (404)          invite_expired (409)
invite_exhausted (409)           invite_revoked (409)          provider_not_accepted (409)
provider_disabled (409)          provider_unverifiable (422)   credential_invalid (401)
credential_expired (401)         credential_stale (401)        identity_provider_unreachable (503)
acknowledgement_required (422)   server_mismatch (422)         died_at_in_future (422)
died_at_too_old (422)            already_retracted (409)       retract_not_permitted (403)
unknown_target (422)             ambiguous_target (422)
last_owner (409)                 field_immutable (422)         link_requires_verifiable_identity (422)
guild_membership_required (403)  guild_role_required (403)     auth_ticket_invalid (401)
auth_ticket_expired (401)        auth_flow_expired (409)       identity_blocked (403)
credential_audience_mismatch (401)
provider_scope_declined (403)
```

A `provider_ticket` is a bearer credential, so it reaches the SPA in the redirect **fragment** and
never in a query string — the same rule, and the same reason, as the invite link above.
`credential_audience_mismatch` is the check that makes a per-instance Discord application actually
close cross-instance replay; see
[04-identity §7](04-identity-and-revocation.md#7-the-discord-trust-boundary-after-adr-0011).
