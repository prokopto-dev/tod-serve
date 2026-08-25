package catalogue

import (
	"context"
	"errors"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// TimerInvalidator is told, INSIDE the transaction that moved a respawn window, that it moved.
//
// It is the fifth `target_state.change_reason` given a caller. The other four are read back off
// the report log, which is what lets invalidation be a DELETE inside the writing transaction: the
// row that was there is gone, and the log still says what happened. `timer_change` is the one the
// log cannot show — a window moved and nothing was reported — so it has to be PUSHED by whoever
// moved it, and the writes that move one are the only things that can.
//
// The consequence of not pushing it is not a stale row somebody notices. It is the board serving
// the OLD window after an officer set an override, which is the exact "our guild has tracked VS
// for two years and the wiki is wrong" case `circle_timer_override` exists for. The nightly verify
// job does catch it, because the recomputation wins — but that is up to twenty-four hours of a
// confidently wrong board, and a confident mistake is this project's named failure mode.
//
// # Why the query set is a parameter
//
// It is the mechanism `audit.Append` already uses, for the same reason: **the derived row is
// written or rolled back with the change that caused it.** A row that survives a rollback is worse
// than no row, because it is believed — and the believed thing here is a board.
//
// It has to be the transaction's OWN query set and not merely a working one, because
// [Service.ResolveTimer] is on the recomputation's path. A resolve on a pooled connection reads
// the snapshot from before the transaction opened, so it would recompute the board from the window
// that was there BEFORE the write: atomicity that is not merely cosmetic but actively wrong.
// `projection.recompute`, `storeOrDrop`, `revokedReporters` and `latestQuake` take it for the same
// reason.
//
// # Why the port is declared here
//
// It lived in `internal/api` until ADR-0013, beside the handlers, on the rule that the consumer
// owns the port. The transaction moved the consumer: it is this package's write methods that call
// it now, and a port whose parameter is a transaction's query set belongs to whoever owns that
// transaction. `internal/projection` satisfies it and imports nothing new — it already reads this
// package for the effective timer — and this package still knows nothing of the projection.
//
// It is passed per call rather than held on the [Service] because the projection is built ON this
// service, so a constructor field would need one of the two built half-wired. Per call also keeps
// the CALLER — a route, or `tod-serve seed timers` — the thing that says a window moved, which is
// what [SeededTimer] and `Route.InvalidatesTimer` are both about.
//
// A nil one is refused rather than skipped. "No invalidator wired" is the exact condition this
// port exists to make impossible, and a nil check that silently no-ops would make it the default.
type TimerInvalidator interface {
	// OnTimerChange recomputes one target for ONE circle and records `timer_change` as the
	// reason. It is what a circle-scoped override write calls.
	OnTimerChange(
		ctx context.Context, q *sqlitegen.Queries,
		circleID core.CircleID, targetID core.RaidTargetID,
	) error
	// OnCatalogueTimerChange recomputes one target for EVERY circle pinned to the given server.
	//
	// It is a second method rather than a nullable circle id because the two are genuinely
	// different amounts of work and the caller knows which it did: `raid_target_timer` is
	// instance-wide and per-server, so writing one moves the window for every circle on that
	// server that has not overridden it — and leaves alone every circle that has. Collapsing the
	// two into one signature would put that fan-out decision behind a nil check.
	OnCatalogueTimerChange(
		ctx context.Context, q *sqlitegen.Queries,
		server core.Server, targetID core.RaidTargetID,
	) error
}

// errNoInvalidator is what a write that moves a window returns when it was handed no port.
//
// It is an error at the call rather than a skipped push for the same reason every secret-minting
// constructor refuses a nil entropy source: the unwired case is the failure the mechanism exists
// to prevent, so it must not be the quiet one.
func errNoInvalidator(what string) error {
	return apierr.Wrap(apierr.CodeInternalError, errors.New(what+
		": no timer invalidator; a write that moves a respawn window has to tell the "+
		"projection inside its own transaction"), "")
}
