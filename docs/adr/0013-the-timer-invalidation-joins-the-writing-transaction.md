# ADR-0013 — The timer invalidation joins the writing transaction

**Status:** accepted · **Date:** 2026-08-25 · **Deciders:** Courtney Caldwell

## Context and problem statement

A write that moves a respawn window has to tell the projection, because `timer_change` is the one
`target_state.change_reason` the append-only report log cannot show — a window moved and nothing was
reported. That push happened **after** the write committed, so a process that died in between never
told it and the board served the old window until the nightly verify job, up to twenty-four hours
later. Failing the request closed the case a caller can see; a crash produces no response for
anybody to retry from. It was recorded as `TimerPushIsNotTransactional`.

The mechanism to close it is the one `audit.Append` already uses — take the writing transaction's
query set — and the blocker was ownership: `internal/catalogue`'s write methods own their own
transactions, and the port lived in `internal/api`.

## Considered options

| Option | For | Against |
|---|---|---|
| A — The handler owns the transaction and passes it through both packages | Puts the transaction where the request is, which is where the retry and the response already are | A restructure of every catalogue write, not just the timer ones, and `internal/api` would hold a `*sqlitegen.Queries` — a layer that has never named a query set |
| B — `internal/catalogue` imports `internal/projection` | No port at all; the call is direct | Does not compile. `projection` already depends on `catalogue` for the effective timer, and inverting that is the dependency `api.TimerInvalidator` existed to avoid |
| **C — The port moves DOWN to `internal/catalogue`, takes the transaction's query set, and is passed per call** | The port is declared where the transaction is owned. `projection` satisfies it structurally and imports nothing new; `catalogue` still knows nothing of the projection | Every window-moving write grows a parameter, and `internal/api` now holds a port it forwards rather than calls |
| D — Leave it, and keep the nightly verify job as the backstop | No change; the bound is one night | A confidently wrong board for up to twenty-four hours is this project's named failure mode, and "bounded" is not "closed" |

## Decision outcome

**Chosen: C.** A port whose parameter is a transaction's query set belongs to whoever owns that
transaction, and that is `internal/catalogue`. `internal/api` keeps supplying the implementation —
it is still the layer that knows a ROUTE moved a window — and hands it to the write instead of
calling it afterwards. `tod-serve seed timers` hands the same port to `ApplySeed`. The port is
passed per call rather than held on the `Service`, because the projection is built ON the catalogue
and a constructor field would need one of the two built half-wired.

**The reads matter as much as the write.** `projection.recompute` calls
`catalogue.Service.ResolveTimer`, so that read takes the same query set: a resolve on a pooled
connection reads the snapshot from before the transaction opened and would recompute every board
from the window that was there *before*. `recompute`, `storeOrDrop`, `store`, `revokedReporters`,
`latestQuake` and `circle` all take it; callers that are not in a transaction pass `s.db.Queries()`
and say so at the call.

**The unit of atomicity for a seed is one window, not the file.** SQLite has a single writer and a
catalogue timer fans out over every circle on that server, so a sixty-window seed in one
transaction would block every report on the instance for its duration. A crash mid-seed leaves the
windows already written with their boards recomputed and the rest untouched; the remedy is to run
the same file again, and the report says how far it got.

**Mechanisms.** `TestRouteRegistry_EveryTimerWritingRoute_RollsBackWhenTheInvalidationFails` drives
every flagged route with a failing invalidator and compares the database before and after.
`TestTimerWrite_TheInvalidation_RunsBeforeTheWriteIsVisible` covers the crash window itself: at the
moment the push runs, the change is visible on the transaction's query set and not on a pooled one.
`TestTimerWrite_ANilInvalidator_IsRefused` and
`TestApplySeed_AFailurePartWay_KeepsTheWindowsItWroteAndNoOthers` cover the rest.

### Consequences

- Good, because the crash window is gone rather than bounded: there is no state in which a window
  moved and the boards derived from it were not recomputed.
- Good, because `deleteCircleTimerOverride` loses its compensating push on the 404. That existed
  only because a committed DELETE left a failed push nothing to retry from; a rolled-back one does.
- **Bad, because the fan-out now runs under the write lock.** `putRaidTargetTimer` recomputes every
  circle on a server inside its request's transaction, and every other writer waits.
- **Bad, because `seed timers` is no longer all-or-nothing.** A run can stop part-way, and an
  operator has to re-run the file rather than reason about one atomic outcome.
- **Bad, because six projection helpers grew a `*sqlitegen.Queries` parameter,** and a caller that
  passes the pool where it meant the transaction has written a bug no compiler will catch.

### Reversal cost

A day. The port moves back to `internal/api`, the pushes return to the handlers, and the
`*sqlitegen.Queries` parameters come off six projection helpers and two catalogue reads — mechanical
in every case. `TimerPushIsNotTransactional` goes back on the invariants page as a known gap.
