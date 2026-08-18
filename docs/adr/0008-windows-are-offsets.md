# ADR-0008 — Store a window as two offsets, and publish no probability curve

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

## Context and problem statement

A P99 raid target respawns inside a randomised window. The community quotes those numbers two
different ways for the same target — "7 days ±12h" and "16 to 24 hours" — and officers entering data
will type whichever form they read.

We have to pick one storage shape, and we have to decide how much certainty to imply about where in
the window a spawn actually falls.

## Considered options

| Option | For | Against |
|---|---|---|
| A — `(base_respawn, variance)` | Matches how half the community writes it, and how a wiki table reads | Cannot express an asymmetric window without inventing a sign convention, and two officers will read that convention differently. Requires a conversion step before every response |
| B — `(window_open_offset, window_close_offset)` from the ToD | Exactly what the API returns, so no conversion can be got wrong. Asymmetric windows are expressible with no extra concept | Reads less naturally against a wiki page; the importer has to do the conversion once |
| C — B, plus a probability density over the band | Would let a client say "most likely around here" | We have no spawn-time distribution data. Community lore about spawns clustering early is unverified, and a rendered curve is read as fact |

## Decision outcome

**Chosen: B**, with C explicitly rejected.

`window_kind ∈ fixed | variance | unknown`, and the CHECK constraints tie the offsets to the kind:
`unknown` iff both offsets are NULL, `fixed` iff they are equal, and close is never before open.
`unknown` is a first-class value, not a NULL to be interpreted — an unseeded instance reports
`status: no_timer` and still records ToDs correctly, which is a degraded state and an honest one.

`fixed_grace_seconds` (default 900) exists because a fixed timer otherwise makes `in_window` an
instant: a fixed target would flip `pre_window → overdue` with no state between, and the UI could
never say "spawning now". The grace makes a fixed target renderable by the same component as a
variance target.

Rejecting C is the substantive half of this decision. The output is the band and where "now" sits in
it — `progress_bp`, integer basis points — and nothing that implies we know more than that. The
failure mode this project is built against is a confident mistake.

### Consequences

- Good, because no conversion sits between storage and the wire, so there is no place for a sign
  convention to be misread.
- Good, because asymmetric and fixed windows need no special case.
- Good, because `unknown` being explicit means "we do not know" renders differently from "nothing has
  died", which is the distinction an officer actually needs.
- **Bad, because importing community data requires a conversion step**, and getting *that* wrong is
  now the single place window data can be corrupted.
- **Bad, because `fixed_grace_seconds` is an invented number** with no basis in game mechanics; it
  exists to make a UI state reachable, and it will read as authoritative to someone.
- **Bad, because refusing a probability curve means the product looks less clever** than a competitor
  willing to draw one from lore.

### Reversal cost

A migration and a release. Adding a curve later is additive; changing the storage shape is not.
