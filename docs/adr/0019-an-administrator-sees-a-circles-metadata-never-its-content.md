# ADR-0019 — An instance administrator sees a circle's metadata, never its content

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** Courtney Caldwell

## Context and problem statement

`instance.owner` expands to the instance realm
([ADR-0015](0015-instance-owner-implies-the-instance-realm.md)), and that realm holds three keys.
`circle.read`, `member.read` and `circle.delete` are `RealmCircle`, so a non-member administrator
gets `404` on all 25 served circle-scoped routes, enumerated from the registry in #29. An unwanted
circle cannot be removed, and `listCircles` is `AuthSelf`, so circles nobody invited them to are
invisible. Content isolation is **absolute** today and load-bearing:
[ADR-0002](0002-circle-is-the-tenant.md) gave up cross-circle views on purpose and law 5 — `404`,
never `403` — buys it back.

## Considered options

| Option | For | Against |
|---|---|---|
| **A — Leave it closed; an administrator joins like anyone else** | No key, view, route or gate, and every gate keeps a statement with no exceptions | A membership is **wider than the ask**: it carries `tod.read`, so the administrator reads the content this refuses. And it makes a circle's owner the gatekeeper of the operator's ability to act on abuse |
| B — Widen `getCircle`, `listMembers` and `deleteCircle` to take an instance key | No new route, no second view type | Law 5's gate takes its subject from `CircleScoped`, so it becomes conditional on the caller |
| C — Console only: `tod-serve circle list` and `circle delete` | No HTTP surface, no permission | Needs SSH, and re-makes the console the only administration path, which [ADR-0012](0012-instance-grants-are-a-capability-ledger.md) left |
| **D — Two instance-realm keys and separate metadata-only routes** | Enumerable: two keys, one field set, its own routes and gates | Live `instance.owner` grants widen at deploy, because the realm's expansion is derived |

## Decision outcome

**Chosen: D.** A deserves the most weight and fails on the axis it wins on: a membership grants
*more* than the ask. The ask is existence and roster; a membership is both **and** every report in
the circle, plus a row in the roster being inspected.

Two keys, `RealmInstance`, both in the capability floor — session-only, re-authenticated, no scope,
because reading a roster you are not in should cost a stolen session:

- **`instance.circle.read`** — existence, name, server, `created_at`, member roster with roles and
  join times, member counts.
- **`instance.circle.delete`** — tombstone an unwanted circle, and nothing else.

**Refused, and still `404`:** ToD reports, quake events, target states, timer overrides, invites,
circle audit. Prose does not hold that list: the response is its own type, so a field the handler
cannot render cannot leak.

**The routes are new, not widened** (B): their own registry flag, not `CircleScoped`, so
`TestTenancy_CrossCircle_EveryOperationDenies` keeps an unconditional statement over the 25 — a gate
taught an exception stops being read as absolute. Without the key a caller gets `404`, so the routes
disclose nothing by existing. **No membership and no credential** — the read writes no `membership`
row and mints no token, so an invite stays the only way in ([ADR-0007](0007-one-join-endpoint.md)).

### Widening `instance.owner` is the decision, not a side effect

`Implies` derives the expansion from `Realm`, so these keys widen every live `instance.owner` grant
at deploy, with no new row and nobody deciding. ADR-0015 named that cost; this is its first
exercise, accepted because an owner already holds `instance.security.manage` — they can add a
provider and mint themselves in — and operates the host, which ADR-0002 says can read every circle
on it. This is an **audited** path to less than they already reach unaudited.

What must not stay silent is the widening: `TestImplies_InstanceOwner_ExpandsToExactlyTheseKeys`
pins the expansion against a written-out list, so the *next* key cannot land without somebody
editing that set and a reviewer seeing that live grants grew. `Implies` stays derived; the list is a
test's.

### Deletion stays a tombstone, and every read is audited

`deleteCircle` writes `deleted_at` and cannot remove the row, because the append-only tables
reference it. So an administrator's delete hides a circle and erases nothing: `tod_report`,
`quake_event` and `audit_log` are untouched, and revoked members' reports still count.

Every read is audited into the circle's **own** `audit_log`, so the people whose roster was read can
see who read it. `actor_membership_id` stays NULL — the FK is to `membership(circle_id, id)` and
there is no membership — and the identity goes in `detail_json`, which the chain hash covers. A
column is not an option: `ChainHash` covers every field, so adding one rewrites every stored hash.

### What the implementation PR is held to

- **Sub-resource level, not path level.** #29 found the tenancy suite only drove middleware with
  placeholder ids. Drive a non-member holding both keys at every registry route with **real** ids
  of the target circle; `404` outside the metadata set.
- **Exact set** — whole-value `go-cmp` of the view against a golden, both directions.
- **Registry-derived** — every route with the new flag requires one of the keys, is session-only and
  appends its audit row, so an uncovered one is red rather than missed.

### Consequences

- Good, because an operator can remove an abusive circle without a shell and without a membership,
  and the circle can see afterwards that they looked.
- Good, because the hole is enumerable: two keys, one field set, one flag.
- **Bad, because content isolation is no longer absolute**, and "does an administrator see this" is
  now a question every future circle-scoped field answers.
- **Bad, because every live `instance.owner` grant widens on deploy**, retroactively and unrecorded.
- **Bad, because a read's actor is in `detail_json`, not a foreign key**, so nothing in the schema
  forces it to be there; a test on the write path is the whole mechanism.

### Reversal cost

Low, and no migration: the routes, the flag and the view are one package's change. The keys cannot
be removed — `instance_grant` is append-only and rows naming them exist — so they stay in the
catalogue granting nothing, the state ADR-0015 exists to fix.
