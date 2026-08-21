// Package projection maintains `target_state_cache`: the materialised answer for every target in
// every circle.
//
// **The cache is never authority.** Every row here is droppable, and if you find yourself reading
// it to make a decision the derivation should make, that is the bug. The authority is the report
// log and [consensus.Derive] over it; this package feeds that function, stores what it returned,
// and throws the row away the moment anything it was computed from changes.
//
// Three things keep it honest, in ascending order of how much they cost:
//
//  1. **Invalidation is a DELETE, inside the writing transaction.** `internal/tod` clears the row
//     for a `(circle, target)` in the same transaction that appends the report, and a quake clears
//     the whole circle. A cache cleared afterwards is a cache that survives a rollback of the
//     write it was cleared for.
//  2. **A read-miss rebuilds one target.** So a dropped table costs latency, never correctness.
//  3. **The nightly verify job recomputes everything and diffs.** THE RECOMPUTATION WINS and an
//     alert fires — see [Service.Verify]. It is the only thing standing between a stale cache and
//     a wrong board, which is why the integration suite corrupts a row and asserts the repair.
//
// What is cached and what is not is a decision rather than an accident. `status` and the countdowns
// are functions of `now`, so a stored `pre_window` is stale the instant the window opens with no
// write in between; the cache therefore stores the *point estimate* and the evidence behind it, and
// every read re-renders the status and the window from that estimate against the current instant
// through [consensus.Project]. What the cache actually buys is not reading and clustering the log.
package projection
