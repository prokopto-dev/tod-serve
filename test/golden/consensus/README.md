# The consensus golden corpus

**Status: normative.** [`docs/design/03-consensus.md`](../../../docs/design/03-consensus.md) says
this corpus is the authority when the prose and the tests disagree, so a change here is a change to
the specification. `/test/golden/` is CODEOWNERS-protected for that reason, `-update` is refused
when `CI=true`, and `TestGoldenCorpus_FixtureCount_NeverShrinks` fails if a fixture disappears. The
fastest route to a green build must not be rewriting the oracle.

Replayed by `make test-golden`. Each file is one whole call to
`consensus.Derive(reports, quakes, timer, now, cfg)` and its whole expected `State`, compared with
`go-cmp` — never field by field, because a field nobody asserted is a field that can be silently
wrong.

## Shape

```jsonc
{
  "name": "in_window",              // must equal the filename
  "description": "why this fixture exists, and which section of the spec it pins",
  "now": "2026-08-24T23:13:02.000000Z",
  "circle": { "min_reporters_to_supersede": 1 },
  "timer": {
    "kind": "variance",             // fixed | variance | unknown
    "window_open_offset_seconds": 561600,
    "window_close_offset_seconds": 648000,
    "fixed_grace_seconds": 900,
    "cluster_epsilon_seconds": null, // per-timer epsilon override; null means derive it
    "is_quake_target": true
  },
  "quakes":  [ { "id": "01K3Q…", "occurred_at": "…" } ],
  "reports": [ { "id": "01K3R…", "kind": "kill", "died_at": "…", "reported_at": "…",
                 "reporter_membership_id": "01K3M…", "reporter_revoked": false,
                 "source": "log_line", "retracts_report_id": null } ],
  "want": { /* the whole State */ }
}
```

Times are RFC 3339 with six fractional digits, always `Z`, as
[canonical §1](../../../docs/design/00-canonical-conventions.md#1-time) requires. Ids are real
ULIDs — `R01`, `M01`, `Q01` suffixes so a failure names something a human can follow.

`reported_at` is present on every report and the derivation **never reads it**. That is
`LatestDiedAtWins` written into the input shape rather than only into an assertion, and
`backfilled_older_report.json` is the fixture where the two orders disagree.
`TestDerive_ReportedAtPermuted_SameState` asserts it for the whole corpus.

A retraction row carries a `died_at` that deliberately does not match the report it retracts.
Nothing reads it; a fixture where it differs is how we find out if something starts to.

## Choices this corpus makes that the prose does not

`03-consensus.md` is silent or ambiguous on the following. Each is resolved the way that fails safe
— an honest `contested` or a lower confidence beats a confident answer — and each is pinned by a
named fixture so the choice is visible rather than buried in the derivation.

| Question the spec does not answer | Reading, and the fixture that pins it |
|---|---|
| What threshold makes a cluster `wide_spread`? | The five minutes §7 already uses for confidence, rather than a second invented number. `confidence_spread_over_boundary_is_medium` |
| What is a *corroborating* reporter in §7's `high`? | A distinct second reporter whose `died_at` is within five minutes of the estimate. A reporter ten minutes off corroborates nothing. `confidence_log_line_without_corroborator_is_medium` versus `log_line_median_only` |
| Which `contest_reason` wins when several apply? | `implausible_ordering` → `pending_supersede` → `thin_supersede` → `wide_spread`. A contradicted answer is worse news than a thin one, and a thin one is worse than an internally noisy one. `implausible_ordering`, `epsilon_chaining_hazard` |
| What counts as `implausible_ordering`? | Two retained clusters closer together than `window_open_offset_seconds` — the target cannot die twice inside one respawn interval. Nothing is implausible when the timer is `unknown`. `implausible_ordering`, `epsilon_clamped_to_five_minutes` (exactly at the offset, therefore plausible) |
| Confidence for `source = 'import'`? | `low`. §7 names `manual` and `api`; a bulk import is the weakest evidence there is. `confidence_low_single_import` |
| Are `alternatives` only for contested states? | No. Every live-window rival cluster is surfaced; `contested` is a separate, named judgement. `alternatives_capped_at_three_newest_first` |
| Does a `window_kind = 'unknown'` cluster ever expire out of `alternatives`? | No — a window that never closes never closes. `epsilon_unknown_window_kind` |
| What does the window look like with no ToD to anchor it? | `kind` still reports the resolved timer, every boundary is null. `spawn_at` is present iff the timer is fixed **and** a window exists. `unknown_no_reports`, `up_after_quake` |
| Confidence when the target is `up` after a quake? | `unknown` — there is no ToD to be confident about. `up_after_quake` |

Two fields exist in `want` that §7's example does not show:

- **`alternatives_total`** — how many live-window rival clusters there were before the cap of three.
  `AGENTS.md` says never hide a row silently; the cap hides rows, so it counts them.
  `alternatives_capped_at_three_newest_first`.
- **`implausible_report_ids`** — §8 says a physically impossible `died_at` is flagged on *the
  report*. `State` has no per-report structure, so the derivation names the ids and the API layer
  joins them onto the reports it renders. Keeping the predicate here means it has exactly one
  implementation and this corpus is its gate.

`alternatives[].window` is the full window rather than the two keys §7's example elides to, so
there is one window shape in the system and not two.
