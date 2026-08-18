# Canonical conventions

**Status:** normative. **Audience:** contributor, agent.

This file is the tie-breaker. Any contradiction between another document and this one is a bug **in
that document**. Fix the document, not this file.

Where a decision below has an enforcement mechanism, the mechanism is authoritative and the prose is
a description of it.

Much of this is inherited from [Dragon Kill Party](https://github.com/prokopto-dev/dragonkillparty),
deliberately: two projects in the same ecosystem, read by the same officers and driven by the same
plugin, should not disagree about what a timestamp is. Where this project **diverges**, the section
says so and names the ADR.

---

## 1. Time

| Rule | Value |
|---|---|
| Storage | `INTEGER` Unix **microseconds**, UTC. Column suffix `_at`. |
| Go type | `Micros int64` in `internal/core`. |
| Wire format | RFC 3339 with microsecond precision, always `Z`. |
| Clock access | Only via an injected `Clock`. `time.Now` is banned outside `internal/clock`. |

Two distinct timestamps exist on every ToD report and **must never be conflated**:

- `died_at` is **game truth**. It may be backdated, and routinely is — someone types in a kill from
  three hours ago.
- `reported_at` is **system truth**. It is never backdated.

Every derived response carries a top-level `as_of`, and **every countdown in it is expressed as a
signed offset from that `as_of`, never as an absolute a client subtracts from its own clock.** An
overlay running on a machine whose clock is four minutes fast would otherwise render a window that is
wrong on screen and right in the database, which is the worst available combination.

**Enforced by:** `CLOCK001`, an AST analyser in `internal/repogate`, so an aliased import does not
defeat it. Time-dependent tests use `testing/synctest`; `time.Sleep` is grep-banned in `**/*_test.go`.

## 2. Identifiers

ULID in `TEXT`, 26 characters, Crockford base32, generated in Go. Lexicographically sortable, which
gives free time-ordered cursors and avoids the `uuidv7()` / `gen_random_uuid()` dialect split.

`tod_report.id` is therefore also its own cursor, which matters because the report log is the one
collection that grows without bound.

## 3. No floats. Anywhere that computes a window.

`internal/consensus` may not import `float32` or `float64`. This is **not** the money rule inherited
from DKP — there is no money here. It is a reproducibility rule.

A window boundary computed in float is not guaranteed bit-identical across platforms, and the nightly
projection-verify job diffs the cached state against a fresh recomputation and alerts on any
difference. A cross-platform float discrepancy would make that job cry wolf until someone turned it
off, and the job is the only thing standing between a stale cache and a wrong board.

Ratios are **basis points** — `_bp`, integer, 10000 = 100%. `progress_bp` is where "now" sits between
`open_at` and `close_at`, by integer division, clamped to `[0, 10000]`.

**Enforced by:** `NOFLOAT001` in `scripts/repo-gates.sh`, plus a `golangci-lint` rule on the package.

## 4. Sequences

One sequence, `event_seq`, global, on `event_outbox`. It appears in the SSE frame `id:`, the
`X-TOD-Event-Sequence` header, and `Last-Event-ID`.

`?since_seq=` is valid **only** on `/events/replay`. Every other collection uses the opaque ULID
cursor. There is deliberately no `as_of_seq` time-travel read: derived ToD state is a cache, not an
authority, and offering to reproduce it "as of" a point would imply otherwise.

## 5. Enums

**The wire value is the database value.** Lowercase `snake_case`, everywhere, with no translation
layer. Both the SQL `CHECK` constraint and the OpenAPI `enum` are generated from one Go catalogue.

```
server:                        blue green red
circle.state:                  active archived
membership.role:               owner officer member observer
membership.kind:               human service
invite.minted_by_kind:         session pat
identity_provider.kind:        discord oidc local
tod_report.kind:               kill retraction
tod_report.source:             log_line manual api import
tod_report.self_confidence:    certain probable guess
target_state.status:           unknown no_timer pre_window in_window overdue up
target_state.confidence:       unknown low medium high
target_state.contest_reason:   thin_supersede implausible_ordering wide_spread pending_supersede
target_state.change_reason:    new_kill corroboration retraction quake timer_change
raid_target.expansion:         classic kunark velious
raid_target.category:          open_world zone_boss planar ntov sleeper key_holder
raid_target.state:             active retired
raid_target_timer.window_kind: fixed variance unknown
circle.revocation_strength:    durable weak
identity_link.method:          officer_asserted provider_verified
```

**`target_state.confidence` and `membership.role` are ordered, and the order is the rule.**
`unknown < low < medium < high`; `observer < member < officer < owner`. Both are stored as the `TEXT`
value with an `ORDER BY CASE` where ordering is needed, so the wire value stays readable and the
ordering stays in one place.

A resource has **one** of `state` or `status`, never both.

**Enforced by:** the enum catalogue is a Go `const` block in `internal/schemaenum`; `make gen` writes
it into the migration `CHECK` and the OpenAPI schema; a test asserts the three copies agree.

## 6. Permissions and scopes — one catalogue, generated

There is exactly one source: `internal/authz/catalogue.go`. It generates the `permission` table seed,
the OpenAPI `x-tod-permission` metadata, the PAT scope enum, the authorization matrix, and
`docs/reference/permissions.md`. Hand-written permission lists are forbidden; `role_permission` is
FK-constrained to `permission(key)`, so a divergent list is a **boot failure**, not a style issue.

**Permission keys** are `<resource>.<action>`, dot-separated, lowercase:

```
circle.read circle.manage circle.security.manage circle.delete
member.read member.manage member.revoke
invite.read invite.create invite.revoke
tod.read tod.read.attribution tod.report tod.retract tod.retract.any tod.quake.report
catalogue.read catalogue.manage
audit.read ops.read
token.mint token.revoke
instance.circle.create instance.security.manage instance.owner
```

Two of those splits are choices, and the obvious alternative is one key:

- **`circle.manage` vs `circle.security.manage`.** The split is by *what a compromise costs*, the
  same boundary DKP drew between `admin.settings` and `admin.security.manage`. Renaming a circle
  leaks nothing. Changing its accepted identity providers changes its revocation guarantee, so it is
  a security key and only an owner holds it.
- **`tod.read.attribution` vs `tod.read`.** This separation *is* the `observer` role. Without it
  there is no way to share a board with an allied guild without also handing over the identity of
  your trackers. An observer sees the state, the window and the evidence counts; not who reported.
  The counts stay visible even to an observer, because a confidence figure with no denominator is
  worse than no confidence figure.

**PAT scopes** are `<family>:<verb>`, colon-separated. Scopes are coarser than permissions on
purpose — a scope narrows a token, a permission narrows a role:

```
tod:read tod:report tod:retract
circle:read
member:read
invite:read invite:create
catalogue:read
events:subscribe
```

### The capability floor

**Effective capability = role permissions ∩ token scopes.** A token can only ever narrow what its
membership's role already grants. **There is no `admin:*` scope and no all-powerful token.**

Operations that alter authentication, authorization, or bulk-export state are **session + step-up
only** and have no scope at all:

```
circle.manage circle.security.manage circle.delete
member.manage member.revoke
invite.revoke
token.mint token.revoke
catalogue.manage
audit.read
instance.circle.create instance.security.manage instance.owner
```

The block above is fenced because it is **parsed**:
`TestCapabilityFloor_MatchesCanonicalConventions` compares `authz.CapabilityFloor()` against these
tokens element by element and in both directions, so the Go function and this section cannot drift.

**`invite.create` is deliberately NOT in the floor, while `token.mint` is.** An invite is time-boxed,
single-use, role-capped below `owner` and fully audited, so a leaked bot token can add a visible,
revocable member — not seize the circle. A minted PAT has none of those properties. The bot that
posts an invite link on request is the single most natural Discord integration this product will ever
have, and putting a browser session in front of it would kill it.

That trade is only defensible because invites minted **by a PAT** are hard-narrowed regardless of
what the request asks for: `max_uses = 1`, `expires_in ≤ 24h`, role ≤ `member`. Values above the cap
are clamped and the response says so via `capped_by: "pat"`. Without that narrowing, `invite.create`
belongs in the floor.

## 7. HTTP conventions

| Concern | Rule |
|---|---|
| Base path | `/api/v1`. Within v1, **additive only**. |
| `operationId` | Explicit on every operation, `lowerCamelCase`, **never auto-derived and never renamed** — generated SDK method names come from it, so a rename is a breaking change even when the HTTP surface is unchanged. |
| Errors | RFC 9457 `application/problem+json`, stable machine `code` from a closed enum, `type` URL resolving to a real docs page. Never HTTP 200 with an error body. |
| Pagination | Cursor only, in the body envelope: `{items, next_cursor, has_more}`. `limit` default 50, max 200. Never `Link` headers, never offset. |
| Idempotency | `Idempotency-Key` **required** on every POST that creates domain state. Uniqueness is `(principal_id, key)` where principal is the **membership** — never the token — so rotation mid-retry still replays. |
| Concurrency | `ETag` on mutable resources; `If-Match` required on state transitions. `412` returns the current representation in `meta.current`. |
| Auth transport | `Authorization: Bearer tods_pat_…` only. Query-string tokens are rejected with `401`, **with no exception at all** — there is no compat shim here. |
| Session cookie | `__Host-tod_session`. |
| Hidden operations | `Hidden: true` is permitted only on `/healthz`, `/readyz`, `/metrics` and the OAuth callback. |

### Cross-circle access returns 404, never 403

A `403` confirms that the circle exists and that the caller found a valid id. ToDs are competitive
intelligence and a circle's *existence* is part of what it is hiding — an officer should not be able
to confirm that a rival guild runs a circle on this instance by probing ids.

Within-circle permission failures are `403` normally. The distinction is exactly: wrong tenant is
`404`, right tenant and insufficient permission is `403`.

**Enforced by:** `TestTenancy_CrossCircle_EveryOperationDenies`, which walks the route registry
rather than a hand-written list, so a new circle-scoped route with no coverage is a red test.

## 8. Database conventions

```sql
-- Every table is STRICT. STRICT permits only INT, INTEGER, REAL, TEXT, BLOB, ANY —
-- BIGINT, BOOLEAN, DATETIME, NUMERIC and DECIMAL are ILLEGAL. Use INTEGER (already 64-bit).

id           TEXT    NOT NULL PRIMARY KEY,   -- ULID
created_at   INTEGER NOT NULL,               -- Micros, UTC
updated_at   INTEGER NOT NULL                -- absent on append-only tables

-- Enums:    TEXT + CHECK (x IN ('a','b'))   -- readable in a DB browser; the officer's
--                                              debugging tool is `sqlite3 tod.db`, not our UI
-- Booleans: INTEGER NOT NULL CHECK (x IN (0,1))
-- Ratios:   *_bp    INTEGER                 -- basis points, 10000 = 100%
-- Time:     *_at    INTEGER                 -- Micros
-- Names:    name TEXT + name_norm TEXT      -- normalised IN GO, a plain column, then indexed
-- JSON:     *_json  TEXT NOT NULL DEFAULT '{}'  -- validated on write, NEVER queried into
```

Table names are **singular** (`tod_report`). Columns are `snake_case`.

`name_norm` is normalised in Go (NFKC + casefold + strip `'` `` ` `` `-`), **not** a generated
column: core SQLite has no NFKC, `lower()` is ASCII-only, and `ALTER TABLE ADD COLUMN` cannot add a
STORED column, so every future normalisation change would force a table rebuild.

The backtick strip is not hypothetical here. `` Vulak`Aerr `` is a raid target, and matching it
against user input typed as `Vulak'Aerr`, `VulakAerr` or `vulak aerr` is the whole job of
`raid_target.name_norm`.

Case-insensitive matching uses `name_norm`, never a collation.

## 9. Tenancy — **this project diverges from DKP**

**The circle is the tenant. Every circle-scoped table carries `circle_id NOT NULL REFERENCES
circle(id)`, and every circle-scoped query names it in the `WHERE`.**

DKP [ADR-0004](https://github.com/prokopto-dev/dragonkillparty/blob/main/docs/adr/0004-single-guild-per-instance.md)
deletes the tenant column outright, on the correct reasoning that a missing `WHERE guild_id = ?` is a
silent cross-tenant leak no test catches by accident. This project reintroduces that bug class
knowingly. [ADR-0002](../adr/0002-circle-is-the-tenant.md) is the argument and the replacement
mechanism; the short version is that DKP's tenant is sixty people with a year of ledger history,
where "run a second container" is proportionate, and a circle is four friends, where it is the
difference between the product existing and not.

The replacement is three gates, not a promise:

| Gate | Asserts |
|---|---|
| Schema test + instance-scoped allowlist | Every table not on the allowlist has `circle_id NOT NULL REFERENCES circle(id)` |
| `TEN001` over `db/queries/*.sql` | Every circle-scoped query names `circle_id` in its `WHERE` |
| `TestTenancy_CrossCircle_EveryOperationDenies` | Derived from the route registry, so coverage cannot be forgotten |

The instance-scoped allowlist is explicit and short: `tod_meta`, `instance`, `identity_provider`,
`identity`, `identity_link`, `auth_flow`, `credential_ticket`, `raid_target`, `raid_target_alias`,
`raid_target_timer`, `api_token`, `idempotency_record`, `event_outbox`. Adding a table to it is a
reviewed decision, not a convenience.

The two newest entries are on it because **no circle owns them** — not because a circle cannot be
identified before redemption:

- `auth_flow` holds the OAuth `state` and the server-side PKCE verifier. It may record a
  **nullable** `circle_id` so the authorization request can be parameterised before the browser
  leaves — which scopes to ask for, which guild the gate names. That is a hint for building a
  provider request, not a tenancy key, and it is derived **only from an invite code**: the public
  route accepts no circle identifier, because resolving one would confirm a circle's existence to
  anybody who guessed an id (§7).
- `credential_ticket` carries a verified subject for 120 seconds and is redeemable at **either**
  `/join` or `/sessions`. Which circle it lands in is settled at redemption, by the invite or by the
  request.

**Reading an invite's circle before redemption is permitted; binding these rows to that circle is
not.** A circle-scoped table carries `circle_id NOT NULL REFERENCES circle(id)`, and that would be a
false statement about a row which exists before the caller holds any membership and which may be
redeemed into a circle chosen later. Both rows are looked up by an unguessable server-minted secret
— `state`, `ticket_hash` — on a unique index and **never by circle**, so there is no query here
whose missing `WHERE circle_id = ?` could leak across tenants; the bug class the tenancy rule exists
to catch does not arise.

**Redemption is the authority on which circle a person joins.** Anything either row recorded
earlier is advisory and re-checked there — including whether the invite is still live. See
[04-identity §5](04-identity-and-revocation.md#5-one-join-endpoint).

This list and `INSTANCE_SCOPED` in `scripts/repo-gates.sh` are two copies of one fact.
**Enforced by:** `TestInstanceScopedAllowlist_MatchesRepoGates`, which parses both and compares them
in each direction — exactly the drift this repository gates against elsewhere.

## 10. The report log — non-negotiable

- **Append-only, enforced by database trigger.** `tod_report`, `quake_event`, `invite_redemption`,
  `identity_link`, `audit_log` and `event_outbox` are never `UPDATE`d or `DELETE`d, in Go, in SQL, or
  in a migration. `BEFORE UPDATE OR DELETE … RAISE(ABORT)`.
- **Corrections are new rows.** A retraction is a row with `retracts_report_id` set. The original
  stays visible. A retraction of a retraction is not supported — post a fresh report.
- **Derived state is a cache.** `target_state_cache` is droppable, rebuilt lazily on read-miss and
  wholly by `tod-serve rebuild-states`. A nightly job recomputes every state from the reports and
  diffs; **the recomputation wins and an alert fires.**
- **Consensus is pure.** `internal/consensus` takes reports, quakes, a timer, a `now` and a circle
  config, and returns a state. No store, no `time.Now`, no `math/rand`, no floats. It must be
  replayable byte-identically and property-testable without a database.
- **Revoked members' reports still count**, and the reporter renders as revoked. Their retractions
  apply too, by symmetry — anything else lets revocation silently rewrite history.

**Enforced by:** the triggers, *plus* `TestAppendOnly_TriggersFire_AfterAllMigrations` — table
rebuilds drop triggers, and that test is how you find out.

## 11. Retention

**ToD reports are never pruned.** A busy circle produces a few hundred rows a week and the log *is*
the audit trail; the whole trust argument for deriving rather than storing collapses if the evidence
expires. This is a deliberate divergence from DKP's 90-day `parse_line` prune, and the volumes differ
by three orders of magnitude.

## 12. Health checks

| Endpoint | Touches DB? | Used by |
|---|---|---|
| `/healthz` | **No** | The container `HEALTHCHECK`. A DB-touching healthcheck lets Docker kill the container mid-migration. |
| `/readyz` | Yes — DB reachable, migrations at expected version | Load balancers, `tod-serve doctor`, deploy gates |

## 13. Metrics

`/metrics` is **disabled by default** (`TOD_METRICS_ENABLED=false`). When enabled it binds a separate
listener and requires `TOD_METRICS_TOKEN`. It is never public and never gated by a PAT scope.

## 14. Outbound requests

**Outbound HTTP originates only from `internal/identity`, and every request goes through the one
guarded client in `internal/identity/outbound`.** The providers that issue requests are
`internal/identity/discord` and `internal/identity/oidc`; neither may construct a client of its
own. Discord's URL is fixed. OIDC discovery and JWKS URLs are *operator-supplied*, which is the
classic SSRF pivot, so the dialer denies private, link-local, loopback and cloud-metadata
addresses, follows no redirects, caps the response size and enforces a timeout.

**One client rather than one per provider.** The rule used to name the two provider packages and
let each build its own `http.Client`; that admitted a provider whose client simply did not have the
guard in it, which is a guard nobody would notice was missing. Confining *construction* to a third
package is a narrower rule than the one it replaces, not a wider one.

**The dialer resolves, checks every resolved address, and then dials the address literal.** The
ordering is the mechanism: a filter that validates a hostname and then hands the *name* to the
dialer leaves a window in which the attacker's resolver answers the second query differently, which
is DNS rebinding. There is no second lookup to win. One denied answer refuses the whole name rather
than the surviving addresses being used.

`instance.security.manage` is step-up and PAT-forbidden precisely so that a leaked token cannot add a
malicious issuer and pivot.

**Enforced by:** `NET001`, in two halves — `http.Client`, `http.Transport` and `net.Dial` outside
`internal/identity/outbound`, and `http.NewRequest` outside `internal/identity` — plus a unit test
on the dialer's deny list.

## 15. Data provenance

Raid target **identity** — names, zones, expansions, categories — ships embedded as our own
literals. They are facts about the game.

Raid target **timers** do not ship. Respawn and variance numbers are community-derived, genuinely
disputed, changed when P99 changes them, and their most convenient source is a wiki whose licence has
not been cleared. They load from a separate, optional `tod-serve-p99-seed` repository via
`tod-serve seed timers --file`, behind the same CI grep firewall DKP puts around `dkp-p99-seed`.

An unseeded instance reports `status: no_timer` everywhere and still records ToDs correctly. That is
a degraded state, and an honest one.

## 16. Naming quick reference

| Thing | Convention | Example |
|---|---|---|
| Go package | short, lowercase, no underscores | `internal/consensus` |
| Go test | `TestThing_Condition_Expectation` | `TestDerive_TwoReportsWithinEpsilon_MergeToOneKill` |
| SQL table | singular snake_case | `tod_report` |
| SQL column | snake_case, typed suffix | `died_at`, `progress_bp`, `window_open_offset_seconds` |
| JSON field | snake_case | `died_at`, `next_cursor` |
| `operationId` | lowerCamelCase, verb + resource | `createTodReport` |
| Permission | `resource.action` | `tod.quake.report` |
| PAT scope | `family:verb` | `tod:report` |
| Error code | snake_case, closed enum | `membership_revoked` |
| Webhook / SSE event | `resource.past_tense_verb` | `tod.changed` |
| Migration file | `NNNNNN_snake_case.sql`, append-only | `000003_add_quake_event.sql` |

## What to do when two rules conflict

The invariant wins, and the conflict is a bug worth reporting. Say so rather than picking one
silently. This document is the tie-breaker between any two others; if a document disagrees with it,
the document is stale.
