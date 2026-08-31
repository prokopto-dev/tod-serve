# ADR-0020 — A device grant is identity-scoped and exchanges for per-membership tokens

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** Courtney Caldwell

## Context and problem statement

[ADR-0019](0019-a-device-authorization-grant-against-this-instance.md) chose a device authorization
grant but left open what an approval yields. The requirement is one "sign in with Discord" in the
plugin, after which a circle the user gains access to **later** appears without authenticating
again. [ADR-0005](0005-pats-bound-to-memberships.md) binds a PAT to a membership — but read it
closely: immediate revocation comes from membership state being checked on **every request**, not
from the binding. What the binding buys is **blast radius**, a leaked token costing one circle
instead of all. That is narrower than "a cross-circle credential is forbidden", so the token model
is open, not settled.

## Considered options

| Option | For | Against |
|---|---|---|
| a — One approval mints every current per-membership PAT | No new credential kind; ADR-0005 untouched | A circle joined later is invisible until the user approves again — precisely the requirement, so it fails by construction |
| b — An identity-scoped **grant** exchanging for short-lived per-membership tokens | New circles appear at the next exchange with no second login, and what reaches a route is still a membership-bound PAT. The refresh-token shape every device flow has | A second credential kind, an exchange endpoint, and a grant that is identity-wide and long-lived — the most valuable thing on a player's disk |
| c — One identity-scoped token, membership checked per request | Simplest client: one credential to store and one to revoke | A leak reaches every circle the identity is in, surrendering the one thing the binding buys and edging toward the `admin:*` scope the catalogue lacks |

## Decision outcome

**Chosen: b.** The requirement is not "fewer credentials" but "no second login when I join a
circle", which (a) fails by construction. (c) meets it by surrendering blast radius, the only thing
ADR-0005's binding buys. (b) keeps both.

**The grant authorizes no request.** Only the PATs it mints do, each an ordinary ADR-0005 token
bound to one membership and checked every request, so tenancy is untouched and no `admin:*`-shaped
scope appears.

`device_grant` is keyed on an **identity**: `identity` is unique on `(provider_id, subject)`, and
`instance_grant.identity_id` ([ADR-0012](0012-instance-grants-are-a-capability-ledger.md)) is the
precedent — what a grant answers outlives any one circle. **A row is a decision, not a state:**
approval and revocation are both rows, the table is append-only, and it is its own hash-chained
record on `audit.ChainHash`, which is what makes an approval auditable *before* any membership
exists.

**Exchange re-proves the gate; it does not inherit one.** `/sessions` re-reads the live circle gate
and calls `EvaluateGuildGate` before minting, because — `internal/identity/gate.go` — "a gate on
join alone would let `/sessions` mint a fresh PAT for somebody who has left the guild". Exchange
does that same re-read. Where `Gate.IsZero()` it mints for every current, non-revoked membership,
carrying ADR-0019's scope set and never widening it. Where the circle gates it needs facts, which no
approval can capture for a circle joined later —
[ADR-0021](0021-guild-facts-come-from-browser-flows.md) is where they come from and how they age. A
membership it cannot prove is **skipped and counted in the response**, never dropped silently.
`api_token.expires_at` already exists, so short TTLs need no schema change.

**Approval consumes a `provider_ticket`, not a session, so a memberless identity can approve.**
`auth.Session.MembershipID` *is* the principal — "a session, like a token, is bound to one
membership" — so a session holding none is not representable, and inventing one would be the new
principal kind this ADR claims not to add. A `credential_ticket` carries a verified subject, no
membership, and the guild facts the gate above needs
([ADR-0011](0011-operator-registered-discord-application.md)); `/join` and `/sessions` take it
through the one credential union ([ADR-0007](0007-one-join-endpoint.md)). Approval is a third
consumer: no new principal kind, no new `Auth` mode. For a subject this instance has never seen it
creates the `identity` row, and the exchange returns an **empty token set** until an invite is
redeemed.

**One Discord login cannot span instances, by design rather than by gap.** An identity is
`(provider_id, subject)` where `provider_id` is *that instance's* `identity_provider` row, because
ADR-0011 made each instance its own confidential client. So: **one login per instance, and within an
instance circles appear automatically**. Two guilds on two servers is two logins, the price of the
replay protection. **There is no instance registry**; the plugin adds one by URL,
an affordance somebody must build.

### Consequences

- Good, because it is one login per instance, and a circle joined later needs no second one.
- Good, because what reaches a route is still an ADR-0005 PAT: authz and tenancy are unchanged, and
  a revoked membership mints nothing.
- **Bad, because a guild-gate change is no longer caught within 120 seconds.** `/sessions` evaluates
  facts that fresh; the exchange reads stored ones up to `GateFactsTTL` old
  ([ADR-0021](0021-guild-facts-come-from-browser-flows.md)), so somebody who left the guild keeps
  refreshing until then. That window is the security this design spends.
- **Bad, because the grant is identity-wide and long-lived** — blast radius is per-circle on the
  tokens and whole-identity on the grant.
- **Bad, because two instances means two logins, forever,** which every user reads as a bug before
  reading it as replay protection.
- **Bad, because anyone who can sign in with Discord may hold a grant on a public instance** before
  an officer approved anything; the rows are append-only, so permanent.
- **Bad, because exchange writes rows:** three circles refreshed hourly is seventy-two `api_token`
  rows a day.

### Reversal cost

A release and a revocation row per live grant — **not** a migration dropping `device_grant`. That
table is the only record a memberless approval has, so erasing it would erase the audit this ADR
added; a reversal closes the ledger rather than deleting it. Clients fall back to option (a).
