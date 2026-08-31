# ADR-0002 — Make the circle the tenant, with `circle_id` on every scoped row

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

## Context and problem statement

The unit that pools time-of-death data is not always a guild. It is often four people who share a
Nagafen clock, and one person belongs to several such groups at once and chooses per kill which ones
a report goes to.

Dragon Kill Party solved tenancy by deleting it:
[ADR-0004](https://github.com/prokopto-dev/dragonkillparty/blob/main/docs/adr/0004-single-guild-per-instance.md)
states "exactly one guild per instance and no `guild_id` column. Do not add one 'for later'." The
reasoning is sound and is not in dispute here — a missing `WHERE guild_id = ?` is a silent
cross-tenant leak that no test catches by accident, and removing the column removes the bug class.

The question this ADR forces: does tod-serve inherit that rule, given that the data being leaked is
competitive intelligence and the tenant is sometimes four people?

## Considered options

| Option | For | Against |
|---|---|---|
| A — One instance per circle, inheriting ADR-0004 | The leak class does not exist. Schema stays trivial. Consistent with the sibling project | Every group self-hosts and exposes a port. For four friends that is a VPS or a port-forward, which is the barrier that stops the product being used at all |
| B — Circles inside one instance, `circle_id` everywhere | A group creates a circle with an invite code and never touches infrastructure. One host serves many groups | Reintroduces the exact bug class ADR-0004 deleted, in a product whose data is precisely what rivals want |
| C — Circles, but one SQLite file per circle | Isolation is physical; no `WHERE` clause can leak across files | Cross-circle operations (one person's destination list, one token) need a coordinator anyway, and per-file connection management at hundreds of circles is its own failure mode |

## Decision outcome

**Chosen: B.** The trade differs from DKP's because the *tenant* differs. DKP's tenant is sixty
people with a year of ledger history, where "run a second container" is proportionate to what is
being protected. A circle is four friends with a spawn clock, and requiring a container per circle
means the product does not exist for the population that most needs it.

Every circle-scoped table carries `circle_id NOT NULL REFERENCES circle(id)`. The safety ADR-0004
bought by deleting the column is bought back by three mechanisms, not by a promise:

| Gate | Asserts |
|---|---|
| Schema test against an explicit instance-scoped allowlist | Every table not on the allowlist has `circle_id NOT NULL REFERENCES circle(id)` |
| `TEN001` over `db/queries/*.sql` | Every circle-scoped query names `circle_id` in its `WHERE` |
| `TestTenancy_CrossCircle_EveryOperationDenies` | Walks the **route registry** — not a hand-written list — and asserts a principal of circle A gets `404` on every circle-scoped operation against circle B |

The third is load-bearing. Derived from the registry, a new circle-scoped route with no coverage is a
red test rather than an omission somebody has to remember.

Cross-circle access returns **`404`, never `403`**. A `403` confirms the circle exists and that the
caller found a valid id; a circle's existence is part of what it is hiding.

The instance-scoped allowlist is explicit and short: `tod_meta`, `instance`, `identity_provider`,
`identity`, `identity_link`, `instance_grant`, `instance_setting_change`, `auth_flow`,
`credential_ticket`, `raid_target`, `raid_target_alias`, `raid_target_timer`,
`api_token`, `session_revocation`, `idempotency_record`, `event_outbox`. Adding
to it is a reviewed decision. The list is written the same way in
[canonical §9](../design/00-canonical-conventions.md), and
`TestInstanceScopedAllowlist_TheADRAndCanonical_Agree` diffs the two — this copy sat three tables
short of it for a while, and nothing noticed.

Option C is not ruled out forever; it is ruled out now because it moves the isolation problem into
connection management without removing the need for an instance-level coordinator.

### Consequences

- Good, because a group with no infrastructure can use the product in the time it takes to paste an
  invite code.
- Good, because the multi-destination client composes cleanly: a destination is `(endpoint, token,
  circle)`, and it does not matter whether two of them share a host.
- Good, because someone who wants nobody else reading their circle can still run their own binary —
  the two models coexist rather than compete.
- **Bad, because the silent cross-tenant leak class is back.** Three gates reduce it to "a leak
  requires defeating a schema test, a query gate and a route-derived isolation test", but that is
  strictly weaker than a bug that cannot be expressed.
- **Bad, because whoever operates a host can read every circle on it.** No design at this weight
  class changes that, and the README says so plainly rather than implying otherwise.
- **Bad, because `circle_id` appears in nearly every query**, and the reviewer burden is permanent.
- **Bad, because it diverges from the sibling project** on a rule that project states emphatically,
  so anyone moving between the two carries a wrong instinct in one direction or the other.

### Reversal cost

A release. Collapsing to single-tenant means dropping `circle_id`, migrating each circle into its own
database, and breaking every deployed plugin's destination list — the client-visible half is the
expensive part, not the schema.
