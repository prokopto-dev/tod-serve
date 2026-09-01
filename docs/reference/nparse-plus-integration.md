# Building an nParse+ client against a tod-serve instance

**Status:** reference. **Audience:** somebody — or something — writing a client in a **different
repository**, with no access to this one and no prior context.

That audience is the constraint this document is written under. You cannot ask a follow-up
question, you cannot read `internal/`, and you will believe what is written here. So every claim
below names the file, the generated document or the command that proves it, and where this
repository's own prose disagrees with the generated spec, **the spec wins**.

Read [§0](#0-how-to-check-everything-in-this-document) before anything else, then
[§2](#2-credentials--the-part-that-is-usually-got-wrong) and
[§8](#8-the-hard-rule-never-invent-a-p99-log-format).

---

## 0. How to check everything in this document

### The authority

`openapi/openapi.json` in this repository is the authority for every route, field name, enum value
and error code. It is **generated** from the route registry (`make gen-openapi`) and checked in, so
it cannot drift from the server without a failing build.

Get it from the repository at the version your instance runs:

```
https://github.com/prokopto-dev/tod-serve/blob/main/openapi/openapi.json
```

**The running instance does not serve it.** `internal/api/server.go` sets `config.OpenAPIPath = ""`,
`config.SchemasPath = ""` and `config.DocsPath = ""`. There is no `/api/v1/openapi.json`, no
`/docs`, no Swagger UI. Do not write a client that fetches its own schema at runtime; there is
nothing to fetch.

`GET /api/v1/meta` returns the instance's `version` and its `api_versions`. Compare that against the
tag whose `openapi.json` you took.

### Recipes

Everything in this document is reproducible with `jq` against that one file.

| Question | Command |
|---|---|
| Every operation | `jq -r '.paths \| to_entries[] \| .key as $p \| .value \| to_entries[] \| select(.key\|test("get\|post\|put\|patch\|delete")) \| "\(.key\|ascii_upcase) \($p) \(.value.operationId)"' openapi/openapi.json` |
| One operation, whole | `jq '.paths["/api/v1/circles/{circle_id}/tod-reports"].post' openapi/openapi.json` |
| A request or response schema | `jq '.components.schemas.CreateTodReportInputBody' openapi/openapi.json` |
| The scopes an operation needs | `jq -r '.paths[][]? \| select(.operationId=="createTodReport") \| .["x-tod-scopes"]' openapi/openapi.json` |
| Every error code that exists | `jq -r '.components.schemas.Problem.properties.code.enum[]' openapi/openapi.json` |
| Which routes need `Idempotency-Key` | `jq -r '.paths \| to_entries[] \| .key as $p \| .value \| to_entries[] \| select(.value["x-tod-idempotency"] // "" \| length > 0) \| "\($p) \(.value.operationId) \(.value["x-tod-idempotency"])"' openapi/openapi.json` |
| Which operations carry an entity tag (the reads answer `304`) | `jq -r '.paths[][]? \| select(.["x-tod-etag"]==true) \| .operationId' openapi/openapi.json` |
| Which routes a PAT can never reach | `jq -r '.paths[][]? \| select(.["x-tod-session-only"]==true) \| .operationId' openapi/openapi.json` |

The `x-tod-*` extensions are generated from the same permission catalogue the middleware enforces.
They are the machine-readable form of everything §2 and §6 say in prose. Prefer them to this page.

### The four places the spec is silent or understates

These are the only places you must not simply trust a client generated from `openapi.json`. Each is
checkable in one command.

1. **`Idempotency-Key` is required on some POSTs and is not a declared parameter.**
   `grep -c 'Idempotency-Key' openapi/openapi.json` → `0`. Only the *response* header
   `Idempotency-Replayed` is declared. The machine-readable signal for "this route requires the
   request header" is `x-tod-idempotency` being a non-empty string. A generated client will omit
   the header and every such POST will answer `400 idempotency_key_required`.
   See [§6](#6-idempotency).
2. **Several timestamps are `null` on the wire while typed as a non-nullable `string`.** The schema
   generator aliases the timestamp type to a fixed string schema, which loses the nullability of an
   optional field. See [§4](#nullable-timestamps) for the list. A strict decoder will throw on a
   perfectly valid response.
3. **The SSE event stream does not exist yet.** `docs/design/02-api-design.md` describes
   `subscribeCircleEvents` and `replayCircleEvents`; neither is in the spec
   (`jq -r '.paths[][]?.operationId' openapi/openapi.json | grep -c Events` → `0`). They are
   declared in the route registry with no handler. **Poll.** See [§5](#polling-and-etags).
4. **The device authorization flow does not exist yet.**
   See [§9](#9-not-yet-available--the-device-flow).

---

## 1. Base URL, versioning and wire conventions

`servers[0].url` in the spec is `/api/v1` — a *relative* URL, because the instance's host is the
operator's, not ours. Your base URL is:

```
<instance public URL> + /api/v1
```

for example `https://tod.example.com/api/v1`. The operator configures the public URL as
`TOD_PUBLIC_URL`; ask them for it, do not derive it.

**Three paths sit at the root, outside `/api/v1`,** and are not API operations: `/healthz`,
`/readyz` and `/metrics`. `/metrics` is on a separate listener with its own credential and is none
of your business. A container health check and a load balancer are configured once and must not
need editing when the API version moves — which is why they are not versioned.

| Convention | Value |
|---|---|
| Request body | `application/json`. A wrong `Content-Type` is `415 unsupported_media_type` |
| Response body | `application/json` |
| Error body | `application/problem+json` (RFC 9457) — see [§7](#7-errors-rfc-9457-and-a-closed-code-enum) |
| `Accept` | Must admit `application/json` (`*/*` and `application/*` both do). Otherwise `406 not_acceptable` |
| Timestamps | RFC 3339 with **microsecond** precision, always `Z`: `2026-08-18T02:14:07.000000Z` |
| Identifiers | ULID: 26 characters of Crockford base32, `^[0-9A-HJKMNP-TV-Z]{26}$`, lexicographically time-ordered |
| Enums | The wire value is the database value: lowercase `snake_case`, no translation layer |
| Ratios | Integer **basis points**, `_bp`, 10000 = 100%. There are no floats in any window computation |
| Pagination | Body envelope `{items, next_cursor, has_more, as_of}`. Opaque cursor; `limit` is 1–200. Never a `Link` header |
| Correlation | Every response carries `X-Request-Id`; a problem body repeats it as `meta.request_id`. **Log it** — it is what an operator greps for |

`GET /api/v1/meta` (`getServerMeta`) is public and unauthenticated. Use it as your reachability
check and your version check. Response: `ServerMeta` — `name`, `version`, `api_versions`,
`configured`, `self_service_circle_creation`, `setup_available`, `as_of`.

---

## 2. Credentials — the part that is usually got wrong

### Persistent credentials already work. Do not build a per-session login.

**This is the answer to "I don't want to log in every time I open nParse+".** The obvious
assumption — that signing in with Discord means re-authenticating each session — is wrong, and a
client built on it is a worse client.

- `api_token.expires_at` is **nullable**. A personal access token can be non-expiring.
- **No route in this server sets a token TTL today.** `internal/membership/token.go` writes
  `expires_at` only when the caller passes `ttl > 0`; `grep -rn "TokenTTL" .` finds the struct
  fields and the two call sites and **no assignment anywhere**. `POST /join` builds its request
  without one.
- Therefore every token this server mints today comes back as `"expires_at": null` and **never
  expires**. (Verified by marshalling the mint result: the wire form is
  `{"id":…,"token":…,"token_prefix":…,"name":…,"scopes":[…],"expires_at":null,"created_at":…}`.)

So the shape of a correct client is:

> Redeem an invite **once**. Store the token. Send it on every request. Never sign in again.

Do not implement a refresh loop. Do not re-run the join flow on startup. Do not open a browser
because your process restarted. There is no refresh token because there is nothing to refresh.

**What ends a token is not time.** It is revocation, and it is checked on every request — see
[§3](#3-membership-is-checked-on-every-request). Your client must still handle a stored, previously
working credential starting to fail. That is the trade this design makes: no expiry, immediate
revocation.

### Token format

```
tods_pat_<8-char public prefix>_<43 chars base64url>
```

- Scheme: the literal `tods_pat_`, deliberately greppable so a leaked credential can be found in a
  paste or a repository scan.
- Prefix: **8 characters** of Crockford base32 (5 random bytes, no padding). The alphabet excludes
  `I`, `L`, `O` and `U`, so a prefix is safe to read aloud.
- Secret: **43 characters** of unpadded base64url over 32 random bytes. Stored server-side only as
  `HMAC-SHA256(instance pepper, secret)`.
- ADR-0005 (`docs/adr/0005-pats-bound-to-memberships.md`) is where the format is fixed.

**The 8-character prefix is loggable. The secret never is.**

That is not a style preference. The prefix is the *only* thing that makes a leaked token traceable
back to the device it was minted for — it is what the server logs, and what an operator will ask
you for. A client that logs the whole token has destroyed the property the format exists to
provide, and has written a live credential into a file that ends up in a bug report.

Concretely:

- Log `token_prefix`. Never log the token, never put it in a crash dump, an analytics event, a
  telemetry payload or a URL.
- Store it in the OS keychain where one exists; otherwise a file the user owns with mode `0600`.
- Send it **only** as `Authorization: Bearer tods_pat_…`.
- **A token in a query string is refused with `401 unauthenticated`,** on every route, before the
  rate limiter and before authentication, under any parameter name at all. There is no compatibility
  shim. Do not try.

### One token per circle, and a person may be in any number of them

ADR-0005 binds a PAT to a **membership**, not to a person and not to an instance. So the number of
tokens a user holds is the number of **circles** they belong to. Get the shape right, because the
obvious simplification is wrong:

| Relationship | Cardinality |
|---|---|
| circle → server | **Many-to-one, and immutable** ([ADR-0009](../adr/0009-circle-pinned-to-one-server.md)). A circle is pinned to one server for life |
| person → circle | **Many-to-many, with no per-server limit.** `membership` carries no `server` column, and its only uniqueness is `(circle_id, identity_id)` — one membership per circle, and nothing stopping several circles on one server |
| circle name → server | Unique per server (`(name_norm, server)` where not deleted). **On one server circles are told apart by name; across servers the same name may repeat** |

So a user may hold three tokens for their guild's Blue circle, an allied Blue circle and a Green
circle — **two of them on the same server**. That is an ordinary case, not a corner one.

- **Key your credential store on `circle_id`, and on nothing else.** It is a set, not a map from
  server to token. A store keyed or deduplicated by server silently loses one of two Blue
  memberships, and the user's reports go to the wrong circle.
- Store `(circle_id, circle name, server, token)` together, but treat only `circle_id` as the
  identity. `server` is an attribute of the circle you must send back on a report; it identifies
  nothing.
- **Never label a circle by server alone** in any UI you build. Two rows can both say "blue". Show
  the name and the server together.
- With the scope set this document recommends you cannot list circles at all
  (`listCircles` needs `circle:read`, which you should not request). `GET /me` is how a token tells
  you which circle it is for.
- There is no "list every circle on this instance" operation at any permission level, by design. A
  circle's existence is part of what it hides.

`GET /api/v1/me` (`getCurrentPrincipal`) works with **any** scope (`x-tod-any-scope: true`) and
returns `PrincipalView`: `circle_id`, `membership_id`, `role`, `display_name`, `permissions[]`,
`scopes[]`, `token_prefix`, `token_expires_at`, `as_of`. Call it once after storing a token, to
learn the `circle_id` every other route needs, and to confirm the scopes you were actually granted.

### Getting a token today: preview, then join

**Step 1 — `POST /api/v1/invites/preview`** (`previewInvite`, public). The code travels in the
**body**, never in a path segment, because a code is a bearer credential and a path lands in access
logs, browser history and `Referer` headers.

Request `PreviewInviteInputBody`; response `InvitePreview`, carrying `circle`, `granted_role`,
`kind` (`invite`, or `owner_grant` for the code the CLI prints on first run), `max_uses`, `uses`,
`expires_at`, `providers[]` and the circle's `revocation_strength`.

Invite codes look like `TODI-XXXXX-XXXXX`, uppercase Crockford base32. The parser is generous — any
case, with or without the `TODI-` prefix, stray whitespace and separators are all tolerated — so
pass through whatever the user typed and let the server normalise it.

An invite link carries the code in the URL **fragment** —
`https://tod.example.com/join#TODI-4KQ7M-9XPB2` — which is never sent to any server. If you are
handed a link, take the fragment.

**Step 2 — `POST /api/v1/join`** (`redeemInvite`, public, **`Idempotency-Key` required**).

Request `RedeemInviteInputBody`:

```json
{ "invite_code": "TODI-4KQ7M-9XPB2",
  "provider": "<a provider key from previewInvite's providers[]>",
  "credential": { "kind": "none" },
  "display_name": "Tankguy",
  "client": { "name": "nparse-plus-tod", "version": "1.4.2" },
  "scopes": ["tod:read", "tod:report", "catalogue:read"] }
```

Response `Joined`: `{created, replayed, membership, circle, token, as_of}`. **`token.token` is the
credential, and this response is the only place it will ever exist in plaintext.** There is no
operation that re-reads it; `GET /tokens` returns prefixes only. Persist it before you do anything
else with the response.

`/join` also sets a `__Host-tod_session` cookie. Ignore it. You are not a browser, and the browser
session is a different credential with a different reach.

**The credential union.** `credential` is discriminated on `kind`, and is identical in
`redeemInvite` and `authenticateIdentity`:

| `kind` | Carries | Usable by a desktop plugin? |
|---|---|---|
| `none` | nothing | **Yes** — for a `local` provider. `display_name` is then required |
| `id_token` | `id_token`, `nonce` | **Yes** — for an `oidc` provider, if you can complete an OIDC flow yourself |
| `bearer_token` | `token` | **Yes, conditionally** — a `discord` token, audience-checked against *this instance's* `client_id` before anything else |
| `provider_ticket` | `ticket` | **No** — single-use, 120-second TTL, delivered in the browser redirect fragment to the instance's own web console |

Be honest with your user about which of these your instance offers: `previewInvite`'s `providers[]`
gives each provider's `key`, `kind`, `display_name`, `available` and `verifiable_subject`.

**The gap, stated plainly:** for a circle that accepts only a browser Discord flow, the
`provider_ticket` lands in the *console's* URL fragment and there is no supported way to hand it to
a desktop plugin. That gap is exactly what the device flow in
[§9](#9-not-yet-available--the-device-flow) closes, and it is not closed today. Where you hit it,
the working path is: the user completes the join in the console's browser, and the operator or
the user supplies the token to your plugin out of band. Do not invent a token-passing scheme of
your own.

**Re-authenticating an existing membership.** `POST /api/v1/sessions` (`authenticateIdentity`,
public, `Idempotency-Key` required) mints a fresh token for a membership that already exists, given
`circle_id` and the same credential union — no invite needed. This is the "new device" path, not a
refresh: your stored token has not expired and does not need replacing.

### The scopes to request, and the ones to refuse

Ask for exactly these three in the `scopes` array of your join request:

```json
"scopes": ["tod:read", "tod:report", "catalogue:read"]
```

**Never send an empty array and never omit the field.** Empty means *every scope in the catalogue*.
It is still bounded by the membership's role, but it is far more than a log parser needs and it is
not what you want sitting unattended on a desktop.

| Scope | Buys you |
|---|---|
| `tod:read` | The board (`listTargetStates`), one target (`getTargetState`), the report log (`listTodReports`, `getTodReport`), the quake log. It also grants `tod.read.attribution` — the permission that puts `reporters[]` in `getTargetState` — but the role must hold it too: it starts at `member`, and an `observer` never sees who reported, whatever the token carries |
| `tod:report` | `createTodReport` — the whole point of the plugin |
| `catalogue:read` | `listRaidTargets`, `getRaidTarget`, `resolveRaidTarget` |

**`catalogue:read` is not optional.** Without it you cannot turn a parsed mob name into a raid
target, so you cannot turn a log line into a report at all.

The refusals matter as much as the grants:

| Refused | Why |
|---|---|
| `circle:read` | Reads the circle's settings and revocation strength. A log parser has no use for it, and `/me` already tells you which circle you are in |
| `member:read` | **A log parser must not be able to enumerate a guild's members.** That is a roster, and a roster is what a compromised desktop process should not be able to exfiltrate |
| `invite:read` | The circle's invite ledger: who minted what, when, how many uses are left, and each code's display prefix (`listInvites` returns `code_prefix`, never the code itself). Both invite permissions start at `officer`, so on a member's token these two scopes grant nothing **today** — and that is exactly the trap. A scope sits on the token until the token is revoked, so the day an officer promotes that user, an unattended desktop process silently acquires the capability |
| `invite:create` | **Mints credentials.** An invite is a bearer credential into the circle. See the promotion trap above: do not carry a scope you cannot use, because "cannot" is a property of the role, and the role changes |
| `tod:retract` | `tod:retract` grants `tod.retract` **and** `tod.retract.any` — there is no own-reports-only scope to hand over, so it would let an unattended process invalidate *another member's* ToD. Correct a mistake by **reporting again**; consensus is derived from an append-only log, so a correction is a new row rather than a deletion |
| `events:subscribe` | Grants only `tod.read`, which you already hold, and there is no event handler to subscribe to today |

There is no quake scope at all — `reportQuake` declares none, so no PAT reaches it, deliberately: a
false quake wipes the whole board.

**Effective capability is `role permissions ∩ token scopes`.** Both sides must hold. This is why two
different error codes exist:

- An **observer** with `tod:report` still cannot report → `403 forbidden`. The role is the problem;
  a different token will not help.
- A **member** whose token carries only `tod:read` cannot report → `403 insufficient_scope`. The
  token is the problem; a token minted with `tod:report` fixes it.

Branch on the code, not on the status.

---

## 3. Membership is checked on every request

ADR-0005 again: membership state is re-read on **every** request rather than tokens being
cascade-revoked at revocation time. One join, always correct, nothing to forget.

**The consequence your client must be built around: a token that worked a minute ago can start
failing, and that is not a bug, not a network fault and not something to retry through.**

| Status | `code` | What happened | What to do |
|---|---|---|---|
| 401 | `unauthenticated` | No credential, or a token in a query string | Send `Authorization: Bearer …` |
| 401 | `token_invalid` | The token was revoked, is unknown, is malformed, or its circle was deleted. Unknown and revoked answer identically, on purpose | The stored credential is dead. Discard it and ask the user for a new one |
| 401 | `token_expired` | Genuine token, `expires_at` has passed | Re-authenticate via `POST /sessions`. You will not see this today (no route sets a TTL) — handle it anyway, because [§9](#9-not-yet-available--the-device-flow) changes that |
| 403 | `membership_revoked` | An officer revoked this membership. Takes effect immediately | **Stop.** Do not retry, do not redeem a new invite, do not mint. Tell the user to speak to an officer; reinstatement is an explicit, audited action |
| 403 | `insufficient_scope` | Role holds the permission; token does not carry a scope reaching it | A different token, not a different role |
| 403 | `forbidden` | The role does not hold the permission | A different role, not a different token |
| 404 | `not_found` | Including a resource in a circle you are not a member of | Cross-circle access answers **404, never 403** — the existence of another circle's data is itself withheld. Do not infer anything from a 404 |

`membership_revoked` is the one to get right. Retrying it in a loop is how a revoked member's
desktop turns into an incident. Surface it once, stop, and leave the credential in place so the
user can be reinstated without re-joining.

There is no unauthenticated read path. Do not build a degraded read-only mode that skips the header.

---

## 4. Time: `as_of`, and every countdown as a signed offset

### The rule

**Every derived response carries a top-level `as_of`, and every countdown in it is a signed offset
from that `as_of` — never an absolute the client subtracts from its own clock.**

An overlay running on a machine whose clock is four minutes fast would otherwise render a window
that is *wrong on screen and right in the database*, which is the worst available combination:
nobody can see the error, and the raid misses the spawn.

This repository enforces the rule in its own web console with a gate called `WEB002`, which bans
the browser's clock outright. **No gate can reach your repository.** The reasoning is identical off
the console, and this paragraph is the only enforcement you get.

### How to implement it

`Window` carries both forms, and you use only one of them:

| Field | Use |
|---|---|
| `seconds_until_open`, `seconds_until_close` | **Yes.** Signed integers, relative to `as_of`; negative means the boundary has passed. They truncate toward zero |
| `progress_bp` | **Yes.** Where "now" sits between `open_at` and `close_at`, integer basis points, 10000 = 100%, clamped to `[0, 10000]`. **`null` for a `fixed` or `unknown` window** — a fixed timer has no band to be part-way through |
| `spawn_at` | Present **iff** the timer is `fixed` and a window exists, so you can branch on its presence without inspecting `kind` |
| `open_at`, `close_at` | Display and audit only. **Do not compute a countdown from them** |
| `kind` | `fixed`, `variance` or `unknown` |

The correct recipe, on every response:

1. Record `t0 = monotonic_now()` — a monotonic source, not the wall clock — at the moment the
   response is parsed.
2. Keep `seconds_until_open` as received.
3. Render `seconds_until_open - (monotonic_now() - t0)`.

Your machine's absolute clock is never consulted. Clock drift, a DST transition and an NTP step all
become irrelevant, and the number on screen is the number in the database plus the elapsed time you
actually measured.

`unknown` is a first-class value, not a null to interpret: it means the instance holds no timer for
that target, and the board says `status: no_timer` beside it. Render "we don't know", never a
guess. See [§8](#8-the-hard-rule-never-invent-a-p99-log-format).

### Two timestamps that must never be conflated

- **`died_at` is game truth.** It may be backdated, and routinely is — somebody types in a kill from
  three hours ago.
- **`reported_at` is system truth.** It is never backdated.

On a report you send:

- `died_at` may not be in the future beyond **120 seconds** of clock skew → `422 died_at_in_future`.
  That is the only hard rejection on a time in the whole product, because a death in the future is
  impossible independent of any derivation.
- `died_at` may not be more than **90 days** in the past → `422 died_at_too_old`. Backdating is
  supported; a 90-day-old ToD is a timezone or epoch bug, not intel.
- `client_clock_offset_seconds` is *your* skew estimate. Send it. It is recorded so an operator can
  see the skew; it is not applied to your timestamp for you.

### Nullable timestamps

These fields are `null` on the wire when the value is absent, while `openapi.json` types them as a
non-nullable `string` with `format: date-time`. Decode them as nullable or your client will throw
on a valid response:

| Schema | Fields that can be `null` |
|---|---|
| `Window` | `open_at`, `close_at`, `spawn_at` (and `progress_bp`, `seconds_until_open`, `seconds_until_close`, which the spec *does* type as nullable) |
| `BoardEntry`, `TargetStateResponse` | `died_at`, `up_since`, `computed_at` |
| `Token` (the join response) | `expires_at` — and today it is **always** `null` |
| `TokenView` (`listMyTokens`) | `last_used_at`, `expires_at`, `revoked_at` |
| `PrincipalView` (`/me`) | `token_expires_at` |
| `Member` | `revoked_at` |

`contest_reason` and `change_reason` are already correctly typed `["string","null"]`.

---

## 5. The endpoints a plugin actually needs

Shapes are named, not restated: fetch the schema with the recipe in [§0](#recipes) rather than
trusting a paraphrase. `{circle_id}` is the ULID from `GET /me`.

| # | Operation | Method and path | Scope | Notes |
|---|---|---|---|---|
| 1 | `getServerMeta` | `GET /meta` | public | Reachability and version. Response `ServerMeta` |
| 2 | `previewInvite` | `POST /invites/preview` | public | Rate-limited. Response `InvitePreview` |
| 3 | `redeemInvite` | `POST /join` | public | **`Idempotency-Key`**. Response `Joined` — the token, once |
| 4 | `authenticateIdentity` | `POST /sessions` | public | **`Idempotency-Key`**. New device for an existing membership |
| 5 | `getCurrentPrincipal` | `GET /me` | any | Response `PrincipalView`. Your `circle_id` comes from here |
| 6 | `resolveRaidTarget` | `POST /raid-targets/resolve` | `catalogue:read` | Body `ResolveRaidTargetInputBody`; response `ResolutionResponse` |
| 7 | `listRaidTargets` | `GET /raid-targets` | `catalogue:read` | Response `PageCatalogueEntry`. Filters `server`, `expansion`, `zone`, `q`, `include_retired` |
| 8 | `createTodReport` | `POST /circles/{circle_id}/tod-reports` | `tod:report` | **`Idempotency-Key`**. Body `CreateTodReportInputBody`; response `TodReportResponse` |
| 9 | `listTargetStates` | `GET /circles/{circle_id}/tods` | `tod:read` | **The board.** `ETag`/`304`. Response `PageBoardEntry` |
| 10 | `getTargetState` | `GET /circles/{circle_id}/tods/{target_id}` | `tod:read` | `ETag`/`304`. Response `TargetStateResponse` |
| 11 | `listTodReports` | `GET /circles/{circle_id}/tod-reports` | `tod:read` | Response `PageReport`. Filters `target_id`, `died_after`, `died_before`, `reporter_membership_id`, `include_retracted` |
| 12 | `listMyTokens` | `GET /tokens` | any | Your own devices only. Prefixes, never secrets |

`revokeToken` (`DELETE /tokens/{token_id}`) carries `x-tod-session-only: true`: **a PAT cannot
revoke a token, including its own.** Revocation is a browser-session operation in the console. Do
not offer a "revoke this device" button that cannot work.

### Resolving a target name — do not hold a catalogue

`createTodReport` takes **exactly one of** `target_id` and `target_name`. Send `target_name`.

The server runs one resolve ladder, strongest rung first, and never falls through:

```
id   name   name_normalised   alias   alias_normalised   prefix   substring
```

Normalisation strips `'`, `` ` `` and `-` and casefolds, so ``Vulak`Aerr``, `Vulak'Aerr`,
`VulakAerr` and `vulak aerr` all land on the same target. An exact hit is never ranked below a
substring hit. A rung that matches more than one target is an ambiguity, not a coin toss.

**Do not build a client-side catalogue to avoid this call.** The ladder exists precisely so the
plugin never has to hold one, and a stale local copy is how a real kill gets attached to the wrong
mob. Use `resolveRaidTarget` when you want to show the user what a string will match before
committing; otherwise let `createTodReport` do it in one round trip.

Two outcomes to handle:

- `422 unknown_target` — nothing matched at any rung. Do not retry with a mangled name. Show the
  user the string you parsed and let them correct it, or send an explicit `target_id`.
- `422 ambiguous_target` — a tie. The problem body carries `meta.candidates[]` with the tied
  targets (capped, and `detail` says so when it was cut). **Present them and let a person pick**,
  then re-send with `target_id`. Guessing here attaches a real kill to the wrong mob and puts a
  confidently wrong window on a board people are planning a raid night around.

### Creating a report

Body (`CreateTodReportInputBody`; `server` and `died_at` are the only required fields):

```json
{ "target_name": "Vulak`Aerr",
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

- `server` must equal the circle's server or the answer is `422 server_mismatch`. Read it from
  `Joined.circle.server` and store it **against that circle id**. There is no such thing as "the
  user's server": one user can be reporting into two circles on `blue` and one on `green`, so a
  single global server setting is a bug that will misroute every report from the second circle.
- `source` ∈ `log_line | manual | api | import`, defaulting to `manual`. A parsed line is
  `log_line`; a time the user typed is `manual`. Be truthful — this is what an officer uses to
  weigh evidence.
- `self_confidence` ∈ `certain | probable | guess`, defaulting to `certain`. **Use `probable` and
  `guess` when they are true.** The enum exists so a client can say when it does not know, and a
  parser that marks every heuristic match `certain` has thrown that away.
- `source_line` is the raw line **verbatim**, up to 1024 characters. Send it. It is stored unchanged
  so a human can audit what you parsed, and it is the single most useful field when a ToD is
  disputed.
- Reports are append-only and immutable. There is no edit. A correction is a new report.

### The board

`listTargetStates` returns **every active raid target**, including ones nobody has reported —
a board that hid untracked targets could not tell you what you are not tracking. Sorted by window
open time, soonest first, with everything that has no window after everything that does.

A `BoardEntry` carries `target`, `server`, `status`, `died_at`, `up_since`, `window`,
`timer_source`, `confidence`, `contested`, `contest_reason`, `change_reason`, `evidence` and
`computed_at`. It does **not** carry `report_ids[]` or `alternatives[]`; `getTargetState` has
those.

The enums, all closed:

```
status:         unknown  no_timer  pre_window  in_window  overdue  up
confidence:     unknown  low  medium  high            (ordered: unknown < low < medium < high)
timer_source:   circle_override  catalogue  none
contest_reason: thin_supersede  implausible_ordering  wide_spread  pending_supersede
change_reason:  new_kill  corroboration  retraction  quake  timer_change
```

`confidence` is an ordered enum rather than a number because a float would be read as a probability
this server cannot compute. Render it as a label, never as a percentage.

`contested: true` means the reports disagree and the server is saying so rather than picking a
winner. Show it. `getTargetState` gives `alternatives[]`, the rival clusters, each with its own
window and reporter counts.

**Pagination caveat, stated in the design and worth repeating:** the board's cursor is the last
row's `target_id` while the sort is by window open time, so a row whose window moves between two
page fetches can be skipped or repeated. The catalogue is a hundred-odd rows fixed by the game, so
the simple correct thing is to request `limit=200` and take one page.

### Polling and ETags

There is no event stream today. `subscribeCircleEvents` and `replayCircleEvents` appear in
`docs/design/02-api-design.md` and in the route registry, but have no handler and are absent from
`openapi.json`. Do not write a client that tries to connect to them and falls back.

Poll instead, and poll cheaply. `listTargetStates`, `getTargetState` and `getRaidTarget` all carry
`x-tod-etag: true`:

1. Keep the `ETag` from the last `200`.
2. Send it as `If-None-Match` on the next poll.
3. A match answers **`304` with no body** — cheap for the instance and cheap for you.

Pick an interval that suits a raid night, not a trading floor; 30–60 seconds is plenty for a respawn
window measured in hours. There is no rate limit on these routes today (see [§7](#rate-limiting)),
which is a reason to be considerate rather than a reason not to be.

---

## 6. Idempotency

### Which routes, and how to tell

The machine-readable answer is the `x-tod-idempotency` extension: a **non-empty** value means the
`Idempotency-Key` request header is required. Run the recipe in [§0](#recipes). At the time of
writing that set is:

`createTodReport`, `retractTodReport`, `reportQuake`, `createInvite`, `createServiceMember`,
`createCircle`, `createIdentityProvider`, `createRaidTarget`, `redeemInvite`,
`authenticateIdentity`, `runSetup`.

Of those, a plugin sends the header on `redeemInvite`, `authenticateIdentity` and `createTodReport`.

**The header is not declared as a parameter in `openapi.json`** — only the `Idempotency-Replayed`
response header is. A generated client will silently omit it and every one of those POSTs will
answer `400 idempotency_key_required`. Add it by hand.

### The rules

- Header: `Idempotency-Key: <string>`, at most 255 characters. A ULID is ideal.
- **One key per logical operation, and the same key on every retry of that operation.** A key is
  not per-request; it is per-thing-you-are-trying-to-do.
- Uniqueness is `(membership, key)` — keyed on the membership, not the token, so rotating your
  credential mid-retry still replays instead of duplicating.
- The server hashes the request alongside the key, so replaying a key with different content is
  refused rather than answered with the old response.
- A replayed response carries `Idempotency-Replayed`. Without it you cannot tell a retry that worked
  from a request that ran twice — which is the one question the key exists to answer. `Joined` also
  carries a `replayed` boolean.
- Records live for **24 hours**; after that a key is reusable.

### The errors, and what to do

| Status | `code` | Meaning | Action |
|---|---|---|---|
| 400 | `idempotency_key_required` | You sent no key on a state-creating POST | Send one. Not optional |
| 409 | `idempotency_conflict` | A request with this key is still in flight | **Wait and retry the same request with the same key.** It will replay once the original finishes. Minting a new key here is exactly how one retry becomes two kills in a log that is never edited |
| 422 | `idempotency_key_reused` | This key was used by you for a *different* request | A client bug. Use a fresh key for a different operation |

Why this matters more here than in most APIs: `tod_report` is **append-only**, enforced by database
triggers. There is no delete and no edit. A duplicated report is permanent, it skews the consensus
that derives the respawn window, and the only correction available is another row.

---

## 7. Errors: RFC 9457, and a closed code enum

Every failure is `application/problem+json` with this body (`Problem` in the spec):

```json
{ "type": "https://docs.tod-serve.org/errors/ambiguous_target",
  "title": "Ambiguous target",
  "status": 422,
  "code": "ambiguous_target",
  "detail": "…specific to this occurrence…",
  "instance": "…the request…",
  "errors": [ { "location": "body.target_name", "message": "…" } ],
  "meta": { "request_id": "…", "candidates": [ … ] } }
```

**Branch on `code`.** Not on `status`, not on `detail`, never on `title`. `code` is a **closed
enum** published in the spec (`jq -r '.components.schemas.Problem.properties.code.enum[]'` — 55
values at the time of writing), and the last segment of `type` is always the code. `status`
collapses distinct causes onto one number; `detail` is prose and will change.

Handle an unrecognised code gracefully — fall back to `status` and show `detail`. A client that
crashes on a code added later is a client that breaks on an upgrade it did not need to notice.

`errors[]` carries per-field detail with a dotted `location` (`body.died_at`,
`header.Idempotency-Key`). Use it to point at the field rather than restating the whole request.

### The codes a plugin actually meets

Credential and access — see [§3](#3-membership-is-checked-on-every-request) for the full table:
`unauthenticated`, `token_invalid`, `token_expired`, `membership_revoked`, `insufficient_scope`,
`forbidden`, `not_found`.

Joining:

| Status | `code` | Meaning and action |
|---|---|---|
| 401 | `credential_invalid` | The credential **in your join request** did not verify — a Discord token for a different application, an `id_token` failing signature/`iss`/`aud`/`exp`/`nonce`, or a `credential` object whose `kind` does not match the provider. Note this is about the join credential, **not** your PAT: a bad PAT is `token_invalid`. Re-authenticate; do not retry the same credential, nothing about it will verify on a second attempt |
| 401 | `credential_expired`, `credential_stale` | The provider credential aged out. Get a fresh one |
| 404 | `invite_invalid` | The code is not a live invite. Note the status: a code that names a deleted circle, and a code that was never issued, both get this same answer, so a 404 here tells you nothing about whether the circle exists |
| 409 | `invite_expired`, `invite_exhausted`, `invite_revoked` | The code existed and is no longer usable. Ask for a new one; retrying cannot help |
| 409 | `provider_not_accepted`, `provider_disabled` | The `provider` key you sent is not one this circle accepts, or is disabled on the instance. Pick another from `providers[]` |
| 422 | `provider_unverifiable` | The provider cannot verify a subject, so it cannot be used here |
| 403 | `guild_membership_required`, `guild_role_required` | The circle gates on a Discord guild or role you do not hold. Absence of a fact is a refusal, not a pass |

Reporting:

| Status | `code` | Meaning and action |
|---|---|---|
| 422 | `unknown_target` | Nothing matched at any rung. Show the parsed string; do not guess |
| 422 | `ambiguous_target` | A tie. Read `meta.candidates[]`, let a person pick, re-send with `target_id` |
| 422 | `died_at_in_future` | Beyond 120 seconds of skew. Fix the clock or the parse |
| 422 | `died_at_too_old` | More than 90 days back. Almost always a timezone or epoch bug |
| 422 | `server_mismatch` | `server` is not the circle's server |
| 422 | `validation_failed` | Read `errors[]` |
| 400 | `malformed_request` | The body is not what the schema says |

Transport and infrastructure: `unsupported_media_type` (415), `not_acceptable` (406),
`payload_too_large` (413), `request_timeout` (408), `internal_error` (500), `service_unavailable`
(503). The last two are the only ones worth a blind retry, with backoff.

### Rate limiting

| Status | `code` | Action |
|---|---|---|
| 429 | `rate_limited` | Honour the `Retry-After` header (mirrored in the body as `meta.retry_after_seconds`) and back off |

Today the only metered routes are `previewInvite` and `createAuthorizationURL`, which share **one**
bucket keyed on the caller — both reveal whether an invite code is live, and two buckets would hand
a code-guesser twice the budget. Defaults are a burst of 10 with one attempt returning every 6
seconds.

So a polling plugin will not meet `429` today. **Handle it anyway**, with `Retry-After` honoured and
exponential backoff. The set of metered routes is a server-side policy decision that can change in
a release you will not read the notes for.

### A retry policy that is correct here

| Outcome | Retry? |
|---|---|
| `408`, `429`, `500`, `502`, `503`, `504`, connection error | Yes — exponential backoff, jitter, a cap. **On a POST, reuse the same `Idempotency-Key`** |
| `409 idempotency_conflict` | Yes — same request, same key, after a wait |
| Any other `4xx` | **No.** Nothing about the request will succeed on a second attempt |
| `403 membership_revoked` | **No, and stop the loop.** Surface it to the user once |

---

## 8. The hard rule: never invent a P99 log format

> **Do not write a regex for a P99 log line you have not verified against a real one, and do not
> ship it.**

This is the one rule in this document with no mechanism behind it anywhere. `AGENTS.md` in this
repository states it and names it **review-only, with no gate** — there is no test, no lint rule
and no CI check that can catch you doing it, in this repository or in yours. This page is the only
place it gets said to you.

**Why it is worse than it looks.** A guessed pattern does not fail loudly. It matches something —
an emote, a pet's death, a similarly-shaped line from a different encounter — and produces a
`died_at` that is well-formed, in range, and wrong. That report enters an append-only log, joins the
consensus, and puts a **confidently wrong respawn window** on a board that a raid is planning
around. Nobody sees an error. People see a countdown and believe it. That is strictly worse than
your plugin refusing to parse and saying so.

The whole product is built against exactly this failure mode: an unseeded timer reports `no_timer`
rather than a guess, a contested ToD says it is contested, and `confidence` is an ordered enum
rather than a number because a float would be read as a probability nobody can compute.

**What to do instead:**

- Keep **golden fixtures**: real log lines, captured from the server in question, with the expected
  parse beside each. A pattern with no fixture is a guess.
- Mark an unconfirmed format `unverified` and keep it **off** until somebody confirms it against a
  real log. Raise it with the instance's operator, or file it against tod-serve, rather than
  shipping it dark.
- **When the line does not match, do not report.** Either stay silent, or fall back to
  `source: "manual"` with a time the user typed and an honest `self_confidence`.
- Always send `source_line` verbatim. It is the audit trail that lets a human find your misparse.
- Use `self_confidence: "probable"` or `"guess"` when your pattern is heuristic. A wrong ToD marked
  `guess` is recoverable; a wrong ToD marked `certain` is not.

**The same discipline applies to timer data.** Respawn and variance numbers for P99 are
community-derived and disputed. This server does not bundle them — they load from a separate seed
repository, and an instance without them reports `status: no_timer` and `window.kind: unknown`
rather than guessing. There is a gate here (`SEED001`) but it protects *this* repository, not
yours. **Do not paper over `no_timer` with a number of your own.** Render "no timer for this
target". An honest gap is information; a fabricated window is a lie with a countdown on it.

---

## 9. NOT YET AVAILABLE — the device flow

> **Do not build against anything in this section. None of it exists.**

### What is designed

Three architecture decision records were merged into this repository as **design documents**, all
with status `proposed`:

- `docs/adr/0021-a-device-authorization-grant-against-this-instance.md`
- `docs/adr/0022-a-device-grant-is-identity-scoped.md`
- `docs/adr/0023-guild-facts-come-from-browser-flows.md`

Implementation is tracked as issue **#44** and is **not done**.

The design, in one paragraph: a device authorization grant against *this instance* rather than
against Discord — the plugin shows a user code, the user approves it in a browser that signs in the
way it does today, and the plugin polls. What an approval yields is a durable, **identity-scoped**
`device_grant`, which exchanges for ordinary per-membership PATs. So one Discord approval per
**instance** covers every circle that identity is in, including circles joined *later*, with no
second approval. It would add `/device/authorize`, `/device/approve` and `/device/token`, all
public, and would allow exactly the scope set this document already recommends —
`tod:read`, `tod:report`, `catalogue:read`, and nothing else.

### Why you must not build against it

- **The routes do not exist.** `jq -r '.paths | keys[]' openapi/openapi.json | grep device` returns
  nothing. Calling them gets a 404 that tells you nothing useful.
- **The ADRs are `proposed`, not `accepted`.** The shape can still change.
- Do not write a capability probe that tries the device routes and falls back to invite redemption.
  A probe against an unimplemented endpoint is a code path you cannot test and a support burden you
  cannot debug.
- Do not tell a user in your UI that it is coming.

**How to know it has landed:** the paths appear in the `openapi.json` of the version your instance
reports at `GET /meta`. That is the only signal worth acting on. Not this document, not the ADRs,
and not the issue being closed.

### The one thing to prepare for now

The device flow mints **short-lived** tokens, which is a deliberate change from
[§2](#2-credentials--the-part-that-is-usually-got-wrong)'s "never expires". `api_token.expires_at`
already exists, so it needs no schema change and can arrive in a point release.

So even though you will not see it today: read `token_expires_at` from `/me` and store it, treat
`null` as "no expiry", and handle `401 token_expired` by re-authenticating rather than by discarding
the credential. That costs you a few lines now and means the device flow is a configuration change
rather than a rewrite.

---

## 10. A minimal correct client, end to end

1. **Configure.** Take the instance's public URL from the operator. `GET {base}/meta` to confirm
   reachability and note `version`.
2. **Pair, once per circle.** User pastes an invite code → `POST /invites/preview` → show the circle
   name, server and granted role → `POST /join` with `Idempotency-Key`, `provider` from the
   preview's `providers[]`, the matching `credential`, and
   `"scopes": ["tod:read","tod:report","catalogue:read"]`.
3. **Store, once.** Persist `token.token` from the response — it exists nowhere else — keyed by
   `circle.id` and never by server, beside `circle.server` and `circle.name`. The store is a set of
   circles; a user can be in two on the same server. Keychain, or a `0600` file. Log
   `token.token_prefix` and nothing more.
4. **Confirm.** `GET /me` with the new token. Record `circle_id`, `role` and the `scopes` you were
   actually granted.
5. **Parse conservatively.** Only patterns you have verified against real log lines
   ([§8](#8-the-hard-rule-never-invent-a-p99-log-format)). No match means no report.
6. **Report.** `POST /circles/{circle_id}/tod-reports` with a fresh `Idempotency-Key` per kill,
   `target_name` as parsed, `server` from your stored circle, `died_at` from the line,
   `source: "log_line"`, `source_line` verbatim, and an honest `self_confidence`. Retry the *same*
   key on timeout.
7. **Handle a tie.** On `422 ambiguous_target`, show `meta.candidates[]` and let the user pick, then
   re-send with `target_id`.
8. **Poll the board.** `GET /circles/{circle_id}/tods?limit=200` with `If-None-Match`, every 30–60
   seconds. Take `as_of` and the signed offsets; drive countdowns off a monotonic clock
   ([§4](#4-time-as_of-and-every-countdown-as-a-signed-offset)).
9. **Fail correctly.** `membership_revoked` → stop and tell the user. `token_invalid` → the
   credential is dead, ask for a new one. Everything else per
   [§7](#7-errors-rfc-9457-and-a-closed-code-enum).

That is the whole client. One pairing, one stored token per circle, and no login again.

---

## Appendix — the claims in this document, and where each is checked

| Claim | Check |
|---|---|
| Base path is `/api/v1` | `jq '.servers' openapi/openapi.json` |
| The instance serves no spec and no docs UI | `internal/api/server.go` — `config.OpenAPIPath = ""`, `config.DocsPath = ""` |
| Token format, and that the prefix is loggable and the secret is not | `internal/auth/pat.go`; `docs/adr/0005-pats-bound-to-memberships.md` |
| A PAT is bound to a membership; membership is checked every request | `docs/adr/0005-pats-bound-to-memberships.md`; `docs/errors/membership_revoked.md` |
| `expires_at` is nullable and no route sets a TTL today | `internal/membership/token.go` (`ttl > 0`); `grep -rn "TokenTTL" .` finds no assignment |
| Scope set and the operations each reaches | `jq -r '.paths[][]? \| "\(.operationId) \(.["x-tod-scopes"])"' openapi/openapi.json` |
| Effective capability is `role ∩ scopes` | `docs/errors/insufficient_scope.md`; `x-tod-permission` on any operation |
| Cross-circle access is 404, never 403 | `docs/design/00-canonical-conventions.md` §7 |
| Every countdown is a signed offset from `as_of` | `docs/design/00-canonical-conventions.md` §1; `docs/adr/0008-windows-are-offsets.md` |
| `died_at` bounds: 120 seconds forward, 90 days back | `docs/errors/died_at_in_future.md`, `docs/errors/died_at_too_old.md` |
| Which routes need `Idempotency-Key` | the `x-tod-idempotency` recipe in [§0](#recipes) |
| The header is undeclared in the spec | `grep -c 'Idempotency-Key' openapi/openapi.json` → `0` |
| Idempotency semantics and the three error codes | `docs/errors/idempotency_key_required.md`, `idempotency_conflict.md`, `idempotency_key_reused.md` |
| The error `code` enum is closed and published | `jq '.components.schemas.Problem.properties.code.enum' openapi/openapi.json`; one page per code in `docs/errors/` |
| Only two routes are rate-limited today | `docs/design/02-api-design.md`, "One shared bucket for invite-code probing" |
| The resolve ladder, and not to hold a local catalogue | `docs/errors/unknown_target.md`; `docs/design/02-api-design.md` |
| No event stream today | `jq -r '.paths[][]?.operationId' openapi/openapi.json \| grep -c Events` → `0` |
| The P99 log-format rule is review-only, with no gate | `AGENTS.md`, "Domain caution" |
| The device flow is designed and unimplemented | `docs/adr/0021…`–`0023…` (status `proposed`), issue #44, and no `/device/*` path in the spec |
