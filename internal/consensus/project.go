package consensus

import "github.com/prokopto-dev/tod-serve/internal/core"

// Project renders §6's window and §7's status for a point estimate that is already known, against
// a fresh `now`.
//
// It exists because both of those are pure functions of `(timer, died_at, up_since, now)` and
// nothing else: `status` moves from `pre_window` to `in_window` with no new report, and every
// countdown moves continuously. A cache of either is stale the instant the clock passes a
// boundary, so `internal/projection` stores the point estimate and re-renders these two on every
// read.
//
// This is an EXPORT of what [Derive] already does, not a second implementation — both call the
// same two unexported helpers, which is the whole reason it lives in this package rather than in
// the one that needs it. TestProject_OverTheGoldenCorpus_AgreesWithDerive is the gate.
//
// `up` is not recomputable from a timer and is therefore an input: `upSince` non-nil means a quake
// repopped the target and no kill has been reported since, which is a fact about the report log
// rather than about the clock.
func Project(
	timer Timer, died *core.Micros, upSince *core.Micros, now core.Micros,
) (Status, Window) {
	return deriveStatus(timer, died, now, upSince), deriveWindow(timer, died, now)
}
