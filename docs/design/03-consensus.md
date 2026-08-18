# Consensus and windows

**Status:** normative. **Tie-breaker:** [00-canonical-conventions.md](00-canonical-conventions.md).

The current ToD for a target is **derived from the reports, never written by whoever spoke last.**
This document is the specification of that derivation. It is pinned by a golden corpus
(`test/golden/consensus/*.json`), and the corpus is the authority when this prose and the tests
disagree.

## 0. Shape

```
consensus.Derive(reports []Report, quakes []Quake, timer Timer, now Micros, cfg CircleConfig) → State
```

A **pure function**. No store, no `time.Now`, no `math/rand`, no floats — the same purity law
`internal/strategy` carries in Dragon Kill Party, for the same reason: it must be replayable
byte-identically and property-testable without a database. The float ban is
[canonical §3](00-canonical-conventions.md#3-no-floats-anywhere-that-computes-a-window).

**Enforced by:** `PURE001`, `PURE002`, `CLOCK001`, `NOFLOAT001`.

Several questions below this document leaves open — what makes a spread *wide*, what *corroborating*
means, which `contest_reason` wins when two apply — are answered in the corpus rather than here, and
[`test/golden/consensus/README.md`](../../test/golden/consensus/README.md) lists each one with the
fixture that pins it. Each is resolved toward the reading that fails safe. That file also records
the two fields the derivation returns that §7's example does not show, and why.

## 1. Retraction folding

Drop any `kill` report R for which a `retraction` row exists with `retracts_report_id = R.id`.

A retraction of a retraction is **not supported** — post a fresh kill report instead;
`409 already_retracted`. A retraction is valid only from the original reporter (`tod.retract`) or
from a principal holding `tod.retract.any`.

**A revoked member's retractions apply, exactly as their reports count.** The revocation rule cuts
both ways; anything else means revocation silently rewrites history.

## 2. Quake truncation

Let `Q` be the latest `quake_event.occurred_at` in the circle. For a target with
`is_quake_target = 1`, every report with `died_at < Q` moves to history and cannot form the current
cluster. Reports after `Q` behave normally.

If no post-quake kill exists, the target's status is `up` with `up_since = Q`.

## 3. Clustering into kill events

Two reports describe the same kill iff their `died_at` values are close. The separation between two
*real* kills of the same target is at minimum the window-open offset — days, for every P99 raid
target — so the threshold can be generous.

```
ε = timer.cluster_epsilon_seconds
    ?? clamp(window_open_offset_seconds / 4, 5 min, 30 min)
    ?? 30 min                                     -- window_kind = 'unknown'
```

30 minutes covers the ordinary case of someone typing a ToD in after the raid. The `/4` term exists so
a short-timer target added later behaves correctly rather than merging two genuine kills; the `min`
cap means it never binds on a real raid dragon.

**Algorithm.** Sort by `died_at`. Single-linkage sweep, extending the cluster while the next report is
within ε of the *previous* report, **capped at a total cluster span of 2ε**.

The span cap is not decoration. Without it, five reports 29 minutes apart chain into a single
two-hour "kill". Deterministic, O(n log n), and the chaining hazard has its own golden fixture.

## 4. Selecting the current cluster

**The current ToD is the cluster with the latest `died_at`, not the most recently reported one.**

A report arriving now about a kill three days ago does not supersede a kill that happened yesterday.
This is the answer to "what about a report older than the current one": it forms or joins an earlier
cluster, is fully retained, is visible in history, and does not move the answer.

Two refinements:

- If `cfg.min_reporters_to_supersede > 1` and the latest cluster has fewer distinct reporters than
  that, and an earlier cluster whose window is still live has more, the earlier cluster stays current
  and the later one surfaces as a *pending* alternative (`contest_reason: pending_supersede`).

  **Default is 1**, because the honest single reporter is the common case and disabling them would
  make the product useless. A circle that has been burned can raise it.

- Otherwise the latest cluster wins — **and if it is thinner than the corroborated cluster it
  displaces, `contested` is set** and the displaced cluster appears in `alternatives[]` with
  `contest_reason: thin_supersede`.

## 5. The point estimate within a cluster

**Median. And if any report in the cluster has `source = 'log_line'`, the median of the log-line
reports only.**

A log line carries a machine timestamp read out of the game's own file; a hand-typed time is a
memory. Manual reports in a cluster that contains log lines are *corroboration* — they raise
confidence — but they are not estimators.

Median rather than mean: one person typing `21:04` instead of `22:04` must not drag the answer by
thirty minutes.

On an even count, take the **earlier** of the two middle values. An early window costs a wasted trip;
a late window costs a missed spawn. This bias is a judgement call, it is recorded as such, and
[§8](#8-known-weaknesses) flags it for a raid leader to confirm.

## 6. The window

Given the derived `died_at` `d` and the resolved timer (circle override → catalogue → `unknown`):

| `window_kind` | `open_at` | `close_at` | `spawn_at` |
|---|---|---|---|
| `variance` | `d + open_off` | `d + close_off` | `null` |
| `fixed` | `d + open_off` | `d + open_off + fixed_grace` | `d + open_off` |
| `unknown` | `null` | `null` | `null` |

`spawn_at` is present **iff** the timer is fixed, so a client can branch on its presence without
inspecting `window_kind`.

`fixed_grace_seconds` (default 900) exists because a fixed timer otherwise makes `in_window` an
instant: the target would flip `pre_window → overdue` with no state in between and the UI could never
say "spawning now". The grace is what makes a fixed target renderable in the same component as a
variance target.

Where "now" sits, as integers only:

```json
"window": {
  "kind": "variance",
  "open_at":  "2026-08-20T14:22:31.000000Z",
  "close_at": "2026-08-21T02:22:31.000000Z",
  "spawn_at": null,
  "progress_bp": 3742,
  "seconds_until_open": -16214,
  "seconds_until_close": 26986
}
```

`progress_bp` is basis points, integer division, clamped to `[0, 10000]`; `null` for `fixed` and
`unknown`. Both `seconds_until_*` are signed — negative means passed.

**Every response carries `as_of`, and every countdown is relative to it.** See
[canonical §1](00-canonical-conventions.md#1-time).

*Rejected: a probability curve over the variance band.* We have no spawn-time distribution data,
community lore about "spawns cluster early" is unverified, and a rendered curve would be read as
fact. The honest output is the band plus where now sits in it.

## 7. What the client sees

### Status

```
status: unknown | no_timer | pre_window | in_window | overdue | up
```

- `unknown` — no ToD at all.
- `no_timer` — we have a ToD but `window_kind = 'unknown'`. **Distinct from `unknown` on purpose:**
  the client can still render "died 4 hours ago" and must not render a window.
- `pre_window` — dead, timer running.
- `in_window` — `open_at ≤ now ≤ close_at`.
- `overdue` — past `close_at`. Means the ToD is wrong, the timer is wrong, or someone killed it
  quietly. **Real, actionable intel; not an error state.**
- `up` — post-quake, or (1.1) a sighting.

### Confidence and evidence

Confidence is an ordered enum, never a score. A 0–1 float would be false precision, would be a float
in a package that bans them, and would be read as a probability we cannot compute.

| Value | Condition |
|---|---|
| `unknown` | no usable report |
| `low` | one distinct reporter, source `manual` or `api` |
| `medium` | one distinct reporter with source `log_line`; **or** ≥2 distinct reporters with spread > 5 min |
| `high` | ≥2 distinct reporters and spread ≤ 5 min; **or** ≥1 `log_line` reporter plus ≥1 corroborating reporter |

`confidence` is a convenience. **`evidence` is the contract**, and clients may compute their own view
from it:

```json
"evidence": {
  "report_count": 4,
  "distinct_reporter_count": 3,
  "log_line_count": 2,
  "spread_seconds": 190,
  "revoked_reporter_count": 1,
  "report_ids": ["01K...", "01K...", "01K...", "01K..."]
}
```

### Disagreement

Surfaced, never resolved silently:

```json
"contested": true,
"contest_reason": "thin_supersede",
"alternatives": [
  { "died_at": "2026-08-16T03:11:00.000000Z",
    "report_count": 1, "distinct_reporter_count": 1, "confidence": "low",
    "window": { "open_at": "...", "close_at": "..." },
    "report_ids": ["01K..."] }
]
```

`contest_reason ∈ { thin_supersede, implausible_ordering, wide_spread, pending_supersede }`. Only
clusters whose window has not yet closed appear as alternatives, capped at 3, newest first.
Everything else is history and is one `listTodReports` call away.

*Rejected: last-write-wins.* *Also rejected: averaging conflicting clusters* — the mean of two
different kills is a time at which nothing happened.

## 8. Edge cases, answered

| Case | Answer |
|---|---|
| Two reports minutes apart — one kill or two? | One, by ε. For every real P99 raid target the answer is always "one", because ε caps at 30 min and the shortest raid window is hours. The rule is expressed in terms of the timer so a future short-timer target behaves correctly. |
| A report older than the current one | Joins or creates an earlier cluster. Retained, visible, not current. If it lands *inside* the current cluster's ε it joins it and may shift the median — legitimate; that is more evidence. |
| A report for a target already in window | If `died_at` is later than the current cluster and outside ε, a second kill happened. Legitimate and common — it popped and someone killed it before you noticed. New cluster becomes current. |
| `died_at` in the future | `422 died_at_in_future`, tolerance +120 s for clock skew. The only hard rejection on a time, because it is impossible independent of any derivation. |
| `died_at` more than 90 days old | `422 died_at_too_old`. Almost always a timezone bug, never useful. |
| `died_at` before the current cluster's `open_at` — physically impossible | **Accepted, not rejected.** Flagged `implausible: true` on the report and raised as `contest_reason: implausible_ordering`. The current cluster might be the wrong one, and **derived state must never veto an observation.** |
| All reports for the current cluster retracted | The previous cluster becomes current, `change_reason: retraction`. |
| The answer changes with no new kill | Correct and expected — a backfilled corroboration shifts the median. It must be **visible**: every state change emits SSE `tod.changed` with `change_reason ∈ { new_kill, corroboration, retraction, quake, timer_change }`. |

## 9. Known weaknesses

Recorded here rather than discovered later.

**Clock skew is the most likely source of real-world bad data.** EQ log lines are
`[Mon Aug 18 02:14:07 2026]` — the client's local clock, **with no timezone**. The plugin converts
using the machine's zone. One raider with a wrong TZ poisons the board by hours, and does it
*confidently*, because their reports are `source: log_line` and therefore outrank everyone's manual
entries under [§5](#5-the-point-estimate-within-a-cluster).

Designed in: `client_clock_offset_seconds` from the plugin, and a server-side `clock_skew_seconds`
flag when `reported_at − died_at` is implausible for a log-line report submitted in real time. **That
is not sufficient.** A systematic per-reporter skew estimate — the median of `reported_at − died_at`
over that reporter's live log-line reports, applied as a correction — is worth designing before 1.0
and is on the roadmap.

**ε has no data behind it.** `clamp(window_open/4, 5 min, 30 min)` is a guess. It is a per-timer
override column so it is tunable without a release, and every merge decision is logged so a corpus
builds itself. Revisit after a month of real reports.

**The early-bias tiebreak** in §5 assumes a missed spawn costs more than a wasted trip. For a
Sleeper's Tomb or NToV run the trade may invert. Confirm with an actual raid leader.

**The lone-troll supersede.** One member with `tod.report` can move the board by claiming a kill ten
seconds ago. The design surfaces `contested` but still shows the new ToD.
`min_reporters_to_supersede` is the lever, and its default of 1 is a deliberate choice in favour of
the honest single reporter.
