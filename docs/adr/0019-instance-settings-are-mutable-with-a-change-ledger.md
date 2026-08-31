# ADR-0019 — Instance settings stay mutable, and their changes are their own ledger

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** owner

## Context and problem statement

`self_service_circle_creation` decides whether any authenticated principal may create a circle. It
was written once by the first-run wizard and no endpoint changed it, so the answer was fixed at the
moment the operator had the least information about how their instance would be used
([#42](https://github.com/prokopto-dev/tod-serve/issues/42)).

Making it changeable makes it an instance-wide **policy** decision, which is exactly what an audit
log exists for — and `audit_log.circle_id` is `NOT NULL`, so `internal/audit` cannot hold the
event. That is the same wall [ADR-0012](0012-instance-grants-are-a-capability-ledger.md) hit.
Leaving the change unrecorded would make "who turned this on" unanswerable on the one switch that
decides whether strangers may create tenants here.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Make `audit_log.circle_id` nullable | One audit table for everything; no new concept | It is a hash-chained, trigger-protected, append-only table on every live instance. SQLite cannot relax a `NOT NULL` in place, so this is a full table rebuild — which drops the triggers silently — on the one table whose whole value is that it was never rewritten. And a nullable tenancy key on a circle-scoped table is exactly what [ADR-0002](0002-circle-is-the-tenant.md)'s allowlist exists to keep out |
| B — Record the change in `instance_grant` | The instance-level ledger already exists, chained and trigger-protected | It is keyed `(identity, permission)` and answers "who may do what". A settings change is about no identity and no permission; it would need a synthetic pair, and `ux_instance_grant_head` would then forbid a second change to the same setting |
| **C — `instance_setting_change`: a second append-only, hash-chained instance-scoped ledger** | Same shape and the same `audit.ChainHash` as `instance_grant`, on a table with no history to preserve — so it is a `CREATE TABLE`, not a rebuild. The `instance` row stays mutable, so `/meta` and every `createCircle` check stay one read | A second instance-level audit surface, so "what happened on this instance" is two queries. The current value and its history are two places that a bug could let disagree |
| D — Derive the setting from the ledger, with no `instance` column | One source of truth by construction | Every `createCircle` and every `/meta` becomes a fold over an unbounded log, and first-run setup would have to write a ledger row before any identity exists to attribute it to |

## Decision outcome

**Chosen: C.** The state and the record of how it got there are different things with different
lifetimes: one is read on every request and must stay a column, and the other must never be
rewritten. Splitting them is what lets the second be append-only at all.

`db/schema.hcl` adds `instance_setting_change`, on the canonical §9 instance-scoped allowlist for
`instance_grant`'s reason — the row is about the whole instance, so a `circle_id` would be a false
column rather than a missing one. `000012_instance_setting_change_append_only.sql` is the
hand-written trigger pair, and `TestAppendOnly_TriggersFire_AfterAllMigrations` asserts they
**abort** rather than that they appear in `sqlite_master`. `internal/instancesettings` is the only
writer: it checks the caller's precondition, reads, updates and appends in one `IMMEDIATE`
transaction, so each `old_value` is the value the update actually replaced and no `If-Match` can be
decided against a version some other writer has already moved past.
`TestApply_EveryRow_ChainsOntoTheOneBeforeIt` recomputes every hash from the row's own fields
rather than trusting the writer.

`setting` is `CHECK`ed against the enum generated from `internal/schemaenum`, and the list is the
settings an endpoint may move. **`public_url` is not on it.** It must match the redirect URI
registered with every identity provider character for character; a mismatch redirects the browser
somewhere else, so no request arrives here and nothing logs a failure — the failure
[#26](https://github.com/prokopto-dev/tod-serve/issues/26) made loud at configuration time. It is
also resolved at boot from `$TOD_PUBLIC_URL` before the row is read, so a change would take effect
at some later restart. `updateInstanceSettings` refuses it with `422 field_immutable` and the CHECK
makes a row claiming otherwise unrepresentable — two mechanisms, because a handler refusal alone
is one `if` from being gone.

The routes carry `instance.security.manage`, not `instance.owner`. Ownership expands to the whole
instance realm ([ADR-0015](0015-instance-owner-implies-the-instance-realm.md)), so requiring it
would make delegating this one switch impossible without also handing over the providers, the
catalogue and the ops dashboard — a narrower route behind a wider grant. Every owner already holds
the narrower key through that expansion.

### Consequences

- Good, because the operator can change a policy decision after seeing how the instance is used,
  which is when they first have the information to make it.
- Good, because the change is attributable and durable: `instance_setting_change` is append-only by
  trigger and chained through the same function `audit_log` uses, so a row deleted by something that
  bypassed the trigger is visible in every row after it.
- Good, because nothing on the read path changed: `self_service_circle_creation` is still one column
  on a singleton.
- **Bad, because "what happened on this instance" is now three tables** — `audit_log` per circle,
  `instance_grant` for permissions and this for policy — and nothing joins them. A reader has to
  know all three exist.
- **Bad, because the current value and the ledger can disagree** if anything ever writes the
  `instance` row outside `internal/instancesettings`. First-run setup already does, deliberately,
  and its writes are not in the ledger: `Configured` is the boundary, and nothing enforces it.
- **Bad, because the ledger head is now load-bearing for caching.** The entity tag over these
  settings covers it, because `updated_at` is a clock reading and two commits can share a
  microsecond; a change to how the chain is derived is now also a change to what `304` means.

### Reversal cost

A release. Dropping the routes and the console card is a day; the table would stay, because
`instance_grant` and `audit_log` prove nobody deletes an append-only ledger, and the rows on live
instances would become unreadable history rather than disappearing.
