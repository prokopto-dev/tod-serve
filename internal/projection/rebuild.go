package projection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// Derived is one target's whole answer, straight out of the derivation.
//
// It is what `getTargetState` returns and what the cache is written from. Both come from the same
// call, so the row a client reads and the row the verify job diffs cannot describe two different
// derivations.
type Derived struct {
	Target catalogue.Target `json:"target"`
	Server string           `json:"server"`
	Status string           `json:"status" enum:"unknown,no_timer,pre_window,in_window,overdue,up"`
	// DiedAt is the point estimate — the median of the current cluster, or of its log lines when it
	// has any — and not any single report's `died_at`.
	DiedAt      *core.Micros     `json:"died_at"`
	UpSince     *core.Micros     `json:"up_since"`
	Window      consensus.Window `json:"window"`
	TimerSource string           `json:"timer_source" enum:"circle_override,catalogue,none"`
	Confidence  string           `json:"confidence" enum:"unknown,low,medium,high"`
	// Contested is disagreement surfaced rather than resolved silently. It always says why.
	Contested     bool    `json:"contested"`
	ContestReason *string `json:"contest_reason"`
	ChangeReason  *string `json:"change_reason"`
	// Alternatives are rival clusters whose window has not closed, newest first and capped at
	// three; AlternativesTotal is how many there were before the cap, so a filter that drops a row
	// counts it somewhere visible.
	Alternatives      []consensus.Alternative `json:"alternatives"`
	AlternativesTotal int                     `json:"alternatives_total"`
	// Evidence is the contract. Confidence is a convenience computed from it, and a client that
	// disagrees with our reading can compute its own.
	Evidence consensus.Evidence `json:"evidence"`
	// ImplausibleReportIDs name observations that cannot be true alongside the current answer.
	// They are flagged and retained: derived state must never veto an observation.
	ImplausibleReportIDs []core.TodReportID `json:"implausible_report_ids"`
	// Reporters is present ONLY for a principal holding `tod.read.attribution`. That separation IS
	// the `observer` role: a circle can share a board with an allied guild without handing over
	// the identity of its trackers, and the evidence counts above stay visible either way.
	Reporters  []Reporter   `json:"reporters,omitempty"`
	ComputedAt *core.Micros `json:"computed_at"`
}

// Reporter is one member behind the current cluster.
type Reporter struct {
	MembershipID core.MembershipID `json:"membership_id"`
	DisplayName  string            `json:"display_name"`
	// Revoked is the revocation rule made visible: their reports still count.
	Revoked bool `json:"revoked"`
}

// Get derives one target's state from the report log and refreshes its cached row.
//
// It derives rather than reading the cache, and that is the point: this is the operation a person
// opens when they want to know why the board says what it says, so it must be the derivation's
// answer and not a recollection of it. The refresh is a side effect, which is what makes a
// read-miss self-healing.
func (s *Service) Get(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID, attribution bool,
) (Derived, error) {
	circle, err := s.circle(ctx, circleID)
	if err != nil {
		return Derived{}, err
	}
	target, err := s.catalogue.Get(ctx, targetID)
	if err != nil {
		return Derived{}, err
	}
	timer, err := s.catalogue.ResolveTimer(ctx, circleID, targetID, core.Server(circle.Server))
	if err != nil {
		return Derived{}, err
	}
	quakes, err := s.latestQuake(ctx, circleID)
	if err != nil {
		return Derived{}, err
	}
	rows, err := s.db.Queries().ListTodReportsForTarget(ctx,
		sqlitegen.ListTodReportsForTargetParams{
			CircleID: circleID.String(), TargetID: targetID.String(),
		})
	if err != nil {
		return Derived{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	revoked, err := s.revokedReporters(ctx, circleID)
	if err != nil {
		return Derived{}, err
	}
	reports, err := tod.ToConsensusReports(rows, revoked)
	if err != nil {
		return Derived{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	now := s.clock.Now()
	state := consensus.Derive(reports, quakes, timer.Timer, now, configOf(circle))
	stored, err := s.storeOrDrop(ctx, circleID, targetID, state, rows, changeReason(state, rows, ""))
	if err != nil {
		return Derived{}, err
	}

	view := toDerived(target, circle.Server, timer, state, stored)
	if attribution {
		members, memberErr := s.memberships(ctx, circleID)
		if memberErr != nil {
			return Derived{}, memberErr
		}
		view.Reporters, err = reportersOf(state, rows, members)
		if err != nil {
			return Derived{}, apierr.Wrap(apierr.CodeInternalError, err, "")
		}
	}
	return view, nil
}

// toDerived renders a state as the wire representation.
//
// `row` is nil for a target nothing has ever been reported about, which has no cache row by
// construction — see [Service.storeOrDrop]. `computed_at` is null there rather than an invented
// instant: this answer was derived now, from nothing, and saying otherwise would claim a
// derivation happened that did not.
func toDerived(
	target catalogue.Target, server string, timer catalogue.ResolvedTimer,
	state consensus.State, row *sqlitegen.TargetStateCache,
) Derived {
	view := Derived{
		Target: target, Server: server, Status: string(state.Status),
		DiedAt: state.DiedAt, UpSince: state.UpSince, Window: state.Window,
		TimerSource: string(timer.Source), Confidence: string(state.Confidence),
		Contested:            state.Contested,
		Alternatives:         state.Alternatives,
		AlternativesTotal:    state.AlternativesTotal,
		Evidence:             state.Evidence,
		ImplausibleReportIDs: state.ImplausibleReportIDs,
	}
	if state.ContestReason != nil {
		reason := string(*state.ContestReason)
		view.ContestReason = &reason
	}
	if row != nil {
		view.ChangeReason = row.ChangeReason
		computed := core.Micros(row.ComputedAt)
		view.ComputedAt = &computed
	}
	return view
}

// reporters names the members behind the current cluster, deduplicated, in the order their
// reports appear in it.
//
// It is built from the cluster's own report ids rather than from every report for the target: a
// reporter whose only report was retracted, or whose kill belongs to an older cluster, is not
// evidence for the answer on screen, and listing them would inflate what the board appears to be
// standing on.
func reportersOf(
	state consensus.State, rows []sqlitegen.TodReport, members map[string]sqlitegen.Membership,
) ([]Reporter, error) {
	if len(state.Evidence.ReportIDs) == 0 {
		return nil, nil
	}
	byReportID := make(map[string]string, len(rows))
	for _, row := range rows {
		byReportID[row.ID] = row.ReporterMembershipID
	}

	seen := make(map[string]bool, len(state.Evidence.ReportIDs))
	out := make([]Reporter, 0, len(state.Evidence.ReportIDs))
	for _, reportID := range state.Evidence.ReportIDs {
		membershipID, ok := byReportID[reportID.String()]
		if !ok || seen[membershipID] {
			continue
		}
		seen[membershipID] = true
		id, err := core.ParseID[core.Membership](membershipID)
		if err != nil {
			return nil, err
		}
		member, known := members[membershipID]
		out = append(out, Reporter{
			MembershipID: id,
			// A membership row that is gone is unrepresentable — the log's foreign key is
			// `(circle_id, reporter_membership_id)` and there is no delete-membership path
			// anywhere — so an unknown name here means a bug, and an empty string is a visible
			// one rather than a plausible-looking substitute.
			DisplayName: member.DisplayName,
			Revoked:     known && member.RevokedAt != nil,
		})
	}
	return out, nil
}

// recompute derives one target and writes the row, for a read-miss on the board.
func (s *Service) recompute(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
	circle sqlitegen.Circle, timers map[core.RaidTargetID]catalogue.ResolvedTimer,
	quakes []consensus.Quake, reason string,
) (*sqlitegen.TargetStateCache, error) {
	rows, err := s.db.Queries().ListTodReportsForTarget(ctx,
		sqlitegen.ListTodReportsForTargetParams{
			CircleID: circleID.String(), TargetID: targetID.String(),
		})
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	revoked, err := s.revokedReporters(ctx, circleID)
	if err != nil {
		return nil, err
	}
	reports, err := tod.ToConsensusReports(rows, revoked)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	timer, ok := timers[targetID]
	if !ok {
		timer = catalogue.ResolvedTimer{
			Source: catalogue.TimerSourceNone,
			Timer: consensus.Timer{
				Kind:              consensus.WindowUnknown,
				FixedGraceSeconds: catalogue.DefaultFixedGraceSeconds,
			},
		}
	}
	state := consensus.Derive(reports, quakes, timer.Timer, s.clock.Now(), configOf(circle))
	return s.storeOrDrop(ctx, circleID, targetID, state, rows, changeReason(state, rows, reason))
}

// storeOrDrop writes a derived state, or removes the row when there is nothing to cache.
//
// **A cache row exists for exactly the targets with at least one row in `tod_report`, and that
// single predicate is what makes this subsystem coherent.** The board's read-miss rebuild is driven
// by `ListTodReportTargets`, `deriveAll` walks the same set, and [Service.Verify] treats anything
// else it finds as an orphan — so a row written for a target with no log at all is a row the
// nightly job deletes and ALERTS about. `getTargetState` on a mob nobody has reported is an
// ordinary thing for a person to do, and before this it left exactly that row behind: opening a
// detail page on a fresh instance made `verify-states` fail that night for no reason, which is the
// cry-wolf failure the whole design is built against.
//
// A target whose every kill has been RETRACTED keeps its row, and that is not the same case. It
// still has a log — the retracted kills and the retraction rows — so folding them is real work,
// the answer is a real derivation, and caching "we read your fifty rows and there is no current
// ToD" is exactly what a cache is for. Dropping it would mean re-clustering that log on every
// board render forever, and it would make an ordinary retraction fire the drift alert.
func (s *Service) storeOrDrop(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
	state consensus.State, rows []sqlitegen.TodReport, reason string,
) (*sqlitegen.TargetStateCache, error) {
	if len(rows) == 0 {
		if _, err := s.db.Queries().InvalidateTargetState(ctx,
			sqlitegen.InvalidateTargetStateParams{
				CircleID: circleID.String(), TargetID: targetID.String(),
			}); err != nil {
			return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
		}
		return nil, nil
	}
	row, err := s.store(ctx, circleID, targetID, state, rows, reason)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// store writes one derived state to the cache.
//
// `computed_at` is the instant the derivation was handed, not the instant of the write: the row
// records what was true at a moment, and the verify job compares it against a recomputation at a
// different one.
func (s *Service) store(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
	state consensus.State, rows []sqlitegen.TodReport, reason string,
) (sqlitegen.TargetStateCache, error) {
	now := s.clock.Now()
	params := sqlitegen.PutTargetStateParams{
		CircleID: circleID.String(), TargetID: targetID.String(),
		ComputedAt:  int64(state.AsOf),
		ReportCount: int64(state.Evidence.ReportCount),
		Status:      string(state.Status), Confidence: string(state.Confidence),
		Contested:             boolToInt(state.Contested),
		DistinctReporterCount: int64(state.Evidence.DistinctReporterCount),
		LogLineCount:          int64(state.Evidence.LogLineCount),
		RevokedReporterCount:  int64(state.Evidence.RevokedReporterCount),
		CreatedAt:             int64(now), UpdatedAt: int64(now),
	}
	if state.ContestReason != nil {
		contest := string(*state.ContestReason)
		params.ContestReason = &contest
	}
	if reason != "" {
		params.ChangeReason = &reason
	}
	if state.DiedAt != nil {
		params.DiedAt = int64Ptr(int64(*state.DiedAt))
	}
	if state.Window.OpenAt != nil {
		params.WindowOpenAt = int64Ptr(int64(*state.Window.OpenAt))
	}
	if state.Window.CloseAt != nil {
		params.WindowCloseAt = int64Ptr(int64(*state.Window.CloseAt))
	}
	if state.Window.SpawnAt != nil {
		params.SpawnAt = int64Ptr(int64(*state.Window.SpawnAt))
	}
	if len(state.Evidence.ReportIDs) > 0 {
		params.SpreadSeconds = int64Ptr(state.Evidence.SpreadSeconds)
	}
	if latest := latestReportID(rows); latest != "" {
		params.LatestReportID = &latest
	}

	row, err := s.db.Queries().PutTargetState(ctx, params)
	if err != nil {
		return sqlitegen.TargetStateCache{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return row, nil
}

// latestReportID is the newest row for the target by id, which is ULID order and therefore arrival
// order. It is `reported_at`'s ordering and not `died_at`'s, deliberately: this column says which
// row the cache last saw, and a backdated report is still the most recent thing that happened.
func latestReportID(rows []sqlitegen.TodReport) string {
	latest := ""
	for _, row := range rows {
		if row.ID > latest {
			latest = row.ID
		}
	}
	return latest
}

// changeReason says what kind of event last moved this answer.
//
// It is read off the log rather than by diffing against the row that was there before, because the
// row that was there before has already been deleted: invalidation is a DELETE inside the writing
// transaction, which is what makes a cache that cannot outlive a rolled-back write. The log still
// says what happened, and these four are exactly what it can say.
//
// `timer_change` is the fifth value and is the one the log cannot show — a window moved with no
// row appended — so it is passed in by the caller that changed the timer. [Service.OnTimerChange]
// is that caller.
func changeReason(state consensus.State, rows []sqlitegen.TodReport, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if len(rows) == 0 {
		return ""
	}
	if state.Status == schemaenum.TargetStateStatusUp {
		return schemaenum.TargetStateChangeReasonQuake
	}
	newest := rows[0]
	for _, row := range rows[1:] {
		if row.ID > newest.ID {
			newest = row
		}
	}
	if newest.Kind == schemaenum.TodReportKindRetraction {
		return schemaenum.TargetStateChangeReasonRetraction
	}
	// A kill that landed in the current cluster alongside others did not move the board to a new
	// kill; it corroborated one, and it may well have shifted the median. Saying so is what makes
	// "the answer changed and nobody died" legible rather than alarming.
	if slices.Contains(state.Evidence.ReportIDs, mustParseReportID(newest.ID)) &&
		state.Evidence.ReportCount > 1 {
		return schemaenum.TargetStateChangeReasonCorroboration
	}
	return schemaenum.TargetStateChangeReasonNewKill
}

// mustParseReportID is safe on a row that came out of the database: the column is a ULID this
// binary wrote. A row that is not one produces the zero id, which matches nothing — the reason
// degrades to `new_kill`, which is the reading that under-claims rather than over-claims.
func mustParseReportID(raw string) core.TodReportID {
	id, err := core.ParseID[core.TodReport](raw)
	if err != nil {
		return core.TodReportID{}
	}
	return id
}

func configOf(circle sqlitegen.Circle) consensus.CircleConfig {
	return consensus.CircleConfig{MinReportersToSupersede: int(circle.MinReportersToSupersede)}
}

// OnTimerChange recomputes one target in ONE circle because its window moved rather than because
// anything was reported, and records `timer_change` as the reason.
//
// It exists so the fifth `change_reason` has a caller: a timer edit changes every answer derived
// from it with no row appended anywhere, and a board that changed for no visible reason is the
// thing docs/design/03-consensus.md §8 says must not happen.
//
// **It returns its error, and the route that called it fails the request.** Both writes are
// idempotent, so a retry converges; reporting success while the board goes on serving the old
// window is the outcome worth avoiding, and it is the one a swallowed error produces.
func (s *Service) OnTimerChange(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
) error {
	circle, err := s.circle(ctx, circleID)
	if err != nil {
		return err
	}
	return s.recomputeForTimerChange(ctx, circle, targetID)
}

// OnCatalogueTimerChange recomputes one target for EVERY circle pinned to the given server.
//
// `raid_target_timer` is instance-wide and per-server, so writing one row moves the window for
// every circle on that server that has not overridden it — and leaves alone every circle that has,
// because [catalogue.Service.ResolveTimer] puts the override first and this recomputation goes
// through it like any other. **The fan-out is a loop here rather than a nil check somewhere**: the
// port is two methods rather than one nullable circle id precisely so the amount of work is
// visible at the call site, and hiding it behind a branch in this function would give that back.
//
// Every circle is attempted even after one fails, and the failures are joined. A partial
// invalidation is the bad outcome, so the answer names all of it rather than the first circle that
// happened to break — and the caller still fails, because a seed or a route that reported success
// with three boards stale is exactly what this is for.
func (s *Service) OnCatalogueTimerChange(
	ctx context.Context, server core.Server, targetID core.RaidTargetID,
) error {
	if !server.Valid() {
		return apierr.Newf(apierr.CodeValidationFailed,
			"server must be one of %s", core.Servers())
	}
	circles, err := s.db.Queries().ListLiveCirclesOnServer(ctx, string(server))
	if err != nil {
		return apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	var failures []error
	for _, circle := range circles {
		if recomputeErr := s.recomputeForTimerChange(ctx, circle, targetID); recomputeErr != nil {
			failures = append(failures, fmt.Errorf("circle %s: %w", circle.ID, recomputeErr))
		}
	}
	if len(failures) > 0 {
		return apierr.Wrap(apierr.CodeInternalError, errors.Join(failures...), "")
	}

	s.log.InfoContext(ctx, "catalogue timer change fanned out",
		slog.String("server", string(server)),
		slog.String("target_id", targetID.String()),
		slog.Int("circles", len(circles)))
	return nil
}

// recomputeForTimerChange is the one circle's worth of work both entry points do.
func (s *Service) recomputeForTimerChange(
	ctx context.Context, circle sqlitegen.Circle, targetID core.RaidTargetID,
) error {
	circleID, err := core.ParseID[core.Circle](circle.ID)
	if err != nil {
		return apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	timer, err := s.catalogue.ResolveTimer(ctx, circleID, targetID, core.Server(circle.Server))
	if err != nil {
		return err
	}
	quakes, err := s.latestQuake(ctx, circleID)
	if err != nil {
		return err
	}
	_, err = s.recompute(ctx, circleID, targetID, circle,
		map[core.RaidTargetID]catalogue.ResolvedTimer{targetID: timer}, quakes,
		schemaenum.TargetStateChangeReasonTimerChange)
	return err
}

// Rebuild recomputes every cached state in one circle from the report log.
//
// This is `tod-serve rebuild-states` for one circle, and it is also the shape the verify job
// reuses. It reads the whole log once and groups in Go rather than issuing a query per target: a
// rebuild that ran a query per mob would be the slowest thing the binary does, on a schedule
// nobody watches.
func (s *Service) Rebuild(ctx context.Context, circleID core.CircleID) (int, error) {
	circle, err := s.circle(ctx, circleID)
	if err != nil {
		return 0, err
	}
	states, err := s.deriveAll(ctx, circle)
	if err != nil {
		return 0, err
	}
	for _, derived := range states {
		if _, err := s.store(ctx, circleID, derived.targetID, derived.state,
			derived.rows, derived.reason); err != nil {
			return 0, err
		}
	}
	return len(states), nil
}

// RebuildAll rebuilds every live circle, and reports how many states it wrote.
func (s *Service) RebuildAll(ctx context.Context) (int, error) {
	circles, err := s.db.Queries().ListLiveCircles(ctx)
	if err != nil {
		return 0, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	total := 0
	for _, row := range circles {
		id, parseErr := core.ParseID[core.Circle](row.ID)
		if parseErr != nil {
			return 0, apierr.Wrap(apierr.CodeInternalError, parseErr, "")
		}
		n, rebuildErr := s.Rebuild(ctx, id)
		if rebuildErr != nil {
			return 0, rebuildErr
		}
		total += n
	}
	s.log.InfoContext(ctx, "target states rebuilt",
		slog.Int("circles", len(circles)), slog.Int("states", total))
	return total, nil
}

// derivedState is one recomputation, before it is written or diffed.
type derivedState struct {
	targetID core.RaidTargetID
	state    consensus.State
	rows     []sqlitegen.TodReport
	reason   string
}

// deriveAll recomputes every target the circle has reported anything about.
//
// Targets nobody has reported are deliberately absent, and that absence is the same predicate
// [Service.storeOrDrop] writes by: with no rows in `tod_report` there is nothing to fold, so the
// answer is a pure function of the timer and the quake log and needs no row to hold it. The verify
// job removes any cache row it finds for one.
//
// A target whose every kill has been retracted is NOT one of those. Its rows are still there — the
// log is append-only, so a retraction adds — so it is derived here like any other, and it keeps a
// cached row saying there is no current ToD.
func (s *Service) deriveAll(
	ctx context.Context, circle sqlitegen.Circle,
) ([]derivedState, error) {
	circleID, err := core.ParseID[core.Circle](circle.ID)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	rows, err := s.db.Queries().ListTodReportsForCircle(ctx, circle.ID)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	timers, err := s.catalogue.ResolveTimers(ctx, circleID, core.Server(circle.Server))
	if err != nil {
		return nil, err
	}
	quakes, err := s.latestQuake(ctx, circleID)
	if err != nil {
		return nil, err
	}
	revoked, err := s.revokedReporters(ctx, circleID)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	cfg := configOf(circle)
	out := make([]derivedState, 0)
	for _, group := range groupByTarget(rows) {
		reports, convErr := tod.ToConsensusReports(group.rows, revoked)
		if convErr != nil {
			return nil, apierr.Wrap(apierr.CodeInternalError, convErr, "")
		}
		timer, ok := timers[group.targetID]
		if !ok {
			timer = catalogue.ResolvedTimer{
				Source: catalogue.TimerSourceNone,
				Timer: consensus.Timer{
					Kind:              consensus.WindowUnknown,
					FixedGraceSeconds: catalogue.DefaultFixedGraceSeconds,
				},
			}
		}
		state := consensus.Derive(reports, quakes, timer.Timer, now, cfg)
		out = append(out, derivedState{
			targetID: group.targetID, state: state, rows: group.rows,
			reason: changeReason(state, group.rows, ""),
		})
	}
	return out, nil
}

// targetGroup is one target's rows out of a whole-circle read.
type targetGroup struct {
	targetID core.RaidTargetID
	rows     []sqlitegen.TodReport
}

// groupByTarget splits a circle's log, which the query already returned in target order.
func groupByTarget(rows []sqlitegen.TodReport) []targetGroup {
	groups := make([]targetGroup, 0)
	for _, row := range rows {
		id, err := core.ParseID[core.RaidTarget](row.TargetID)
		if err != nil {
			continue
		}
		if n := len(groups); n > 0 && groups[n-1].targetID == id {
			groups[n-1].rows = append(groups[n-1].rows, row)
			continue
		}
		groups = append(groups, targetGroup{targetID: id, rows: []sqlitegen.TodReport{row}})
	}
	return groups
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func int64Ptr(v int64) *int64 { return &v }
