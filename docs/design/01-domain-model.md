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
| `identity` | append-mostly | `(provider_id, subject)` → a person, instance-wide. |
| `identity_link` | append-only | Officer-asserted equivalence between two **verifiable** identities. |
| `raid_target` | mutable | Catalogue: mob identity. Server-agnostic. |
| `raid_target_alias` | mutable | `VA`, `Naggy`, `Vox`, `Trak` → target. |
| `raid_target_timer` | mutable | **Per-server** respawn window. PK `(target_id, server)`. |
| `api_token` | mutable | Opaque PATs, bound to a membership. |
| `idempotency_record` | mutable | `(principal_id, key)` → request hash, response, state. |
| `event_outbox` | append-only | Global `event_seq`, SSE delivery. |

## Circle-scoped tables

Every row carries `circle_id NOT NULL REFERENCES circle(id)`.

| Table | Mutability | Purpose |
|---|---|---|
| `circle` | mutable | The tenant. |
| `circle_provider` | mutable | Which instance providers this circle accepts. |
| `membership` | mutable | `(circle, identity)` → role + revocation. Mutable because a role change and a revocation are *state*, not events. |
| `invite` | mutable | `uses` and `revoked_at` mutate. |
| `invite_redemption` | append-only | Who redeemed what, when. |
| `tod_report` | **append-only** | The core log. |
| `quake_event` | **append-only** | Server-wide repop. |
| `circle_timer_override` | mutable | This circle disagrees with the catalogue about a target's window. |
| `target_state_cache` | droppable cache | Materialised consensus. **Never authority.** |
| `audit_log` | append-only | Hash-chained. Circle rows carry `circle_id`; instance rows do not. |

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
CHECK ((window_kind = 'fixed')   = (window_open_offset_seconds = window_close_offset_seconds))
CHECK (window_close_offset_seconds >= window_open_offset_seconds)
```

*Rejected: storing `(base_respawn, variance)`.* It cannot express an asymmetric window without
inventing a sign convention, and P99 community data is quoted both ways — "7 days ±12h" and "16 to 24
hours" describe the same shape and would be entered differently by two officers. Two offsets are
exactly what the API returns, so there is no conversion step to get wrong.

`circle_timer_override` carries the same window columns, PK `(circle_id, target_id)`. It exists
because these numbers are community-derived and genuinely disputed, and "our guild has tracked VS for
two years and the wiki is wrong" is a real thing an officer will say. Resolution order: circle
override → catalogue timer → `unknown`.

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

That natural key is a second line of defence behind `Idempotency-Key`: the same reporter cannot lodge
the same kill twice even if the header is botched. A *correction* by the same reporter has a
different `died_at`, so it is unaffected. A duplicate returns `200` with the existing report — a
replay, not an error.

### `quake_event` — append-only

`id`, `circle_id`, `occurred_at`, `reported_by_membership_id`, `source`, `note`.

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
