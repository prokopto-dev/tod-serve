package catalogue

import (
	"context"
	"log/slog"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// TimerSource says where a resolved timer came from.
//
// It is a typed value and not a free string because it is `timer_source` on the wire —
// `getTargetState` publishes it, 02-api-design shows `"timer_source": "circle_override"` in the
// response body — so it is part of the contract a client branches on, not a debugging aid.
type TimerSource string

// The three sources, in the resolution order canonical §15 and the domain model both state:
// circle override, then catalogue, then nothing.
const (
	// TimerSourceCircleOverride means this circle disagreed with the catalogue and said so. It
	// wins, always: "our guild has tracked VS for two years and the wiki is wrong" is the reason
	// `circle_timer_override` exists.
	TimerSourceCircleOverride TimerSource = "circle_override"
	// TimerSourceCatalogue means the instance has been handed a seed and it covered this target on
	// this server.
	TimerSourceCatalogue TimerSource = "catalogue"
	// TimerSourceNone means nobody has told this instance anything about this target's window.
	//
	// It is a first-class answer and not a failure. Timer data does not ship — canonical §15 — so
	// this is what EVERY target resolves to on the day an operator installs the binary, and the
	// derivation turns it into `status: no_timer` while recording ToDs exactly as it otherwise
	// would. A source that guessed here would be the confident mistake this project is built
	// against.
	TimerSourceNone TimerSource = "none"
)

// ResolvedTimer is the effective window for one target in one circle, and where it came from.
//
// The Timer is a [consensus.Timer] rather than a shape of this package's own, so the projection
// hands it straight to [consensus.Derive] with nothing in between to get wrong.
type ResolvedTimer struct {
	Source TimerSource     `json:"timer_source"`
	Timer  consensus.Timer `json:"timer"`
}

// ResolveTimer returns the effective timer for one target in one circle: circle override, then
// catalogue timer, then unknown.
//
// The server is the CIRCLE's server. It is a parameter rather than something this function reads
// off the circle row because the caller — the projection — already holds it, and a second read of
// a column that cannot change (ADR-0009 pins a circle to one server immutably) would be a query
// per target per board render for a value that is constant.
//
// A target id that names nothing is an error; a target with no timer anywhere is not. Those are
// genuinely different: the first is a caller bug and the second is Tuesday.
func (s *Service) ResolveTimer(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID, server core.Server,
) (ResolvedTimer, error) {
	if !server.Valid() {
		return ResolvedTimer{}, apierr.Newf(apierr.CodeValidationFailed,
			"server must be one of %s", core.Servers())
	}
	target, err := s.Get(ctx, targetID)
	if err != nil {
		return ResolvedTimer{}, err
	}

	q := s.db.Queries()
	override, err := q.GetCircleTimerOverride(ctx, sqlitegen.GetCircleTimerOverrideParams{
		CircleID: circleID.String(), TargetID: targetID.String(),
	})
	switch {
	case err == nil:
		return ResolvedTimer{
			Source: TimerSourceCircleOverride,
			Timer: timerOf(target, override.WindowKind,
				override.WindowOpenOffsetSeconds, override.WindowCloseOffsetSeconds,
				override.FixedGraceSeconds, override.ClusterEpsilonSeconds),
		}, nil
	case !store.IsNotFound(err):
		return ResolvedTimer{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	row, err := q.GetRaidTargetTimer(ctx, sqlitegen.GetRaidTargetTimerParams{
		TargetID: targetID.String(), Server: string(server),
	})
	switch {
	case err == nil:
		return ResolvedTimer{
			Source: TimerSourceCatalogue,
			Timer: timerOf(target, row.WindowKind,
				row.WindowOpenOffsetSeconds, row.WindowCloseOffsetSeconds,
				row.FixedGraceSeconds, row.ClusterEpsilonSeconds),
		}, nil
	case !store.IsNotFound(err):
		return ResolvedTimer{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	return ResolvedTimer{Source: TimerSourceNone, Timer: unknownTimer(target)}, nil
}

// ResolveTimers is [Service.ResolveTimer] for every active target at once, keyed by target id.
//
// The board resolves the whole catalogue on every render. Doing that one target at a time is three
// queries per target; this is three queries in total, and the precedence is the same code path
// rather than a second copy of it — [resolveTimerFrom] is shared by both.
func (s *Service) ResolveTimers(
	ctx context.Context, circleID core.CircleID, server core.Server,
) (map[core.RaidTargetID]ResolvedTimer, error) {
	if !server.Valid() {
		return nil, apierr.Newf(apierr.CodeValidationFailed,
			"server must be one of %s", core.Servers())
	}
	targets, err := s.loadTargets(ctx)
	if err != nil {
		return nil, err
	}
	q := s.db.Queries()
	overrideRows, err := q.ListCircleTimerOverrides(ctx, circleID.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	timerRows, err := q.ListRaidTargetTimersForServer(ctx, string(server))
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	overrides := make(map[string]sqlitegen.CircleTimerOverride, len(overrideRows))
	for _, row := range overrideRows {
		overrides[row.TargetID] = row
	}
	timers := make(map[string]sqlitegen.RaidTargetTimer, len(timerRows))
	for _, row := range timerRows {
		timers[row.TargetID] = row
	}

	out := make(map[core.RaidTargetID]ResolvedTimer, len(targets))
	for _, target := range targets {
		out[target.ID] = resolveTimerFrom(target, overrides, timers)
	}
	return out, nil
}

// resolveTimerFrom applies the precedence over already-read rows. It is the one place the order
// lives, so the single-target and whole-board paths cannot drift apart.
func resolveTimerFrom(
	target Target,
	overrides map[string]sqlitegen.CircleTimerOverride,
	timers map[string]sqlitegen.RaidTargetTimer,
) ResolvedTimer {
	if override, ok := overrides[target.ID.String()]; ok {
		return ResolvedTimer{
			Source: TimerSourceCircleOverride,
			Timer: timerOf(target, override.WindowKind,
				override.WindowOpenOffsetSeconds, override.WindowCloseOffsetSeconds,
				override.FixedGraceSeconds, override.ClusterEpsilonSeconds),
		}
	}
	if row, ok := timers[target.ID.String()]; ok {
		return ResolvedTimer{
			Source: TimerSourceCatalogue,
			Timer: timerOf(target, row.WindowKind,
				row.WindowOpenOffsetSeconds, row.WindowCloseOffsetSeconds,
				row.FixedGraceSeconds, row.ClusterEpsilonSeconds),
		}
	}
	return ResolvedTimer{Source: TimerSourceNone, Timer: unknownTimer(target)}
}

// timerOf renders stored window columns as the derivation's timer.
func timerOf(
	target Target, kind string, openOffset, closeOffset *int64, grace int64, epsilon *int64,
) consensus.Timer {
	return consensus.Timer{
		Kind:                  consensus.WindowKind(kind),
		OpenOffsetSeconds:     openOffset,
		CloseOffsetSeconds:    closeOffset,
		FixedGraceSeconds:     grace,
		ClusterEpsilonSeconds: epsilon,
		IsQuakeTarget:         target.IsQuakeTarget,
	}
}

// unknownTimer is what a target with no window anywhere resolves to.
//
// `IsQuakeTarget` is still carried, and that is the point of populating it rather than returning a
// zero value: an unseeded instance still knows a quake repops the target, so a quake still
// truncates its reports and the board still says `up`. Losing that here would make an unseeded
// instance quietly wrong about quakes as well as silent about windows.
func unknownTimer(target Target) consensus.Timer {
	return consensus.Timer{
		Kind:              consensus.WindowUnknown,
		FixedGraceSeconds: DefaultFixedGraceSeconds,
		IsQuakeTarget:     target.IsQuakeTarget,
	}
}

// WindowRequest is the window half of a timer, shared by `putRaidTargetTimer` and
// `putCircleTimerOverride` because the two write the same five columns under the same four CHECK
// constraints, and validating them twice is how the two answers start to differ.
type WindowRequest struct {
	WindowKind               string
	WindowOpenOffsetSeconds  *int64
	WindowCloseOffsetSeconds *int64
	// FixedGraceSeconds is nil when the caller did not say, and defaults to
	// [DefaultFixedGraceSeconds].
	FixedGraceSeconds     *int64
	ClusterEpsilonSeconds *int64
	Source                string
	Note                  string
}

// validated is a window that satisfies every constraint the table holds, resolved to the values
// that will be written.
type validated struct {
	kind        string
	openOffset  *int64
	closeOffset *int64
	grace       int64
	epsilon     *int64
}

// validate refuses in Go exactly what the four CHECK constraints refuse in SQLite.
//
// The constraints are the enforcement and this is not a second copy of the rule so much as a
// translation of it: a CHECK failure surfaces as a driver error with a constraint name in it,
// which is a 500 and a log line rather than a 422 that says which field was wrong. The gate stays
// in the schema; this makes it answerable.
func (w WindowRequest) validate(field string) (validated, error) {
	v := validated{
		kind:       w.WindowKind,
		openOffset: w.WindowOpenOffsetSeconds, closeOffset: w.WindowCloseOffsetSeconds,
		grace: DefaultFixedGraceSeconds, epsilon: w.ClusterEpsilonSeconds,
	}
	if w.FixedGraceSeconds != nil {
		v.grace = *w.FixedGraceSeconds
	}

	fail := func(at, why string) (validated, error) {
		return validated{}, apierr.New(apierr.CodeValidationFailed, why).WithField(field+at, why)
	}
	switch v.kind {
	case schemaenum.RaidTargetTimerWindowKindUnknown:
		if v.openOffset != nil || v.closeOffset != nil {
			// Not silently cleared. A caller sending offsets alongside `unknown` has contradicted
			// themselves, and picking one half of it for them is how a window nobody entered ends
			// up on a board.
			return fail(".window_kind",
				"an unknown window carries no offsets; send neither, or a kind that has them")
		}
	case schemaenum.RaidTargetTimerWindowKindVariance,
		schemaenum.RaidTargetTimerWindowKindFixed:
		if v.openOffset == nil || v.closeOffset == nil {
			return fail(".window_open_offset_seconds",
				"a "+v.kind+" window needs both an open and a close offset")
		}
		if *v.closeOffset < *v.openOffset {
			return fail(".window_close_offset_seconds",
				"the close offset is before the open offset")
		}
		if (v.kind == schemaenum.RaidTargetTimerWindowKindFixed) !=
			(*v.openOffset == *v.closeOffset) {
			return fail(".window_kind",
				"a fixed window is a point — equal offsets — and a variance window is a band")
		}
	default:
		return fail(".window_kind", "window_kind must be fixed, variance or unknown")
	}
	if v.grace < 0 {
		return fail(".fixed_grace_seconds", "fixed_grace_seconds must not be negative")
	}
	if v.epsilon != nil && *v.epsilon < 0 {
		return fail(".cluster_epsilon_seconds", "cluster_epsilon_seconds must not be negative")
	}
	return v, nil
}

// PutTimer writes a target's catalogue timer for one server.
//
// The numbers are not ours and this is the route an operator uses to say so by hand; the bulk path
// is `tod-serve seed timers --file`, which calls the same write.
func (s *Service) PutTimer(
	ctx context.Context, id core.RaidTargetID, server core.Server, req WindowRequest,
) (TargetTimer, error) {
	if !server.Valid() {
		return TargetTimer{}, apierr.Newf(apierr.CodeValidationFailed,
			"server must be one of %s", core.Servers()).
			WithField("path.server", "not a server")
	}
	v, err := req.validate("body")
	if err != nil {
		return TargetTimer{}, err
	}
	if _, err = s.Get(ctx, id); err != nil {
		return TargetTimer{}, err
	}

	now := s.clock.Now()
	row, err := s.db.Queries().PutRaidTargetTimer(ctx, putTimerParams(id, server, v, req, now))
	if err != nil {
		return TargetTimer{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	s.log.InfoContext(ctx, "catalogue timer written",
		slog.String("target_id", id.String()), slog.String("server", string(server)),
		slog.String("window_kind", v.kind))
	return toTargetTimer(row), nil
}

func putTimerParams(
	id core.RaidTargetID, server core.Server, v validated, req WindowRequest, now core.Micros,
) sqlitegen.PutRaidTargetTimerParams {
	params := sqlitegen.PutRaidTargetTimerParams{
		TargetID: id.String(), Server: string(server), WindowKind: v.kind,
		WindowOpenOffsetSeconds: v.openOffset, WindowCloseOffsetSeconds: v.closeOffset,
		FixedGraceSeconds: v.grace, ClusterEpsilonSeconds: v.epsilon,
		Note: req.Note, CreatedAt: int64(now), UpdatedAt: int64(now),
	}
	if req.Source != "" {
		source := req.Source
		params.Source = &source
	}
	return params
}

// TimerOverride is one circle's disagreement with the catalogue, as a client reads it.
//
// It carries no `server`: a circle is pinned to one server immutably (ADR-0009), so the override
// is per (circle, target) and a server column would be a second place for the same fact to be
// wrong.
type TimerOverride struct {
	TargetID   core.RaidTargetID `json:"target_id"`
	TargetName string            `json:"target_name" doc:"The target's canonical name, so a list of overrides is readable"`
	WindowKind string            `json:"window_kind" enum:"fixed,variance,unknown"`

	WindowOpenOffsetSeconds  *int64 `json:"window_open_offset_seconds"`
	WindowCloseOffsetSeconds *int64 `json:"window_close_offset_seconds"`
	FixedGraceSeconds        int64  `json:"fixed_grace_seconds"`
	ClusterEpsilonSeconds    *int64 `json:"cluster_epsilon_seconds"`
	Note                     string `json:"note"`
	// CreatedByMembershipID names who disagreed. An override changes every board in the circle, so
	// the person who set it is part of what it is.
	CreatedByMembershipID core.MembershipID `json:"created_by_membership_id"`
	CreatedAt             core.Micros       `json:"created_at"`
	UpdatedAt             core.Micros       `json:"updated_at"`
}

// ListOverrides returns every override a circle holds, ordered by target id.
func (s *Service) ListOverrides(
	ctx context.Context, circleID core.CircleID,
) ([]TimerOverride, error) {
	rows, err := s.db.Queries().ListCircleTimerOverrides(ctx, circleID.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	out := make([]TimerOverride, 0, len(rows))
	for _, row := range rows {
		view, convErr := s.toOverride(ctx, row)
		if convErr != nil {
			return nil, convErr
		}
		out = append(out, view)
	}
	return out, nil
}

// GetOverride returns one override, or 404 when the circle has none for that target.
func (s *Service) GetOverride(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
) (TimerOverride, error) {
	row, err := s.db.Queries().GetCircleTimerOverride(ctx,
		sqlitegen.GetCircleTimerOverrideParams{
			CircleID: circleID.String(), TargetID: targetID.String(),
		})
	if store.IsNotFound(err) {
		return TimerOverride{}, apierr.New(apierr.CodeNotFound,
			"this circle has no timer override for that target")
	}
	if err != nil {
		return TimerOverride{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return s.toOverride(ctx, row)
}

// PutOverride writes a circle's override for one target.
func (s *Service) PutOverride(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
	actor core.MembershipID, req WindowRequest,
) (TimerOverride, error) {
	v, err := req.validate("body")
	if err != nil {
		return TimerOverride{}, err
	}
	target, err := s.Get(ctx, targetID)
	if err != nil {
		return TimerOverride{}, err
	}

	now := s.clock.Now()
	var row sqlitegen.CircleTimerOverride
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		var txErr error
		row, txErr = q.PutCircleTimerOverride(ctx, sqlitegen.PutCircleTimerOverrideParams{
			CircleID: circleID.String(), TargetID: targetID.String(), WindowKind: v.kind,
			WindowOpenOffsetSeconds: v.openOffset, WindowCloseOffsetSeconds: v.closeOffset,
			FixedGraceSeconds: v.grace, ClusterEpsilonSeconds: v.epsilon,
			Note: req.Note, CreatedByMembershipID: actor.String(),
			CreatedAt: int64(now), UpdatedAt: int64(now),
		})
		if txErr != nil {
			return txErr
		}
		// The audit row goes in the same transaction as the write, so a rollback takes it with it:
		// an audit row that survives a rollback is worse than no row, because it is believed.
		return audit.Append(ctx, q, s.ids, now, audit.Entry{
			CircleID: circleID, Actor: actor, Action: audit.ActionTimerOverrideSet,
			EntityType: audit.EntityRaidTarget, EntityID: targetID.String(),
			Detail: map[string]any{
				"target_name": target.Name, "window_kind": v.kind,
				"window_open_offset_seconds":  v.openOffset,
				"window_close_offset_seconds": v.closeOffset,
			},
		})
	})
	if err != nil {
		if coded, ok := apierr.From(err); ok {
			return TimerOverride{}, coded
		}
		return TimerOverride{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return s.toOverride(ctx, row)
}

// DeleteOverride removes a circle's override, returning what was removed.
//
// What it returns is the override as it stood, once, because a DELETE that answers with nothing
// leaves the caller unable to tell "removed" from "there was nothing there" — and the second is a
// 404 here.
func (s *Service) DeleteOverride(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
	actor core.MembershipID,
) (TimerOverride, error) {
	removed, err := s.GetOverride(ctx, circleID, targetID)
	if err != nil {
		return TimerOverride{}, err
	}
	now := s.clock.Now()
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		rows, txErr := q.DeleteCircleTimerOverride(ctx,
			sqlitegen.DeleteCircleTimerOverrideParams{
				CircleID: circleID.String(), TargetID: targetID.String(),
			})
		if txErr != nil {
			return txErr
		}
		if rows == 0 {
			// Somebody else removed it between the read and the write. One removal is enough.
			return apierr.New(apierr.CodeNotFound,
				"this circle has no timer override for that target")
		}
		return audit.Append(ctx, q, s.ids, now, audit.Entry{
			CircleID: circleID, Actor: actor, Action: audit.ActionTimerOverrideCleared,
			EntityType: audit.EntityRaidTarget, EntityID: targetID.String(),
			Detail: map[string]any{"target_name": removed.TargetName},
		})
	})
	if err != nil {
		if coded, ok := apierr.From(err); ok {
			return TimerOverride{}, coded
		}
		return TimerOverride{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return removed, nil
}

func (s *Service) toOverride(
	ctx context.Context, row sqlitegen.CircleTimerOverride,
) (TimerOverride, error) {
	targetID, err := core.ParseID[core.RaidTarget](row.TargetID)
	if err != nil {
		return TimerOverride{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	actor, err := core.ParseID[core.Membership](row.CreatedByMembershipID)
	if err != nil {
		return TimerOverride{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	target, err := s.Get(ctx, targetID)
	if err != nil {
		return TimerOverride{}, err
	}
	return TimerOverride{
		TargetID: targetID, TargetName: target.Name, WindowKind: row.WindowKind,
		WindowOpenOffsetSeconds:  row.WindowOpenOffsetSeconds,
		WindowCloseOffsetSeconds: row.WindowCloseOffsetSeconds,
		FixedGraceSeconds:        row.FixedGraceSeconds,
		ClusterEpsilonSeconds:    row.ClusterEpsilonSeconds,
		Note:                     row.Note,
		CreatedByMembershipID:    actor,
		CreatedAt:                core.Micros(row.CreatedAt),
		UpdatedAt:                core.Micros(row.UpdatedAt),
	}, nil
}
