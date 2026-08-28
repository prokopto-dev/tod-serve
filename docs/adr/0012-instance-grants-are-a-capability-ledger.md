# ADR-0012 — Instance permissions are a capability ledger on an identity

**Status:** accepted; implication superseded by
[ADR-0015](0015-instance-owner-implies-the-instance-realm.md) · **Date:** 2026-08-20 ·
**Deciders:** Courtney Caldwell

## Context and problem statement

Five permissions are `RealmInstance` — `catalogue.manage`, `ops.read`, `instance.circle.create`,
`instance.security.manage`, `instance.owner` — and **nothing grants them.** `internal/authz` pinned
that hole open since M0a rather than let it be assumed, because
[canonical §6](../design/00-canonical-conventions.md#6-permissions-and-scopes--one-catalogue-generated)
has no instance role enum and inventing one would put a second authorization model in the codebase.

The consequence is not theoretical: configuring the Discord provider is `instance.security.manage`,
so it is CLI-only and the admin console cannot administer the instance at all.

## Considered options

| Option | For | Against |
|---|---|---|
| A — `instance_grant`, a capability list keyed `(identity, permission)` | No new vocabulary: what is granted is a permission the catalogue defines, and nothing is implied by anything else | Revocation is a `DELETE`, so the table remembers nothing. `audit_log.circle_id` is `NOT NULL` and an instance event belongs to no circle, so the history needs a second audit mechanism |
| B — An instance role enum mirroring circle roles | Familiar shape; one column on `identity` | Exactly the second authorization model `internal/authz` warns against: a second matrix, a second ordering rule. And it grants by implication — `ops.read` arrives because somebody is an admin |
| C — `is_instance_owner` on `identity`, the other four derived | One boolean, no new table | Derivation *is* implication: nothing could hand somebody `ops.read` for a dashboard without also handing them the identity providers, which is the whole reason `circle.manage` and `circle.security.manage` are separate keys. *(Revisited by [ADR-0015](0015-instance-owner-implies-the-instance-realm.md).)* |
| **D — `instance_grant` as an append-only, hash-chained ledger of decisions** | A's capability list with A's missing half built in: every grant and revocation is a durable, attributed, chained row. One table, no second audit log | The current state is a derivation rather than a row, and the table grows with decisions rather than grants |

## Decision outcome

**Chosen: D.** A and D differ only in what becomes of a revocation, and A's answer is "it is
forgotten unless a second mechanism remembers it". Handing somebody the instance's identity
providers is exactly the event an audit log exists for, and the audit log this project has cannot
hold it. An append-only, hash-chained grant table is less machinery than a capability list plus an
instance-scoped audit log, and it cannot drift from what it describes because it *is* them.

`instance_grant` carries no `circle_id`, so it is on the
[canonical §9](../design/00-canonical-conventions.md#9-tenancy--this-project-diverges-from-dkp)
allowlist and in `INSTANCE_SCOPED` in `scripts/repo-gates.sh`, which one test already compares.

**Nothing here is ordered by id, and that is load-bearing.** A ULID is monotonic within one
generator, and each console invocation builds its own — so two inside a millisecond can mint out of
order. Each row therefore names the row it supersedes, and `UNIQUE (supersedes_id)` plus a partial
`UNIQUE (identity_id, permission) WHERE supersedes_id IS NULL` make each pair one chain with one
tail. The hash chain's tail is derived the same way: the row whose hash nothing names.

**`permission` is restricted to the instance realm by a generated `CHECK`,** rendered into
`db/enums.hcl` from `authz.Permissions()` filtered on realm — the path every other enumerated column
takes. A circle-realm key here is unrepresentable rather than merely rejected, and the value list
cannot drift from the catalogue because it is generated from it.

**Step-up and PAT.** No scope grants an instance-realm permission, so every route carrying one is
session-only, and `TestRouteRegistry_EveryInstanceRealmRoute_IsSessionOnly` holds that over the
registry rather than a list. Four are also in the capability floor and need a re-authenticated
session; **`ops.read` is not, and canonical §6 is parsed, so that is a fact rather than an
oversight** — reading diagnostics does not cost what adding a hostile OIDC issuer costs.

**Bootstrap: the console goes first, always.** A grant names an identity, an identity is created by
joining a circle, and a fresh database has neither. So the first grant is written by
`tod-serve instance grant`, which holds the database and needs no credential — the standing
`tod-serve circle create` already has, creating a circle without `instance.circle.create`. Those
rows record `granted_by` as NULL, which reads as "the operator at the console" and is a different
fact from a person having decided it. `tod-serve init` now prints the route from a fresh database to
an administrable instance.

**Last owner: no.** The circle rule exists because the API is the only way into a circle; an
instance with no `instance.owner` grant left is still administrable from the console, which is where
it was administered from first. A `last_owner (409)` here would forbid the console the one operation
it exists to make possible.

### Consequences

- Good, because the instance-realm routes become reachable by a decision somebody made and can be
  shown to have made, rather than by a role they happen to hold.
- Good, because granting `ops.read` for a dashboard hands over nothing else: every key is separate
  because every key was granted separately. *(One exception since:
  [ADR-0015](0015-instance-owner-implies-the-instance-realm.md).)*
- Good, because a revocation survives, and the chain makes a hand-deleted row visible.
- **Bad, because instance grants are console-only in this change.** `instance.owner` is grantable
  and reaches no route, so a console can use the identity providers but cannot hand that ability to
  a second operator without shell access.
- **Bad, because the effective grant is a query, not a row**, run per request behind a session
  where a capability list would have been one indexed read.
- **Bad, because the ledger grows without bound**, in decisions rather than grants, and nothing
  prunes it.
- **Bad, because an identity link does not carry grants.**

### Reversal cost

Low. The table is additive and nothing references it; the authorization path asks for an instance
set and would ask a different source unchanged. Reverting to A keeps the tail rows and drops the
rest; reverting to B or C re-opens the audit question this ADR answers.
