// Package tod is the append-only report log: ToD ingest, quakes and retraction.
//
// **Nothing here ever updates or deletes a row.** `tod_report` and `quake_event` carry
// `BEFORE UPDATE OR DELETE … RAISE(ABORT)` triggers and `LOG001` refuses the statement in
// `db/queries` before it ships, so this package can only append. A correction is a new row; a
// retraction is a new row with `retracts_report_id` set and the original stays visible.
//
// It writes; it does not decide. The current ToD for a target is [consensus.Derive]'s answer over
// these rows, and this package deliberately holds no opinion about which report is right — the one
// place that would be tempting is the physically implausible `died_at`, and
// docs/design/03-consensus.md §8 is explicit that such a report is **accepted and flagged, never
// rejected**: derived state must never veto an observation.
//
// Which target a report is about is `internal/catalogue`'s answer, not this package's: the resolve
// ladder, its ranking rule and its `422 ambiguous_target` all live there and their problems come
// back ready to return. There is deliberately no second ladder behind an interface here — the
// nParse+ plugin sends a parsed name and holds no catalogue, so two ladders would be two answers
// with no way for it to notice which one it got.
//
// The two timestamps are never conflated. `died_at` is game truth and is routinely backdated;
// `reported_at` is system truth and never is. The only hard rejection on a time is a `died_at` in
// the future beyond 120 seconds of clock skew, because that is impossible independent of any
// derivation — and the schema carries the same rule as a CHECK, so a caller that reached the store
// another way meets it too.
package tod
