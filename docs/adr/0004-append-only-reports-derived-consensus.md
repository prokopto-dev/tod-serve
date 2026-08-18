# ADR-0004 — Store every report; derive the answer

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

## Context and problem statement

Two people will report the same kill at different times, and sometimes one of them will be wrong —
a mistyped hour, a machine with the wrong timezone, or a deliberate lie from someone feeding a rival
guild. The system has to have an answer for what happens then, and the answer determines the schema.

The incumbent solution in this space is a pinned Discord message or a spreadsheet cell. Both are
last-write-wins, and both fail the same way: a wrong ToD silently replaces a right one and nobody can
tell it happened.

## Considered options

| Option | For | Against |
|---|---|---|
| A — One mutable row per target, last write wins | Simplest possible schema, fastest to ship, trivially fast to read | No attribution, no history, no way to see a correction happened. One fat-fingered report destroys a good one silently — the exact failure of the tools this replaces |
| B — Append-only report log, current ToD derived | Nothing is ever lost. Disagreement is visible. A wrong report is correctable without erasing the record, and confidence can be computed from real evidence | Reads require a derivation or a cache that can drift. Consensus rules are a real algorithm with real edge cases, and "why does it say that" becomes a support question |

## Decision outcome

**Chosen: B.** The whole value proposition is being *more trustworthy* than a spreadsheet, and a
mutable row is a spreadsheet with extra steps.

`tod_report` is append-only, enforced by `BEFORE UPDATE OR DELETE … RAISE(ABORT)` **plus**
`TestAppendOnly_TriggersFire_AfterAllMigrations` — table rebuilds drop triggers, and that test is how
you find out. Corrections are new rows; a retraction is a row with `retracts_report_id` set.

The derivation lives in `internal/consensus` as a pure function — no store, no `time.Now`, no
`math/rand`, no floats — so it is replayable byte-identically and property-testable without a
database. It is pinned by a golden corpus, and the corpus is the authority when the prose and the
tests disagree.

`target_state_cache` is a droppable cache, never authority. A nightly job recomputes from the reports
and diffs; the recomputation wins and an alert fires.

Confidence is an ordered enum rather than a score, and `evidence` — counts and spread — is the actual
contract, so a client can compute its own view. A 0–1 float would be false precision read as a
probability we cannot compute.

Revoked members' reports still count and their reporter renders as revoked; their retractions apply
too. Anything else lets revocation silently rewrite history.

### Consequences

- Good, because a disputed ToD produces a visible dispute rather than a silent overwrite.
- Good, because "how sure are we" has a real answer backed by counts a client can inspect.
- Good, because the log is the audit trail, so there is no second system to keep honest.
- **Bad, because the derivation can change the answer with no new kill** — a backfilled corroboration
  shifts the median. This is correct and non-obvious, and needs `change_reason` on every event plus a
  UI that explains itself.
- **Bad, because the clustering threshold ε is a guess** with no data behind it, tunable per timer
  and revisited after real use.
- **Bad, because a cache that can drift is a class of bug** that a mutable row does not have, paid
  for with a nightly verify job.
- **Bad, because reports are never pruned**, so the table grows without bound. At a few hundred rows
  a week that is acceptable for a decade; it is still unbounded.

### Reversal cost

A rewrite of `internal/consensus` and `internal/projection`, plus a data migration that would have to
pick a winner for every contested target — which is to say, throwing away the thing the design exists
to preserve.
