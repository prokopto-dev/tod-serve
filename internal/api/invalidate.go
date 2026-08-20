package api

import (
	"context"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// TimerInvalidator is told when a respawn window has moved.
//
// It is the fifth `target_state.change_reason` given a caller. The other four are read back off
// the report log, which is what lets invalidation be a DELETE inside the writing transaction: the
// row that was there is gone, and the log still says what happened. `timer_change` is the one the
// log cannot show — a window moved and nothing was reported — so it has to be PUSHED by whoever
// moved it, and the routes that move one are the only things that can.
//
// The consequence of not pushing it is not a stale row somebody notices. It is the board serving
// the OLD window after an officer set an override, which is the exact "our guild has tracked VS
// for two years and the wiki is wrong" case `circle_timer_override` exists for. The nightly verify
// job does catch it, because the recomputation wins — but that is up to twenty-four hours of a
// confidently wrong board, and a confident mistake is this project's named failure mode.
//
// It is an interface declared HERE, beside the handlers that call it, rather than a dependency on
// `internal/projection`: this package is the consumer, the consumer owns the port, and the
// projection satisfies it without knowing the API exists.
type TimerInvalidator interface {
	// OnTimerChange recomputes one target for ONE circle and records `timer_change` as the
	// reason. It is what a circle-scoped override write calls.
	OnTimerChange(ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID) error
	// OnCatalogueTimerChange recomputes one target for EVERY circle pinned to the given server.
	//
	// It is a second method rather than a nullable circle id because the two are genuinely
	// different amounts of work and the caller knows which it did: `raid_target_timer` is
	// instance-wide and per-server, so writing one moves the window for every circle on that
	// server that has not overridden it — and leaves alone every circle that has. Collapsing the
	// two into one signature would put that fan-out decision behind a nil check.
	OnCatalogueTimerChange(ctx context.Context, server core.Server, targetID core.RaidTargetID) error
}

// UnprojectedTimers is a [TimerInvalidator] for a binary that has no projection to invalidate.
//
// **It is temporary and it is scheduled for deletion.** `internal/projection` maintains
// `target_state_cache`; until it is wired into this binary there is no cache, nothing writes a
// derived row, and so there is nothing a moved window could make stale. On THAT binary this type
// is correct rather than a shortcut.
//
// The moment the projection lands it stops being correct, silently, which is precisely the shape
// of failure this file exists to prevent. So it is not left to a comment:
// TestWiring_TimerInvalidation_IsStillTheStub asserts this is what `cmd/tod-serve` passes and says
// what to do the day it is wrong. Whoever lands the projection is sent here to delete this type
// and pass the real service — the same way `uncoveredCircleRoutes` makes closing a tenancy gap an
// edit somebody has to make rather than one they might.
type UnprojectedTimers struct{}

// OnTimerChange does nothing, because there is nothing to invalidate. See the type comment.
func (UnprojectedTimers) OnTimerChange(context.Context, core.CircleID, core.RaidTargetID) error {
	return nil
}

// OnCatalogueTimerChange does nothing, for the same reason.
func (UnprojectedTimers) OnCatalogueTimerChange(
	context.Context, core.Server, core.RaidTargetID,
) error {
	return nil
}
