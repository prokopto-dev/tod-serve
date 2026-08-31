# ADR-0015 — `instance.owner` implies the instance realm

**Status:** accepted · **Date:** 2026-08-28 · **Deciders:** Courtney Caldwell

Supersedes the no-implication half of
[ADR-0012](0012-instance-grants-are-a-capability-ledger.md). Its ledger decision stands unchanged.

## Context and problem statement

ADR-0012 gave every instance-realm key its own grant and no implication between them. Four of the
five are required by routes. `instance.owner` is required by none, `EffectiveForSession` was a plain
union, and no other line of code named it — so `tod-serve instance grant --permission instance.owner`
wrote a durable, hash-chained, audited decision that granted **nothing**, while its own catalogue
summary called it "whatever an instance administrator can do that has no narrower key", `tod-serve
init` printed it as the last bootstrap step, and `docs/operations/deployment.md` §6 told the operator
to make exactly that grant. This is not hypothetical: it cost a setup session on `tod.prokopto.dev`.

Either the key means something or it must stop being grantable. Leaving it is the one option that
keeps a normative document telling operators to run a command that does nothing.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Delete `instance.owner` from the catalogue | Restores ADR-0012's no-implication rule exactly, with no new concept | `instance_grant.permission`'s `CHECK` is GENERATED from the catalogue, and the table is append-only: rows naming it exist on live instances and deleting the key makes their own history unrepresentable |
| B — Give it routes of its own — grant and revoke instance permissions over the API | A meaning with no implication at all | That is the console-only boundary ADR-0012 deliberately drew, re-litigated for an unrelated reason. And it leaves the key granting nothing against every route that exists today |
| **C — It expands to the instance realm, at the authorization boundary** | Makes the summary, `init`'s output and the runbook true at once, without a second granting model | An implication in a catalogue that had none, and a key whose reach grows when the realm does |
| D — Leave it; document that it is inert | No code change | The runbook would have to tell operators to grant `instance.security.manage` instead, and a permission nothing can check is one somebody grants again next year |

## Decision outcome

**Chosen: C**, and the point worth stating is why the implication ADR-0012 rejected is now accepted.

What ADR-0012 rejected was implication **as the granting model**: an instance role enum (B) or a
boolean on `identity` with the other four derived (C), both of which make `ops.read` arrive because
somebody is an admin. That objection is intact and this does not touch it. Every narrower key is
still separately grantable, separately revocable and separately audited, so granting `ops.read` for
a dashboard still hands over nothing else — ADR-0012's consequence, unchanged, and
`TestEffectiveForSession_AnInstanceGrant_AddsExactlyThatPermission` is what holds it.

The difference from ADR-0012's option C is **where the derivation lives**. That option put it in
STORAGE — one boolean, the other four inferred and never decided by anybody — which is what made
the audit question unanswerable. This puts it at the AUTHORIZATION BOUNDARY: `instance_grant` still
records exactly the decision a person made, one row, revoked in one act, and `authz.Implies` is read
per request by `EffectiveForSession`. The ledger never stores an expansion, so revoking
`instance.owner` cannot leave four rows standing.

It is one key, one direction, one pass. `Implies` is non-empty only for `instance.owner`, derived
from `Realm` rather than listed, and
`TestPermissions_EveryPermission_IsRequiredByARouteOrExpandsToOnesThatAre` requires every member of
an expansion to be independently reachable — so this cannot become a chain. `EffectiveForToken`
takes no instance set, so none of it reaches a personal access token at any scope;
`TestPrincipal_APATCarryingEveryInstanceGrant_ReachesNoneOfThem` drives a principal carrying every
grant and asserts it reaches none of them.

### Consequences

- Good, because the bootstrap ADR-0012 describes now produces a working administrator, and
  `deploy/smoke.sh` executes that walkthrough against the shipped image on every build.
- Good, because a key added to the instance realm is one an owner holds without anybody remembering
  to append to a list — the expansion is derived from `Realm`, and the gate above stops it becoming
  a key that expands into nothing again.
- Good, because `tod-serve doctor` can now answer "can anybody administer this instance", which it
  could not while the answer depended on a permission nothing consulted.
- **Bad, because there is now an implication in a catalogue that had none.** The next person wanting
  a convenience key has a precedent to point at. The gate constrains its shape; nothing constrains
  the appetite for another one.
- **Bad, because a future instance-realm permission is granted retroactively** to every holder of
  `instance.owner`, with no new decision and no new row. That is what "derived from `Realm`" buys
  and it is also what it costs: adding a key widens live grants at deploy time. *(Proposed first
  exercise: [ADR-0019](0019-an-administrator-sees-a-circles-metadata-never-its-content.md).)*
- **Bad, because the ledger and the effective set no longer read the same.** `tod-serve instance
  grants` shows one row where the holder has five permissions, so an operator answering "who can do
  X" from that listing is wrong unless they know about the expansion. `doctor` reports the expansion;
  the listing deliberately reports decisions.

### Reversal cost

Low, and asymmetric. Deleting `Implies` and restoring the union is a few lines, and the ledger needs
no migration because it never stored an expansion. But every `instance.owner` row goes back to
granting nothing — the state this ADR exists to fix — so a reversal is only safe if each holder is
granted the narrower keys first, in the same change.
