# ADR-0014 — A second, deferred pool for multi-read renders

**Status:** accepted · **Date:** 2026-08-25 · **Deciders:** Courtney Caldwell

## Context and problem statement

`projection.Service.Board` read the effective timer and the derived cache as two pooled statements —
two implicit transactions. A timer write committing between them handed that render the old window
offsets beside a `died_at` derived under the new ε: the timer carries the clustering threshold, so
the halves describe different derivations, not merely different instants. The board then showed a
window, a confidence and an evidence count with nothing saying they disagreed — a confident mistake,
the failure this project is built against. It was
[issue #17](https://github.com/prokopto-dev/tod-serve/issues/17), and predates
[ADR-0013](0013-the-timer-invalidation-joins-the-writing-transaction.md), which narrowed the class
rather than widening it.

`internal/store` had one transaction primitive, `InTx`, over one `*sql.DB` opened
`_txlock=immediate` so a read-then-write transaction cannot hit `SQLITE_BUSY_SNAPSHOT`. That pragma
takes the **write** lock at `BEGIN`; its comment names the cost. Missing was a read snapshot costing
no lock.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Wrap the render in `InTx` | No new machinery; isolation is what a transaction is | `_txlock=immediate` takes the write lock at BEGIN, so the product's hottest read path would serialise the instance behind its slowest reader. A worse failure than the one it fixes |
| B — Re-read the timers after the cache, retry on a difference | A few lines, no new pool | Narrows the window; does not close it. Would read as a guarantee in review and in the invariants table while staying a race — the half-measure this repository is built against |
| C — One handle, `BeginTx` with `sql.TxOptions{ReadOnly: true}` | No second pool. Today's driver turns it into a plain deferred BEGIN | The lock mode becomes a driver behaviour depended on silently, and SQLite still enforces nothing: a write reached through such a transaction tries to upgrade it — the `SQLITE_BUSY_SNAPSHOT` deadlock `_txlock=immediate` exists to prevent, by the back door |
| **D — A second `*sql.DB` over the same file, `_txlock=deferred` and `query_only`** | Under WAL a deferred read transaction pins a snapshot at its first read and blocks no writer. `query_only` makes "this pool cannot write" SQLite's rule rather than review's, which is what makes a gate on it possible | A second pool to one file, a second `Close` to get wrong, and a primitive a caller can reach for when they meant `InTx` |
| E — Leave it | Self-limiting: the console revalidates every fifteen seconds | Wrong for that render with nothing saying so, and the same defect makes the nightly job report drift that is not there |

## Decision outcome

**Chosen: D.** `store.DB` holds two pools, and `DB.InReadSnapshot` is the primitive for a read
that spans tables and pairs the answers. `Board` takes one; so does `projection.Service.Verify`,
whose diff of a cached row against a recomputation holds the same pair. **Every derivation input
goes inside**, target rows included — `is_quake_target` reaches `consensus.Timer` — so
`catalogue.List`/`Get` grew `ListIn`/`GetIn` siblings taking a query set. The poolless spellings
stay, because `internal/api` is the one layer that never names one (ADR-0013).

**What it costs.** A render holds a read transaction open for the length of its reads, which holds
the WAL from checkpointing for that long — so the contract is read what you need and get out.
Neither pool caps its connections, as before: this workload contends on the single write lock, not
handle count, and the inherited `busy_timeout` should never fire, a deferred reader under WAL
waiting for nothing. `:memory:` cannot have a second pool at all, being per-connection: `Open` gives
such a store none, and `InReadSnapshot` returns `ErrNoSnapshot` rather than falling back to the pool
it exists to replace.

**What it does not close.** A snapshot is reads: everything here writes what it derived *after* the
snapshot ends, so a timer committing in that gap leaves a row derived under a window that has moved.
Unchanged from before, and recorded as `DerivedWriteFollowsItsSnapshot`. Nor does it touch a row simply WRONG on
disk — issue #21.

**Mechanisms.** `TestInReadSnapshot_AWriteCommittedWhileItIsOpen_IsNotVisibleInsideIt` is the
isolation; `TestInReadSnapshot_HoldsNoWriteLock_SoAConcurrentWriteCommits` the concurrency this was
chosen for; `TestInReadSnapshot_AWriteThroughIt_IsRefused` the `query_only`. Above them,
`TestBoard_ATimerCommittingMidRender_NeverPairsAWindowWithADiedAtFromAnotherTimer` rewrites the
timer continuously while it renders, and
`TestGetTargetState_AQuakeFlagFlippingMidRead_NeverRendersItAgainstTheOtherDerivation` flips
`is_quake_target` the same way; both require every answer to be one of two computed independently by
`internal/consensus`. Each was watched failing against the read it replaces.
**`Verify` has no such gate**, and `VerifyDiffsOneSnapshot` records that rather than implying one:
its interleaving could only be produced by tuning a circle's size until the sweep's gap lined up with
the write — two to seven times in forty sweeps here, perhaps never on other hardware.

### Consequences

- Good, because the pairing is closed rather than narrowed: no interleaving shows a render two
  halves of different derivations.
- Good, because the verify job stops calling a moving timer drift — an ERROR on a healthy instance
  is how an alert gets turned off.
- **Bad, because `internal/store` now has two primitives that both look like "run this against the
  database",** and reaching for the wrong one costs a needless write lock or a refused write.
- **Bad, because a store opened at `MemoryPath` can no longer render a board.** It has no snapshot
  pool; nothing uses it, which is how that would go unnoticed.
- **Bad, because `Verify` and `Rebuild` are ungated.** `Rebuild` reads the log rather than the
  cache, so its snapshot buys uniformity rather than a fixed defect. `Verify` holds the pair and
  its fix is real — there it is the gate that could not be built, not a defect that was not there.

### Reversal cost

An hour. `InReadSnapshot` becomes a call on the writing pool's query set, the second `sql.Open` and
its half of `Close` come out, and the call sites keep their shape. The two gates above then fail,
which is the correct outcome: they describe the property, not the mechanism.
