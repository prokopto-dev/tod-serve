# ADR-0019 — An instance administrator sees a circle's metadata, never its content

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** Courtney Caldwell

## Context and problem statement

`instance.owner` expands to the instance realm
([ADR-0015](0015-instance-owner-implies-the-instance-realm.md)), whose five keys are all
instance-wide: none of them reads a circle. `circle.read`, `member.read` and `circle.delete` are
`RealmCircle`, so a non-member administrator gets `404` on all 25 served circle-scoped routes,
enumerated from the registry in #29. An unwanted circle cannot be removed, and `listCircles` is
`AuthSelf`, so unjoined circles are invisible. Content isolation is **absolute** today and
load-bearing: [ADR-0002](0002-circle-is-the-tenant.md) gave up cross-circle views on purpose and law
5 — `404`, never `403` — buys it back.

## Considered options

| Option | For | Against |
|---|---|---|
| **A — Leave it closed; an administrator joins like anyone else** | No key, view, route or gate, and every gate keeps a statement with no exceptions | A membership is **wider than the ask**: it carries `tod.read`, so the administrator reads the content this refuses, and it makes a circle's owner the gatekeeper of the operator's ability to act on abuse |
| B — Widen `getCircle`, `listMembers` and `deleteCircle` to take an instance key | No new route, no second view type | One operation would answer two shapes depending on the caller, so the boundary would be a branch in a handler rather than a type |
| **C — Two instance-realm keys and separate metadata-only routes** | Enumerable: two keys, one field set, its own routes and gates | Live `instance.owner` grants widen at deploy, because the realm's expansion is derived |

## Decision outcome

**Chosen: C.** A deserves the most weight and fails on the axis it wins on: a membership grants
*more* than the ask — existence and roster **and** every report in the circle.

Two keys, `RealmInstance`, both in the capability floor — session-only, re-authenticated, no scope,
because reading a roster you are not in should cost a stolen session:

- **`instance.circle.read`** — existence, name, server, `created_at`, and the member roster with
  roles and join times.
- **`instance.circle.delete`** — tombstone an unwanted circle, and nothing else.

**Refused, and still `404`:** ToD reports, quake events, target states, timer overrides, invites,
circle audit. Prose does not hold that list: the response is its own type, so a field the handler
cannot render cannot leak — also why the routes are new rather than widened (B). **No membership and
no credential:** it writes no `membership` row and mints no token, so an invite stays the only way
in ([ADR-0007](0007-one-join-endpoint.md)).

### The crossing is classified, not excepted

The routes stay `CircleScoped`: `TestRouteRegistry_CircleScoped_MatchesThePath` ties that flag to
`{circle_id}` in both directions, and renaming the parameter to escape it would be evasion by
spelling. What is new is a registry field naming the instance key a route may be crossed with, read
in `authorize` beside the tenancy check and not inside it, so `checkTenancy` keeps the branchless
rule its comment promises. `TestTenancy_CrossCircle_EveryOperationDenies` therefore needs no
exception: its principal is another circle's member holding no grant, and still gets `404` on all 25
and on these. Two registry-derived gates cover the crossing: every route naming a key requires it
and is session-only, and an administrator holding both keys, driven at every circle-scoped route
with **real** ids rather than #29's placeholders, gets `404` on every route naming none.

### Widening `instance.owner` is the decision, not a side effect

`Implies` derives the expansion from `Realm`, so these keys take every live `instance.owner` grant
from five permissions to seven at deploy, with no new row and nobody deciding. ADR-0015 named that
cost; this is its first exercise, accepted because an owner already holds `instance.security.manage`
and operates the host, which ADR-0002 says plainly can read every circle on it — an **audited** path
to less than they already reach unaudited.

What must not stay silent is the widening: `TestImplies_InstanceOwner_ExpandsToExactlyTheseKeys`
pins the expansion against a written-out list, so the *next* key cannot land without somebody
editing it and a reviewer seeing that. `Implies` stays derived.

### Deletion stays a tombstone, and every read is audited

`deleteCircle` writes `deleted_at` and cannot remove the row, because the append-only tables
reference it. So an administrator's delete hides a circle and erases nothing: every report and audit
row stands, and revoked members' reports still count.

Every read is audited into the circle's **own** `audit_log`, so the people whose roster was read see
who read it. `actor_membership_id` stays NULL — the FK is to `membership(circle_id, id)` and there
is no membership — so the identity goes in `detail_json`, which the chain hash covers. A column is
not an option: `ChainHash` covers every field, so adding one rewrites every stored hash.

### What the implementation PR is held to

Whole-value `go-cmp` of the metadata view against a golden, both directions, so no adjacent field
arrives unnoticed; the two crossing gates above are the rest.

### Consequences

- Good, because an operator can remove an abusive circle without a shell or a membership, and the
  circle can see afterwards that they looked.
- **Bad, because content isolation is no longer absolute**, and "does an administrator see this" is
  now a question every future circle-scoped field answers.
- **Bad, because every live `instance.owner` grant widens on deploy**, retroactively and unrecorded.
- **Bad, because `authorize` gains a branch the tenancy rule did not have**, and what keeps it off
  the oracle is a capability a prober lacks rather than there being no second answer.
- **Bad, because a read's actor is in `detail_json`, not a foreign key**, so nothing in the schema
  forces it to be there — a write-path test is the whole mechanism.

### Reversal cost

Low, and no migration: the routes, the field and the view are one package's change. The keys cannot
be removed — `instance_grant` is append-only — so they stay in the catalogue granting nothing, the
state ADR-0015 exists to fix.
