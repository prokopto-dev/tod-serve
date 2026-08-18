// Package consensus derives the current time of death for one raid target from the report log.
//
// It is a pure function of its arguments — no store, no clock, no randomness and no floats. The
// float ban is a reproducibility rule rather than a money rule: the nightly projection-verify job
// diffs the cached state against a fresh recomputation and alerts on any difference, and a window
// boundary that is not bit-identical across platforms would make that job cry wolf until somebody
// turned it off. Ratios are basis points. See canonical conventions §3.
//
// The specification is docs/design/03-consensus.md, and the golden corpus in
// test/golden/consensus is the authority when that prose and these tests disagree.
//
// # The order of the derivation
//
// Each step below is §-numbered against the specification, and the order matters: quake
// truncation reads the reports a retraction has already removed, and clustering reads what the
// quake left behind.
//
//  1. Retraction folding (§1) — drop every kill a retraction names. A retraction of a retraction
//     is not supported and is ignored.
//  2. Quake truncation (§2) — for a quake target, everything before the latest quake is history.
//     With nothing after it, the target is up.
//  3. Clustering (§3) — sort by died_at, single-linkage within ε of the previous report, total
//     span capped at 2ε.
//  4. Current cluster (§4) — the latest died_at wins, unless min_reporters_to_supersede holds a
//     live, better corroborated cluster in place.
//  5. Point estimate (§5) — the median, and the median of the log lines alone if the cluster has
//     any.
//  6. Window (§6), then status, confidence, evidence and contest (§7).
//
// # What this package deliberately does not decide
//
// `change_reason` needs the previous state and is therefore internal/projection's, not this
// package's: Derive is a function of the reports and nothing else. Timer resolution — circle
// override, then catalogue, then unknown — happens before the call, which is why [Timer] carries
// no provenance.
//
// # Where the specification is silent
//
// Several questions §7 and §8 leave open are answered here rather than left to the caller, and
// each answer is the one that fails safe: an honest `contested` or a lower confidence beats a
// confident answer. They are listed, with the fixture that pins each, in
// test/golden/consensus/README.md. The ones that show up in this package's shape are:
//
//   - [State.ImplausibleReportIDs] exists because §8 flags implausibility on *the report* and
//     [State] has no per-report structure. Naming the ids here keeps the predicate in one place
//     with the corpus as its gate.
//   - [State.AlternativesTotal] exists because the cap of three hides rows, and AGENTS.md says
//     never hide a row silently.
package consensus
