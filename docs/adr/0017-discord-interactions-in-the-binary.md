# ADR-0017 — Discord interactions in the binary, disambiguated by a channel binding

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** Courtney Caldwell

## Context and problem statement

A circle's members want to report and read ToDs from Discord.
[ADR-0011](0011-operator-registered-discord-application.md) already registers one operator-owned
application per instance, so a bot is a **second credential on that same application**, not a second
registration. What forces a decision is that `circle_provider` carries no unique index on
`discord_guild_id` on purpose — `db/schema.hcl` says *"A guild raiding Blue and Green makes two
circles"* — so a guild names **N circles** and a command arriving from one has no answer to "which
circle is this?". Whatever answers it also decides who may read the reply, and Discord has **no
channel-membership API**, so that half cannot be computed at all. Left open, a two-circle guild gets
a bot that refuses every command, or one that guesses into a channel nobody can enumerate.

## Considered options

| Option | For | Against |
|---|---|---|
| A — A gateway bot | Real **events**: Discord pushes `GUILD_MEMBER_UPDATE`, so a role removal could revoke a PAT the moment it happens | A persistent outbound WebSocket. Law 6 confines outbound HTTP to `internal/identity` through one guarded client, so this needs a `NET001` exception plus reconnect state in a `FROM scratch`, read-only container |
| B — A separate interactions service | Its failures cannot take the API down with them | It needs **its own copy of the circle↔guild map and its own tenancy logic** — a second answer to "which circle is this?" |
| C — A unique index on `circle_provider.discord_guild_id`, resolving guild → circle | No new table; the resolve reads the row the guild gate already uses | It refuses the second circle at configuration time to make a later lookup convenient |
| D — HTTP interactions in this binary, resolving **channel** → circle | Inbound, so it is a route, and a channel is the narrower thing an officer actually points at | A guild with two circles cannot use the bot until somebody binds a channel |

## Decision outcome

**Chosen: D.** Interactions are inbound — Discord POSTs and we verify an Ed25519 signature over the
body — so the endpoint is a **route**. Law 1's registry gives it a permission, scopes and a tenancy
flag, and law 5's `TestTenancy_CrossCircle_EveryOperationDenies` is derived from that registry, so
it covers the new route without anybody remembering to add it anywhere.

**C is the tempting one and it is wrong.** `db/schema.hcl:1101` states guild-to-many-circles as
intent, not as an oversight; the index would break the case the schema exists to support.

**B is refused on this repository's own history.** A load-bearing list kept in two places drifts
here: [ADR-0002](0002-circle-is-the-tenant.md)'s copy of the instance-scoped allowlist sat three
tables short of [canonical §9](../design/00-canonical-conventions.md#9-tenancy--this-project-diverges-from-dkp)'s
until #31 found it, so it read as though `instance_grant` needed one. The append-only list is the
same class caught earlier: `LOG001` parses
[01-domain-model](../design/01-domain-model.md), and `AGENTS.md` says of its own copy that *a
reviewer, not a gate, keeps it honest*. A second service is that failure with a network between the
copies, and no gate to write: they would not be in one repository.

**The new table is `circle_discord_channel`**, and it answers both halves at once: a channel
disambiguates, and binding one **is** the explicit, stored, per-channel opt-in a visible message
needs.

- **One channel, one circle.** The primary key is `discord_channel_id` alone. Two circles on one
  channel restores the ambiguity the table exists to remove and leaves a visible answer with no
  single circle it could have come from.
- **Circle-scoped, not on the allowlist.** `circle_id NOT NULL REFERENCES circle(id)`, law 4. The
  resolve reads channel → circle, so its `WHERE` cannot name `circle_id` and it takes a counted
  `-- tenancy:` waiver. That is the cheaper cost: [#29](https://github.com/prokopto-dev/tod-serve/pull/29)
  proved that moving a table onto the instance-scoped allowlist is the single edit that makes
  `TEN001` and `TestInstanceScopedAllowlist_MatchesTheAppliedSchema` both quieter with nothing red.
- **A tombstoned circle keeps its bindings.** Nothing deletes them, exactly as nothing deletes
  `circle_provider`'s rows, so the resolve joins `circle.deleted_at IS NULL` the way the guild-gate
  query already does — and re-binding the channel replaces the dead row.
- **`allow_visible` defaults to `0` in the DDL**, and `created_by_membership_id` is a composite key
  into `membership (circle_id, id)`, which makes "an officer of circle A bound a channel to circle
  B" unrepresentable rather than merely refused.

The rules the route is held to are
[04-identity §9](../design/04-identity-and-revocation.md#9-discord-interactions-what-is-disclosed-and-where);
the operator's side is [discord-bot.md](../operations/discord-bot.md).

### Consequences

- Good, because there is one circle↔guild map, one tenancy decision and one deployment artefact.
- Good, because being a route buys the permission, the scopes, the tenancy flag and law 5's
  isolation test rather than reimplementing any of them.
- Good, because "what does this channel disclose" has an answer an officer can read back.
- **Bad, because interactions carry commands and not events, so removing somebody's Discord role
  still does not revoke a PAT they already hold.** ADR-0011 accepted that gap and this decision
  **keeps** it; A is the only option that closes it. `revokeMember` remains the mechanism that
  works, and it is written down here so nobody rediscovers it.
- **Bad, because a guild with two circles cannot use the bot until a channel is bound.** The first
  command in a fresh guild fails, and the remedy is elsewhere.
- **Bad, because a bot token is a second high-value secret at rest**, beside the `client_secret`
  ADR-0011 accepted, on an application whose compromise is now worth more.
- **Bad, because a binding is a disclosure decision stored where the people it affects cannot see
  it.** Members read the channel; they do not read `circle_discord_channel`.

### Reversal cost

A release. The table is additive and unreferenced, so undoing this is dropping a route, a table and
a bot token — but every circle that told its members to use slash commands has to be told to stop.
