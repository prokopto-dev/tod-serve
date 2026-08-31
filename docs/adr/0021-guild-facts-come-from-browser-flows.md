# ADR-0021 — Guild facts for a device grant come from browser flows, not a stored Discord credential

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** Courtney Caldwell

## Context and problem statement

[ADR-0020](0020-a-device-grant-is-identity-scoped.md)'s exchange re-proves a circle's guild gate
before minting, which needs `GuildFacts`. A `credential_ticket` carries them — but only for the
guilds `guildsToAsk` selects: with an invite, that invite's circle; without one, "the circles THIS
IDENTITY already has a membership in" (`internal/identity/flow.go`). So a memberless approval
captures nothing, and a circle joined **later** appears in no captured set. Without another source,
ADR-0020's "circles appear automatically" is false precisely where the circle gates — which is the
deployment [ADR-0011](0011-operator-registered-discord-application.md) exists for.

## Considered options

| Option | For | Against |
|---|---|---|
| a — Store a Discord refresh token; re-fetch at exchange | Facts are always live, so the requirement holds unconditionally | The flow discards the access token deliberately ([04-identity §7](../design/04-identity-and-revocation.md)). The instance would hold a long-lived provider credential, put an outbound call on the mint path, and fail when Discord does |
| b — Every browser flow refreshes the identity's facts; exchange reads them under an age bound | No stored provider credential and no new Discord call — joining a gated circle **is** a browser flow, and `guildsToAsk` already asks that guild. Every stored fact was verified under 120 seconds before it was written | The exchange evaluates a **cached copy**, which `EvaluateGuildGate` explicitly warns against; a user who never returns to a browser stops refreshing that circle once the facts age out |
| c — Gated circles are outside a grant entirely | Nothing stored and nothing cached | Gated circles are the motivating deployment, so this narrows the feature to the case nobody asked for |

## Decision outcome

**Chosen: b.** The decisive observation is that **joining a gated circle is already a browser
flow**: redeeming that invite runs the OAuth callback, and `guildsToAsk` asks for exactly that
circle's guild because the invite names it. The fact this design needs therefore arrives as a side
effect of the join the user performs anyway, with no extra trip and no extra Discord call.

**The facts belong to the identity, not to the grant.** One row per `(identity, guild)` carrying the
member flag, the roles and a `verified_at`, written by every path that verifies one — `/join`,
`/sessions` and device approval. A grant holds no facts of its own, so two devices approved months
apart read the same current answer.

**`guildsToAsk` is unchanged.** Nothing here enumerates guilds, so its guarantee survives intact:
the set "comes from a secret or from a verified identity, never from a caller-supplied id, so there
is nothing here to enumerate". Asking Discord which of an instance's gated guilds a stranger belongs
to is exactly the enumeration that sentence forbids.

**Absence is not a pass.** `EvaluateGuildGate` already returns `guild_role_required` where it holds
no fact rather than succeeding — "reading an absent role list as an empty one would disable the gate
for every user while appearing to enforce it" — and a missing row here is that same absent fact.
Exchange reads a row only past ADR-0020's live gate re-read and only while `verified_at` is within
`GateFactsTTL`.

### Consequences

- Good, because a gated circle joined later appears at the next exchange: the join that granted it
  refreshed its facts on the way through.
- Good, because no Discord credential is held at rest beyond ADR-0011's 120-second ticket, and the
  mint path makes no outbound call — so `NET001`'s boundary does not move.
- **Bad, because this is the cached copy `EvaluateGuildGate`'s comment warns against**, admitted on
  one path. `/join` and `/sessions` still hold a live ticket and still use it; the exchange has none
  by construction, and that is the whole difference.
- **Bad, because a plugin-only user who never opens a browser stops refreshing gated circles** once
  the facts age out. It will read as "my raid circle disappeared", so the skip must say which circle
  and why.
- **Bad, because it is a new table written on three existing paths,** none of which may fail the
  flow it rides on — a facts write that breaks `/join` is a worse bug than a stale fact.
- **Bad, because two facts now age differently:** membership revocation is live on every request,
  guild membership is up to `GateFactsTTL` old. Anyone reasoning about "is this person still in"
  has to hold both.

### Reversal cost

Drop the table and fall back to (c): a gated circle needs an approval of its own. The grant, the
exchange and the live gate re-read are untouched, so it is a narrowing rather than a rewrite.
