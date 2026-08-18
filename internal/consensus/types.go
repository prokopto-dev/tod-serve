package consensus

import (
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// Source is `tod_report.source`. A log line carries a machine timestamp read out of the game's own
// file; everything else is a memory, which is the whole reason §5 lets log lines estimate alone.
type Source string

// The report sources, from the enum catalogue.
const (
	SourceLogLine Source = schemaenum.TodReportSourceLogLine
	SourceManual  Source = schemaenum.TodReportSourceManual
	SourceAPI     Source = schemaenum.TodReportSourceAPI
	SourceImport  Source = schemaenum.TodReportSourceImport
)

// ReportKind is `tod_report.kind`.
type ReportKind string

// The report kinds. A correction is a new row, never an edit — canonical conventions §10.
const (
	KindKill       ReportKind = schemaenum.TodReportKindKill
	KindRetraction ReportKind = schemaenum.TodReportKindRetraction
)

// WindowKind is `raid_target_timer.window_kind`.
type WindowKind string

// The window kinds. `unknown` is a first-class value and not a NULL to be interpreted: an unseeded
// instance reports no_timer and still records ToDs correctly — ADR-0008.
const (
	WindowFixed    WindowKind = schemaenum.RaidTargetTimerWindowKindFixed
	WindowVariance WindowKind = schemaenum.RaidTargetTimerWindowKindVariance
	WindowUnknown  WindowKind = schemaenum.RaidTargetTimerWindowKindUnknown
)

// Status is `target_state.status`.
type Status string

// The statuses. `no_timer` is distinct from `unknown` on purpose: one means we have no ToD, the
// other means we have one and no window to hang on it, and a client renders them differently.
const (
	StatusUnknown   Status = schemaenum.TargetStateStatusUnknown
	StatusNoTimer   Status = schemaenum.TargetStateStatusNoTimer
	StatusPreWindow Status = schemaenum.TargetStateStatusPreWindow
	StatusInWindow  Status = schemaenum.TargetStateStatusInWindow
	StatusOverdue   Status = schemaenum.TargetStateStatusOverdue
	StatusUp        Status = schemaenum.TargetStateStatusUp
)

// Confidence is `target_state.confidence`, an ordered enum and never a score. A 0–1 float would be
// false precision, would be a float in a package that bans them, and would be read as a
// probability we cannot compute.
type Confidence string

// The confidence levels, weakest first — `unknown < low < medium < high`.
const (
	ConfidenceUnknown Confidence = schemaenum.TargetStateConfidenceUnknown
	ConfidenceLow     Confidence = schemaenum.TargetStateConfidenceLow
	ConfidenceMedium  Confidence = schemaenum.TargetStateConfidenceMedium
	ConfidenceHigh    Confidence = schemaenum.TargetStateConfidenceHigh
)

// Rank returns the ascending position of c — 0 is the weakest — and whether it has one. The
// ordering lives in the enum catalogue, so "at least medium" has one answer in the codebase.
func (c Confidence) Rank() (int, bool) {
	e, ok := schemaenum.Lookup(schemaenum.NameTargetStateConfidence)
	if !ok {
		return 0, false
	}
	return e.Rank(string(c))
}

// AtLeast reports whether c is at or above other. An unrecognised value is below everything, which
// is the reading that fails safe: a value nobody taught this function about must not pass a
// threshold check.
func (c Confidence) AtLeast(other Confidence) bool {
	mine, ok := c.Rank()
	if !ok {
		return false
	}
	theirs, ok := other.Rank()
	if !ok {
		return false
	}
	return mine >= theirs
}

// ContestReason is `target_state.contest_reason`.
type ContestReason string

// The contest reasons. Disagreement is surfaced, never resolved silently.
const (
	ContestThinSupersede       ContestReason = schemaenum.TargetStateContestReasonThinSupersede
	ContestImplausibleOrdering ContestReason = schemaenum.TargetStateContestReasonImplausibleOrdering
	ContestWideSpread          ContestReason = schemaenum.TargetStateContestReasonWideSpread
	ContestPendingSupersede    ContestReason = schemaenum.TargetStateContestReasonPendingSupersede
)

// Report is one row of `tod_report`, reduced to what the derivation reads.
//
// `self_confidence` and `client_clock_offset_seconds` are columns and are deliberately absent: the
// derivation does not read them, and a field carried here would suggest it does. The per-reporter
// clock-skew correction that would use the second is on the roadmap, not in this function.
type Report struct {
	ID     core.TodReportID `json:"id"`
	Kind   ReportKind       `json:"kind"`
	DiedAt core.Micros      `json:"died_at"`
	// ReportedAt is system truth and the derivation never reads it. It is carried anyway because
	// `LatestDiedAtWins` is a rule about the difference between these two columns, and a rule
	// whose input is absent cannot be tested. TestDerive_ReportedAtPermuted_SameState is the gate.
	ReportedAt           core.Micros       `json:"reported_at"`
	ReporterMembershipID core.MembershipID `json:"reporter_membership_id"`
	// ReporterRevoked makes revocation visible without letting it change the answer: a revoked
	// member's reports still count and their retractions still apply.
	ReporterRevoked bool   `json:"reporter_revoked"`
	Source          Source `json:"source"`
	// RetractsReportID is set iff Kind is KindRetraction.
	RetractsReportID *core.TodReportID `json:"retracts_report_id"`
}

// Quake is one row of `quake_event`. An earthquake repops every raid target on the server at once,
// and modelling that as N kill reports would be a lie nobody observed.
type Quake struct {
	ID         core.QuakeEventID `json:"id"`
	OccurredAt core.Micros       `json:"occurred_at"`
}

// Timer is the respawn window for one target on one server, already resolved through circle
// override → catalogue → unknown. The provenance of that resolution is the caller's to report; the
// derivation only needs the numbers.
type Timer struct {
	Kind WindowKind `json:"kind"`
	// OpenOffsetSeconds and CloseOffsetSeconds are seconds from the ToD, and are nil exactly when
	// Kind is WindowUnknown.
	OpenOffsetSeconds  *int64 `json:"window_open_offset_seconds"`
	CloseOffsetSeconds *int64 `json:"window_close_offset_seconds"`
	// FixedGraceSeconds is what makes `in_window` reachable for a fixed timer at all: without it a
	// fixed target flips pre_window → overdue with no state in between — ADR-0008.
	FixedGraceSeconds int64 `json:"fixed_grace_seconds"`
	// ClusterEpsilonSeconds overrides the derived ε. nil means derive it; see [EpsilonSeconds].
	ClusterEpsilonSeconds *int64 `json:"cluster_epsilon_seconds"`
	// IsQuakeTarget says whether a server-wide repop resets this target.
	//
	// It is a `raid_target` fact rather than a `raid_target_timer` one, and it sits here because
	// the signature §0 fixes has no other parameter that describes the target. The alternative —
	// having the caller pass no quakes for a non-quake target — would move a rule out of the
	// package the corpus gates and into one it does not.
	IsQuakeTarget bool `json:"is_quake_target"`
}

// CircleConfig is the circle's say in the derivation.
type CircleConfig struct {
	// MinReportersToSupersede defaults to 1, in favour of the honest single reporter: the common
	// case is one person typing in a ToD, and a circle that disabled them would have no product.
	// A circle that has been burned raises it — see §4.
	MinReportersToSupersede int `json:"min_reporters_to_supersede"`
}

// Window is where the target respawns and where now sits in that band. Every countdown is a signed
// offset from [State.AsOf] and never an absolute a client subtracts from its own clock — an
// overlay on a machine whose clock is four minutes fast would otherwise render a window that is
// wrong on screen and right in the database.
type Window struct {
	Kind    WindowKind   `json:"kind"`
	OpenAt  *core.Micros `json:"open_at"`
	CloseAt *core.Micros `json:"close_at"`
	// SpawnAt is present iff the timer is fixed and a window exists, so a client can branch on its
	// presence without inspecting Kind.
	SpawnAt *core.Micros `json:"spawn_at"`
	// ProgressBP is basis points by integer division, clamped to [0, 10000]; nil for a fixed or
	// unknown window. A fixed timer has no band to be part-way through.
	ProgressBP *int32 `json:"progress_bp"`
	// SecondsUntilOpen and SecondsUntilClose are signed — negative means passed — and truncate
	// toward zero.
	SecondsUntilOpen  *int64 `json:"seconds_until_open"`
	SecondsUntilClose *int64 `json:"seconds_until_close"`
}

// Evidence is the contract. Confidence is a convenience computed from it, and a client that
// disagrees with our reading can compute its own.
type Evidence struct {
	ReportCount           int   `json:"report_count"`
	DistinctReporterCount int   `json:"distinct_reporter_count"`
	LogLineCount          int   `json:"log_line_count"`
	SpreadSeconds         int64 `json:"spread_seconds"`
	// RevokedReporterCount is the revocation rule made visible: the reports still count.
	RevokedReporterCount int `json:"revoked_reporter_count"`
	// ReportIDs are the current cluster's reports in died_at order, then id.
	ReportIDs []core.TodReportID `json:"report_ids"`
}

// Alternative is a rival cluster whose window has not closed. Anything older is history and is one
// listTodReports call away.
type Alternative struct {
	DiedAt                core.Micros        `json:"died_at"`
	ReportCount           int                `json:"report_count"`
	DistinctReporterCount int                `json:"distinct_reporter_count"`
	Confidence            Confidence         `json:"confidence"`
	Window                Window             `json:"window"`
	ReportIDs             []core.TodReportID `json:"report_ids"`
}

// State is the whole derived answer for one target.
type State struct {
	Status Status `json:"status"`
	// DiedAt is the point estimate, nil when there is no current cluster.
	DiedAt *core.Micros `json:"died_at"`
	// UpSince is set only for StatusUp, and is the quake that repopped the target.
	UpSince    *core.Micros `json:"up_since"`
	Window     Window       `json:"window"`
	Confidence Confidence   `json:"confidence"`
	Contested  bool         `json:"contested"`
	// ContestReason is nil iff Contested is false.
	ContestReason *ContestReason `json:"contest_reason"`
	// Alternatives are live-window rival clusters, newest first, capped at three.
	Alternatives []Alternative `json:"alternatives"`
	// AlternativesTotal is how many there were before the cap. A filter that drops a row counts
	// it somewhere visible.
	AlternativesTotal int      `json:"alternatives_total"`
	Evidence          Evidence `json:"evidence"`
	// ImplausibleReportIDs name observations that cannot be true alongside the current answer.
	// They are flagged and retained: derived state must never veto an observation.
	ImplausibleReportIDs []core.TodReportID `json:"implausible_report_ids"`
	// AsOf is the `now` the derivation was handed. Every countdown above is relative to it.
	AsOf core.Micros `json:"as_of"`
}
