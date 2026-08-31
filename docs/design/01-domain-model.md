# Domain model

**Status:** normative for Phase 1–2. **Tie-breaker:** [00-canonical-conventions.md](00-canonical-conventions.md).

Every table is `STRICT`. IDs are ULIDs in `TEXT`. Times are `INTEGER` Micros suffixed `_at`. Enums
are `TEXT + CHECK`. Append-only tables have no `updated_at`.

## The two decisions that shape the schema

**The circle is the tenant, and a circle is pinned to one server.** `circle.server` is
`blue | green | red`, `NOT NULL`, and immutable after creation (trigger-enforced). A guild raiding
Blue and Green makes two circles.

This is [merchant-mode's "one server at a time" rule](https://github.com/prokopto-dev/nparseplus-merchantmode)
applied structurally rather than as a `WHERE` clause someone forgets: there is no row in this schema
where a Blue fact and a Green fact can meet, so "a Blue ToD says nothing about Green" is a property
of the shape, not of the queries.

The cost is real and accepted: two circles means two invite sets and two member lists. It composes
with the multi-destination client, though — the plugin already holds several `(endpoint, token,
circle)` destinations, so "report to my Blue circle and my Green circle" is two ticked boxes rather
than a feature.

*Rejected: `circle.server` nullable with `server` on every report.* It permits a combined view, and
the moment a combined view exists someone builds an "all my ToDs" screen that is wrong.

**A report still carries `server` in the request body**, even though storage does not duplicate it.
Mismatch is `422 server_mismatch`. This is not redundancy — it is the guard against the actual
failure mode of a fan-out client, which is that the user is playing Blue and has the Green
destination ticked. Without the echo, the wrong data lands silently.

## Instance-scoped tables

No `circle_id`. This list **is** the allowlist that the tenancy schema gate checks against; adding to
it is a reviewed decision.

| Table | Mutability | Purpose |
|---|---|---|
| `tod_meta` | mutable | `key`/`value`/`updated_at`, `WITHOUT ROWID`. Schema version, pepper generation, event head. |
| `instance` | mutable | Singleton, `id INTEGER CHECK (id = 1)`. Name, public URL, timezone, self-service flag. |
| `identity_provider` | mutable | The pluggable IdP registry. |
| `identity` | append-mostly | `(provider_id, subject)` → a person, instance-wide. Carries `blocked_at`. |
| `identity_link` | append-only | Officer-asserted equivalence between two **verifiable** identities. |
| `instance_grant` | append-only | One instance-level authorization **decision** — `granted` or `revoked` — on an identity. Hash-chained; it is its own audit record, because `audit_log.circle_id` is `NOT NULL`. [ADR-0012](../adr/0012-instance-grants-are-a-capability-ledger.md) |
| `instance_setting_change` | append-only | One change to an instance-wide policy switch: which `setting`, its `old_value` and `new_value`, who and when. Hash-chained, and its own audit record for the same reason `instance_grant` is. [ADR-0019](../adr/0019-instance-settings-are-mutable-with-a-change-ledger.md). `public_url` is not a value `setting` can hold — it is immutable over the API, because it must keep matching every registered redirect URI. |
| `auth_flow` | mutable, prunable | One in-flight OAuth authorization: `state`, the **server-side** PKCE verifier, TTL. |
| `credential_ticket` | mutable, prunable | A verified subject, single-use, 120-second TTL, between the OAuth callback and `/join` or `/sessions`. |
| `raid_target` | mutable | Catalogue: mob identity. Server-agnostic. |
| `raid_target_alias` | mutable | `VA`, `Naggy`, `Vox`, `Trak` → target. |
| `raid_target_timer` | mutable | **Per-server** respawn window. PK `(target_id, server)`. |
| `api_token` | mutable | Opaque PATs, bound to a membership. |
| `session_revocation` | mutable, prunable | A browser session that signed out, refused from that moment on. Sessions are signed rather than stored, so this holds only the ones somebody ENDED; `expires_at` is the revoked session's own expiry and `internal/sweep` takes the row afterwards; a repeated sign-out moves `updated_at` rather than writing a second row. |
| `idempotency_record` | mutable | `(principal_membership_id, key)` → request hash, response, `completed_at`. |
| `event_outbox` | append-only | Global `event_seq` (`INTEGER PRIMARY KEY AUTOINCREMENT`), SSE delivery. `circle_id` is nullable: an instance-level event belongs to no circle. |

## Circle-scoped tables

Every row carries `circle_id NOT NULL REFERENCES circle(id)`.

| Table | Mutability | Purpose |
|---|---|---|
| `circle` | mutable | The tenant. |
| `circle_provider` | mutable | Which instance providers this circle accepts, **and the Discord guild gate**. |
| `circle_discord_channel` | mutable | A Discord channel bound to **one** circle: the disambiguator for a guild raiding two, and where the disclosure decision is stored. [ADR-0017](../adr/0017-discord-interactions-in-the-binary.md) |
| `membership` | mutable | `(circle, identity)` → role + revocation. Mutable because a role change and a revocation are *state*, not events. |
| `invite` | mutable | `uses` and `revoked_at` mutate. |
| `invite_redemption` | append-only | Who redeemed what, when. |
| `tod_report` | **append-only** | The core log. |
| `quake_event` | **append-only** | Server-wide repop. |
| `circle_timer_override` | mutable | This circle disagrees with the catalogue about a target's window. |
| `target_state_cache` | droppable cache | Materialised consensus. **Never authority.** |
| `audit_log` | append-only | Hash-chained. `circle_id NOT NULL` — see the note below. |

## The tables that carry a decision

### `circle`

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT | ULID |
| `name`, `name_norm` | TEXT | normalised in Go |
| `description` | TEXT | default `''` |
| `server` | TEXT | `CHECK (server IN ('blue','green','red'))`, **immutable** |
| `timezone` | TEXT | IANA, default `UTC`, display only |
| `min_reporters_to_supersede` | INTEGER | default 1 — see [consensus §4](03-consensus.md) |
| `revoke_invalidates_invites` | INTEGER | 0/1, defaults on for weakly-revocable circles |
| `state` | TEXT | `active \| archived` |
| `deleted_at` | INTEGER | Micros, NULL while live. A **tombstone**: `deleteCircle` cannot remove the row, because the append-only tables referencing it forbid it |

**`state` and `deleted_at` are different questions, and neither is the other.** `archived` is a
circle that is still there and still readable — a guild that has stopped raiding Velious and wants
its board out of the way — and it is set through `updateCircle` like any other setting. `deleted_at`
is a circle that has stopped existing: every read carries `deleted_at IS NULL`, it releases its
name, its invites resolve to nothing, and its members' credentials stop working on the next request.
Implementing `deleteCircle` as an archive would be the confident mistake — the officers would
believe the circle was gone while every member could still read the board.

`revocation_strength` is **derived, never stored** — `durable` iff every accepted, instance-enabled
provider has `verifiable_subject = 1`, else `weak`. Storing it would let it drift the moment a
provider is added to the instance.

### `membership`

| Column | Notes |
|---|---|
| `id`, `circle_id` | |
| `identity_id` | NULL for a service membership |
| `kind` | `human \| service` |
| `owner_membership_id` | required when `kind = 'service'` |
| `display_name`, `display_name_norm` | circle-local; seeded from the identity, editable |
| `role` | `owner \| officer \| member \| observer` |
| `admitted_by_invite_id` | |
| `joined_at`, `revoked_at`, `revoked_by_membership_id`, `revoke_reason` | |

```sql
CREATE UNIQUE INDEX ux_membership_identity
  ON membership(circle_id, identity_id) WHERE identity_id IS NOT NULL;
```

**That partial unique index is the entire revocation mechanism.** A revoked person redeeming a fresh
invite hits the existing row, sees `revoked_at IS NOT NULL`, and gets `403 membership_revoked`. There
is never a second row, and there is never a delete-then-insert path — **the service has no
delete-membership operation at all.** Reinstatement is an explicit, audited
`POST .../reinstate` requiring `member.revoke`.

Membership state is checked **on every request** rather than by cascade-revoking tokens at revocation
time. One join, always correct, nothing to forget.

### `invite`

`id`, `circle_id`, `code_hash BLOB` (unique), `code_prefix TEXT` (display only), `role`
(`CHECK (role <> 'owner')`), `max_uses`, `uses` (`CHECK (uses <= max_uses)`), `expires_at NOT NULL`,
`revoked_at`, `created_by_membership_id`, `minted_by_kind` (`session | pat`), `note`.

Codes are **instance-unique**, so `POST /join` needs no circle id — one paste. Lookup is by
`code_hash` on the unique index, **never by prefix**: a prefix lookup is a brute-force oracle.
Format `TODI-XXXXX-XXXXX`, Crockford base32 without `I/L/O/U`, 50 bits.

`expires_at` is `NOT NULL`. There are no eternal invites.

### `raid_target` and `raid_target_timer`

Split because a mob's *existence* is a fact about the game and its *timer* is a fact about a server —
the same split merchant-mode draws between an item's id and its price.

`raid_target`: `id`, `name` (canonical, backtick included — `` Vulak`Aerr ``), `name_norm`, `zone`,
`zone_norm`, `expansion`, `category`, `is_quake_target`, `state`.

`raid_target_timer`, PK `(target_id, server)`:

| Column | Notes |
|---|---|
| `window_kind` | `fixed \| variance \| unknown` |
| `window_open_offset_seconds` | seconds from ToD to earliest possible spawn |
| `window_close_offset_seconds` | seconds from ToD to latest possible spawn |
| `fixed_grace_seconds` | default 900 — see [consensus §6](03-consensus.md) |
| `cluster_epsilon_seconds` | per-target override for clustering |
| `source`, `note` | provenance of the numbers |

```sql
CHECK ((window_kind = 'unknown') = (window_open_offset_seconds IS NULL))
CHECK ((window_open_offset_seconds IS NULL) = (window_close_offset_seconds IS NULL))
CHECK ((window_kind = 'fixed') = (window_open_offset_seconds IS NOT NULL
                                  AND window_close_offset_seconds IS NOT NULL
                                  AND window_open_offset_seconds = window_close_offset_seconds))
CHECK (window_open_offset_seconds IS NULL OR window_close_offset_seconds IS NULL
       OR window_close_offset_seconds >= window_open_offset_seconds)
```

**Four, not three, and none of them may evaluate to NULL.** SQLite treats a `CHECK` whose expression
is NULL as *satisfied*, so the three-rule version above accepts a `fixed` timer with a NULL close
offset, a `variance` band with only one edge, and an `unknown` window that kept a close offset —
each reaching the derivation as a window it cannot read. The pairing rule is the branch the other
three were leaning on and never stated.

*Rejected: storing `(base_respawn, variance)`.* It cannot express an asymmetric window without
inventing a sign convention, and P99 community data is quoted both ways — "7 days ±12h" and "16 to 24
hours" describe the same shape and would be entered differently by two officers. Two offsets are
exactly what the API returns, so there is no conversion step to get wrong.

`circle_timer_override` carries the same window columns, PK `(circle_id, target_id)`. It exists
because these numbers are community-derived and genuinely disputed, and "our guild has tracked VS for
two years and the wiki is wrong" is a real thing an officer will say. Resolution order: circle
override → catalogue timer → `unknown`.

Names and aliases share **one namespace**: a spelling belongs to one target, whether it is that
target's name or an alias of it. Neither unique index can say so — SQLite has no constraint that
spans two tables — so `000005_raid_target_name_namespace.sql` enforces it with triggers. Without
it an alias can be hung on a different target, and the resolve ladder answers that spelling with
the canonical-name target because `name_norm` is rung two and `alias_norm` is rung four; the alias
resolves to somebody else's mob and its owner is never told.

`unknown` is a first-class answer, not a missing one. An instance that has never been handed a seed
resolves every target to it, reports `status: no_timer`, and records times of death exactly as it
otherwise would — canonical §15. The resolved timer still carries `is_quake_target`, so a quake
clears the board on an instance that knows nothing about windows.

**Timer numbers load from outside this repository.** `tod-serve seed timers --file` reads a JSON
document from `tod-serve-p99-seed`:

```json
{ "version": 1,
  "source": "tod-serve-p99-seed@<rev>",
  "timers": [ { "target": "Vulak`Aerr", "server": "blue", "window_kind": "variance",
                "window_open_offset_seconds": 0, "window_close_offset_seconds": 0,
                "note": "" } ] }
```

`version` and `source` are both required — the seed repository versions separately, and a window
nobody can attribute is a window nobody can dispute. `target` runs the resolve ladder, so a seed
needs no catalogue of its own; `target_id` pins one outright. Unknown fields are refused rather
than ignored. The whole file parses and every target resolves **before** the transaction opens a
write, so a seed that fails changes nothing.

Target IDENTITY is the other half and ships embedded, in `internal/catalogue`. `tod-serve seed
targets` loads it, and is additive: a target an operator corrected or retired is left alone, so the
command is safe to re-run on every upgrade.

### `tod_report` — append-only, the core

| Column | Notes |
|---|---|
| `id` | ULID — time-ordered, so it is also the cursor |
| `circle_id`, `target_id` | |
| `kind` | `kill \| retraction` |
| `died_at` | **game truth**, may be backdated |
| `reported_at` | **system truth**, never backdated |
| `reporter_membership_id` | NOT NULL — names the reporter even after revocation |
| `source` | `log_line \| manual \| api \| import` |
| `self_confidence` | `certain \| probable \| guess` |
| `source_line` | `` Vulak`Aerr has been slain by Tankguy! `` |
| `source_character` | `Tankguy`, parsed from the line |
| `log_character` | whose `eqlog_<Character>_<server>.txt` it came from |
| `killed_by_guild` | self-asserted; the intel officers actually want |
| `client_clock_offset_seconds` | the plugin's own skew estimate |
| `retracts_report_id` | set iff `kind = 'retraction'` |

```sql
CHECK ((kind = 'retraction') = (retracts_report_id IS NOT NULL))
CHECK (died_at <= reported_at + 120000000)   -- Micros; +120s clock-skew tolerance

CREATE UNIQUE INDEX ux_tod_report_natural
  ON tod_report(circle_id, target_id, reporter_membership_id, died_at) WHERE kind = 'kill';
```

**Every reference from a circle-scoped table to a circle-scoped table carries `circle_id`** — the
foreign key is `(circle_id, reporter_membership_id) REFERENCES membership (circle_id, id)`, not
`(reporter_membership_id) REFERENCES membership (id)`. A single-column key proves the reporter
exists; it does not prove they are in *this* circle, and a report filed in circle B naming a
reporter from circle A would satisfy both keys individually while corrupting B's consensus. The same
applies to `retracts_report_id`, to every `*_membership_id`, and to `admitted_by_invite_id`.

That natural key is a second line of defence behind `Idempotency-Key`: the same reporter cannot lodge
the same kill twice even if the header is botched. A *correction* by the same reporter has a
different `died_at`, so it is unaffected. A duplicate returns `200` with the existing report — a
replay, not an error.

### `quake_event` — append-only

`id`, `circle_id`, `occurred_at`, `reported_at`, `reported_by_membership_id`, `source`, `note`.

`occurred_at` is **game truth** and may be backdated; `reported_at` is **system truth** and never
is — the same split `tod_report` carries, and it carries the same
`CHECK (occurred_at <= reported_at + 120000000)`. A quake in the future is impossible independent of
any derivation.

An earthquake repops every raid target on the server at once. Modelling that as N kill reports would
be a lie — nobody observed N kills — and it would corrupt every confidence figure on the board.

**This is a correctness requirement, not a nicety.** Without it, every window in the circle is wrong
for a week after a quake, and wrong *confidently*, which is the failure mode this project is built
against.

`tod.quake.report` is officer-only. A false quake wipes the whole board.

### `target_state_cache` — droppable

`circle_id`, `target_id` (PK), `computed_at`, `latest_report_id`, `report_count`, plus every derived
field from [consensus](03-consensus.md).

Invalidated on any insert into `tod_report`/`quake_event` for that `(circle, target)` and on any
timer change. Rebuilt lazily on read-miss and wholly by `tod-serve rebuild-states`. A nightly job
recomputes every state from the reports and diffs; **the recomputation wins and an alert fires.**

### `identity_provider`, `auth_flow` and `credential_ticket`

[ADR-0011](../adr/0011-operator-registered-discord-application.md) makes the Discord application
**per-instance and operator-registered**, so the instance is a confidential OAuth client rather than
a relay for somebody else's app. `identity_provider` therefore gains:

| Column | Notes |
|---|---|
| `client_id` | The operator's own application. Public; returned by `listIdentityProviders`. |
| `client_secret` | `core.Secret` — **never serialised, never logged**, `***` in every renderer |
| `redirect_uri` | Must match what the operator registered with the provider |
| `token_endpoint` | Fixed for `discord`, discovered for `oidc` |

```sql
CHECK ((kind = 'discord') = (client_id IS NOT NULL))
```

`auth_flow` — `id`, `state` (unique), `pkce_verifier`, `provider_id`, `invite_code_hash` (nullable),
`circle_id` (nullable, **advisory** — it selects the OAuth scopes and the guild to check, and is
re-derived at redemption). It is populated **only by resolving `invite_code`**, never from caller
input: `createAuthorizationURL` accepts no `circle_id`, because a public route that resolved one
would confirm a circle's existence to anybody who guessed an id. `expires_at`, `consumed_at`.

A row is written **only for a request that passes the shared invite rate limit**
([02-api-design](02-api-design.md#one-shared-bucket-for-invite-code-probing)),
so a rejected probe costs no storage. Rows are short-lived, capped per caller, and swept on expiry
along with `credential_ticket` — an unredeemed flow is litter, not history, and nothing reads it
after `expires_at`. **The PKCE verifier stays on the server**: a
confidential client has a `client_secret` to bind the exchange, and handing the verifier to the
browser would buy nothing and leak it into `sessionStorage`. `invite_code_hash`, not the code —
the same reasoning as `invite.code_hash`.

`credential_ticket`'s 120-second TTL is a `CHECK (expires_at = created_at + 120000000)`, and its
single use is a `BEFORE UPDATE` trigger that aborts once `consumed_at` is set, so neither a
long-lived ticket nor a replay is representable rather than merely rejected. A second trigger
freezes the provider's facts after minting: the guild roles on the row *are* the gate's input, and a
gate evaluated against an edited copy is not a gate.

`credential_ticket` — `id`, `ticket_hash` (unique), `provider_id`, `subject`, `display_name`,
`guild_roles_json` (gated guild id → role ids, from `users/@me/guilds/{guild.id}/member`; a guild
the subject is not in is simply absent, since that call `404`s), `expires_at`, `consumed_at`. There
is deliberately **no** full guild list: one endpoint answers membership and roles for the guild that
gates this circle, so the broader `guilds` scope is never requested and the subject's other guilds
are never learned. Minted by `completeAuthorization`,
**single-use**, 120-second TTL, and it carries the provider's *facts* precisely so that the Discord
access token can be discarded inside the request that read them.

Both are instance-scoped and on the
[canonical §9](00-canonical-conventions.md#9-tenancy--this-project-diverges-from-dkp) allowlist
because **no circle owns them**, not because a circle cannot be identified early. `auth_flow`'s
`circle_id` is nullable and advisory — it parameterises the provider request — and a
`credential_ticket` is redeemable at either `/join` or `/sessions`, so which circle it lands in is
settled at redemption. Making either circle-scoped would demand `circle_id NOT NULL`, which is a
false statement about a row that exists before the caller holds any membership. Both are looked up
by an unguessable secret on a unique index and never by circle, so a missing `WHERE circle_id = ?`
cannot leak across tenants here. Both are prunable on expiry; neither is authority for anything.

### `identity.blocked_at` — the instance-wide block

`identity` gains `blocked_at`, `blocked_by_membership_id` and `block_reason`. A blocked identity is
refused at **join and at ticket redemption**, so a banned Discord id cannot join *any* circle on the
instance — including a new one, and including one whose officers have never heard of them.

This is deliberately **not** a replacement for `revokeMember`, which stays the normal tool:
per-circle revocation is the officers' decision about their own circle, and it takes effect on the
very next request. `blocked_at` is the instance operator's decision about their whole instance, and
it is the only thing that stops a re-join into a circle the operator does not run.

**Enforced by:** `TestJoin_BlockedIdentity_Refused` — `403 identity_blocked`.

### `circle_provider` — where the Discord gate lives

| Column | Notes |
|---|---|
| `circle_id`, `provider_id` | PK |
| `discord_guild_id` | TEXT NULL — the guild whose membership this circle requires |
| `discord_required_role_ids_json` | TEXT NOT NULL DEFAULT `'[]'` — **empty means "anyone in the guild"** |

Discord has **no channel-membership API**. Channel visibility is derived from guild membership plus
roles, which is how the channel an officer is actually thinking of is gated, so that is what the
server can check and therefore what it models. Anything else would be a guess dressed as a rule.

The gate is circle-scoped, not instance-scoped, because **the instance owns the application and the
circle owns the gate**: two circles on one instance may point at two different guilds, which is the
whole reason `circle_provider` already exists. It is evaluated in **both** `/join` and `/sessions`,
against the facts on the 120-second ticket — `403 guild_membership_required` or
`403 guild_role_required`.

**Enforced by:** `TestGuildGate_EvaluatedOnJoinAndSessions`.

## Deferred, with reasons

**`spawn_sighting`.** A tracker seeing a target already up is real intel and resolves an overdue
window. Deferred to 1.1 because it needs its own trust story — a false "it's up" is as damaging as a
false ToD — and because `overdue` already conveys much of it with less machinery.

When it lands it is a *separate* append-only table, **not** a nullable `seen_at` column on
`tod_report`. A polymorphic table with nullable time columns is how a derivation gets quietly wrong.

**Rotation.** "Our circle claims the 14:00–02:00 Trak window" is the natural next ask and it drags in
scheduling, conflict resolution and cross-circle negotiation. It is named here so that it arrives as
a project rather than as a small addition to `target_state`.

**Per-reporter clock-skew correction.** See [consensus §8](03-consensus.md#8-known-weaknesses).

**Instance-realm `audit_log` rows.** This document said "circle rows carry `circle_id`; instance
rows do not", and [canonical §9](00-canonical-conventions.md#9-tenancy--this-project-diverges-from-dkp)
says every table not on the allowlist carries `circle_id NOT NULL`. Both cannot hold, and canonical
conventions is the tie-breaker, so `audit_log.circle_id` is `NOT NULL` and there is nowhere yet for
an instance-realm audit row. Giving it one is a reviewed decision — a separate `instance_audit_log`
table, or `audit_log` on the allowlist — and it is deliberately not made here.

**The `permission` and `role_permission` seed tables.** [Canonical §6](00-canonical-conventions.md#6-permissions-and-scopes--one-catalogue-generated)
makes `role_permission` FK-constrained to `permission(key)`, which is what turns a divergent
hand-written list into a boot failure. Those two tables appear in neither scope list above, so
creating them needs an allowlist decision as well; they land with the authorization seed rather than
being added to the schema quietly.
