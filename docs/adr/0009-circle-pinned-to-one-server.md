# ADR-0009 — A circle is pinned to one server, permanently

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

## Context and problem statement

Project 1999 runs three servers — Blue, Green and Red. A time-of-death on Blue says nothing whatever
about Green: different worlds, different spawn clocks, different guilds racing them.

The sibling plugin nParse+ Merchant Mode reached the same conclusion about items and states it as its
governing rule: *"Items cannot move between P99 servers"*, so every structure that touches a trade is
keyed by server and there is no combined view of it anywhere.

The question is whether `server` is a column on the report or a property of the tenant.

## Considered options

| Option | For | Against |
|---|---|---|
| A — `server` on every report; a circle spans servers | A guild raiding Blue and Green has one circle, one member list, one invite set | Every query that forgets `AND server = ?` produces a confident, wrong answer. Permits a combined view, and the moment one exists someone builds an "all my ToDs" screen |
| B — `circle.server` NOT NULL and immutable | There is no row in the schema where a Blue fact and a Green fact can meet, so the rule is structural rather than a `WHERE` clause someone forgets | A guild on two servers manages two circles: two member lists, two invite sets, two sets of officers to keep in sync |

## Decision outcome

**Chosen: B**, enforced by a trigger on update.

Option A's cost is the same bug class [ADR-0002](0002-circle-is-the-tenant.md) spends three gates
buying back for `circle_id`, and here we can simply not have it. Where the tenancy decision had no
alternative — circles genuinely must coexist in one instance — this one does.

The extra administration composes better than it first appears. The plugin already holds several
`(endpoint, token, circle)` destinations and ticks which ones a kill reports to, so "report to my
Blue circle and my Green circle" is two ticked boxes rather than a feature anyone builds.

**A report still carries `server` in its body**, and a mismatch is `422 server_mismatch`. This is not
redundancy. It is the guard against the actual failure mode of a fan-out client: the user is playing
Blue and has the Green destination ticked. Without the echo, wrong data lands silently and looks
right.

Raid target *identity* stays server-agnostic — `raid_target` has no `server` column, because a mob's
existence is a fact about the game. Only `raid_target_timer`, keyed `(target_id, server)`, is a fact
about a server. That is merchant-mode's split between an item's id and its price, applied to mobs.

### Consequences

- Good, because a cross-server leak is not expressible, rather than being a test away.
- Good, because the body echo catches the mis-ticked destination, which is the mistake users will
  actually make.
- Good, because the catalogue is shared across every circle on the instance while timers are not.
- **Bad, because a guild raiding two servers administers two circles**, and their member lists will
  drift apart the first time someone is added to one and not the other.
- **Bad, because immutability means a circle created on the wrong server is unfixable** — it must be
  recreated, and its report history does not come with it.
- **Bad, because there can be no combined board**, even where a user genuinely wants one; the client
  has to render two.

### Reversal cost

A migration adding `server` to reports and relaxing the trigger — mechanically easy, and it
reintroduces the bug class permanently.
