# ADR-0019 — A device grant is identity-scoped and exchanges for per-membership tokens

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** Courtney Caldwell

## Context and problem statement

[ADR-0018](0018-a-device-authorization-grant-against-this-instance.md) chose a device authorization
grant but left open what an approval yields. The requirement is one "sign in with Discord" in the
plugin, after which a circle the user gains access to **later** appears without authenticating
again. [ADR-0005](0005-pats-bound-to-memberships.md) binds a PAT to a membership — but read it
closely: immediate revocation comes from membership state being checked on **every request**, not
from the binding. What the binding buys is **blast radius**, a leaked token costing one circle
instead of all. That is real, and narrower than "a cross-circle credential is forbidden" — the
token model is open, not settled.

## Considered options

| Option | For | Against |
|---|---|---|
| a — One approval mints every current per-membership PAT | No new credential kind, ADR-0005 untouched, blast radius per-circle | A circle joined later is invisible until the user approves a device again — precisely the requirement, so it fails by construction |
| b — An identity-scoped **grant** that exchanges for short-lived per-membership tokens | New circles appear at the next exchange with no second login, and what reaches a route is still a membership-bound PAT, so blast radius is unchanged. It is the refresh-token shape every device flow has | A second credential kind and an exchange endpoint. The grant is identity-wide and long-lived, so it becomes the most valuable thing on a player's disk |
| c — One identity-scoped token, membership checked per request | Simplest client: one credential to store, one to revoke | Revocation still works, but a leak reaches every circle the identity is in — surrendering the one thing the binding buys, and edging toward the `admin:*` scope the catalogue lacks |

## Decision outcome

**Chosen: b.** The requirement is not "fewer credentials" but "no second login when I join a
circle", which (a) fails by construction. (c) meets it by surrendering blast radius, the only thing
ADR-0005's binding buys. (b) keeps both.

**The grant authorizes no request.** Only the PATs it mints do, each an ordinary ADR-0005 token
bound to one membership and checked every request — so tenancy and revocation are untouched, and no
`admin:*`-shaped scope appears.

`device_grant` is keyed on an **identity**, not a membership: `identity` is unique on
`(provider_id, subject)`, and `instance_grant.identity_id`
([ADR-0012](0012-instance-grants-are-a-capability-ledger.md)) is the precedent — what a grant
answers outlives any one circle. **A row there is a decision, not a state:** approval and revocation
are both rows, the table is append-only, and it is its own hash-chained audit record on
`audit.ChainHash` — which is what makes an approval auditable *before* any membership exists.
Exchange mints one token per current, non-revoked
membership, carrying ADR-0018's approved scope set and never widening it. `api_token.expires_at`
already exists, so short TTLs need no schema change; expired rows join `internal/sweep`. The grant
is a `core.Secret`, hashed like a PAT and never compared by value (`SECRET001`).

**One Discord login cannot span instances, by design rather than by gap.** An identity is
`(provider_id, subject)` where `provider_id` is *that instance's* `identity_provider` row, because
[ADR-0011](0011-operator-registered-discord-application.md) made each instance its own confidential
client, so a token minted by a shared app cannot be replayed at another. The shape is
therefore **one login per instance, and within an instance circles appear automatically**. One
instance is one login, which is most raiders; two guilds on two servers is two — the price of the
replay protection, not an oversight. **There is also no instance registry** — the plugin adds an
instance by URL, an affordance somebody has to build. Nothing here lists peer instances.

**Approval consumes a `provider_ticket`, not a session, so a memberless identity can approve.**
`auth.Session.MembershipID` *is* the
principal — "a session, like a token, is bound to one membership" — so a session holding none is not
representable, and inventing one would be the new principal kind this ADR claims not to add. The
browser flow already mints the right credential: a `credential_ticket` carries a verified subject
and no membership ([ADR-0011](0011-operator-registered-discord-application.md)), and `/join` and
`/sessions` take it through the one credential union ([ADR-0007](0007-one-join-endpoint.md)).
Approval is a third consumer of that union: no new principal kind, no new `Auth` mode, nothing in
the authorization path. It re-proves the identity rather than trusting a cookie, the right bar for
an identity-wide grant. For a subject this instance has never seen, approval creates the `identity`
row; the exchange returns an **empty token set** until an invite is redeemed.

### Consequences

- Good, because it is one login per instance, and a circle joined later needs no second one.
- Good, because what reaches a route is still an ADR-0005 PAT: authz, tenancy and revocation are
  unchanged.
- **Bad, because the grant is identity-wide and long-lived.** Blast radius is per-circle on the
  tokens and whole-identity on the grant: the thing worth stealing got more valuable.
- **Bad, because two instances means two logins, forever,** which every user will read as a bug
  before reading it as replay protection.
- **Bad, because anyone who can sign in with Discord may now hold a grant row on a public instance**
  before an officer approved anything. It reaches no route until an invite is redeemed, but the
  `identity` and `device_grant` rows are append-only, so permanent.
- **Bad, because short-lived tokens mean re-exchanging mid-raid,** and a client mishandling expiry
  fails at the worst moment.
- **Bad, because exchange writes rows:** three circles refreshed hourly is seventy-two `api_token`
  rows a day for the sweep to clear.

### Reversal cost

A release and a revocation row per live grant — **not** a migration dropping `device_grant`. That
table is the only record a memberless approval has, so erasing it would erase the audit this ADR
added; a reversal closes the ledger rather than deleting it. Clients fall back to option (a): a real
change to every client, none to the server's authorization path.
