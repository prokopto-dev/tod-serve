# ADR-0019 — A device grant is identity-scoped and exchanges for per-membership tokens

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** Courtney Caldwell

## Context and problem statement

[ADR-0018](0018-a-device-authorization-grant-against-this-instance.md) chose a device authorization
grant but left open what an approval yields. The requirement is one "sign in with Discord" in the
plugin, after which a circle the user gains access to **later** appears without authenticating
again. [ADR-0005](0005-pats-bound-to-memberships.md) binds a PAT to a membership — but read it
closely: immediate revocation comes from membership state being checked on **every request**, not
from the binding. What the binding buys is **blast radius**, a leaked token costing one circle
instead of all of them. That is real and narrower than "a cross-circle credential is forbidden", so
the token model is open rather than settled.

## Considered options

| Option | For | Against |
|---|---|---|
| a — One approval mints every current per-membership PAT | No new credential kind, ADR-0005 untouched, blast radius per-circle | A circle joined later is invisible until the user approves a device again — which is precisely the requirement, so it fails by construction |
| b — An identity-scoped **grant** that exchanges for short-lived per-membership tokens | New circles appear at the next exchange with no second login, and what reaches a route is still a membership-bound PAT, so blast radius is unchanged. It is the refresh-token shape every real device flow already has | A second credential kind and an exchange endpoint. The grant is identity-wide and long-lived, so it becomes the most valuable thing on a player's disk |
| c — One identity-scoped token, membership checked per request | Simplest client: one credential to store, one to revoke | Revocation still works, but a leak reaches every circle the identity is in. It surrenders the one thing the binding buys and edges toward the `admin:*` scope the catalogue deliberately lacks |

## Decision outcome

**Chosen: b.** The requirement is not "fewer credentials", it is "no second login when I join a
circle", and (a) fails that by construction. (c) meets it by surrendering blast radius, which is the
only thing ADR-0005's binding actually buys. (b) keeps both.

**The grant authorizes no request.** Only the PATs it mints do, each an ordinary ADR-0005 token
bound to one membership and checked every request — so the authorization path, tenancy and
revocation are untouched, and no `admin:*`-shaped scope appears anywhere.

`device_grant` is keyed on an **identity**, not a membership: `identity` is unique on
`(provider_id, subject)`, and `instance_grant.identity_id`
([ADR-0012](0012-instance-grants-are-a-capability-ledger.md)) is the precedent — what a grant
answers outlives any one circle. **A row there is a decision, not a state:** approval and revocation
are both rows, the table is append-only, and it is its own hash-chained audit record on
`audit.ChainHash` — which is what makes an approval auditable *before* any membership exists, when
`audit_log.circle_id` has nothing to hold. Exchange mints one token per current, non-revoked
membership, carrying ADR-0018's approved scope set and never widening it. `api_token.expires_at`
already exists, so short TTLs need no schema change, and expired rows are litter that joins
`internal/sweep` beside `device_authorization`. The grant is a `core.Secret`, hashed like a PAT,
never compared by value (`SECRET001`), revocable on its own and listed beside tokens.

**One Discord login cannot span instances, and that is by design rather than a gap.** An identity is
`(provider_id, subject)` where `provider_id` is *that instance's* `identity_provider` row, because
[ADR-0011](0011-operator-registered-discord-application.md) made each instance its own confidential
client precisely so a token minted by a shared app cannot be replayed at another. The shape is
therefore **one login per instance, and within an instance circles appear automatically**. One
instance is one login, which is most raiders; two guilds on two servers is two logins, and that is
the price of the replay protection, not an oversight. **There is also no instance registry** — the
plugin adds an instance by URL, an affordance somebody has to build. Nothing here discovers or lists
peer instances, and nothing should.

**Authenticating with no membership is supported, and it is new state.** "Instances I plan to get
access to" requires it: a user completes a device authorization on an instance where they hold
nothing, the exchange returns an **empty token set** rather than an error, and circles appear at a
later exchange once they redeem an invite in the browser. Today `/join` requires an invite and is
the only route that mints, so approval must be reachable from a session holding no membership. That
is a change, and it is written here rather than left to be discovered.

### Consequences

- Good, because it is one login per instance, and a circle joined later needs no second one.
- Good, because what reaches a route is still an ADR-0005 PAT: authz, tenancy and revocation are
  unchanged.
- **Bad, because the grant is identity-wide and long-lived.** Blast radius is per-circle on the
  tokens and whole-identity on the grant, so the thing worth stealing got more valuable, not less.
- **Bad, because two instances means two logins, forever,** and every user will read that as a bug
  before they read it as replay protection.
- **Bad, because anyone who can sign in with Discord may now hold a grant row on a public instance**
  before an officer approved anything. It reaches no route until an invite is redeemed; it is still
  an append-only row a stranger can create, and append-only means permanent.
- **Bad, because short-lived tokens mean re-exchanging mid-raid,** and a client that mishandles
  expiry fails at the worst possible moment.
- **Bad, because exchange writes rows:** three circles refreshed hourly is seventy-two `api_token`
  rows a day, which the sweep now has to keep up with.

### Reversal cost

A release and a migration dropping `device_grant`. Clients fall back to option (a) — approval
minting per-membership tokens directly — so it is a real change to every client and none to the
server's authorization path.
