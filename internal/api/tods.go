package api

import (
	"context"
	"errors"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// TodReportResponse is one report, and the instant it was read.
//
// `as_of` is on the response rather than on the view for the same reason it is everywhere else:
// it is a property of the answer and not of the row, and it is what every countdown in this API is
// read against instead of the caller's own clock.
type TodReportResponse struct {
	tod.Report
	AsOf core.Micros `json:"as_of"`
}

// RetractionResponse carries the new retraction row AND the report it names, because the original
// stays visible: retracting is an append, and a client that saw only one half would have to guess
// at the other.
type RetractionResponse struct {
	tod.Retracted
	AsOf core.Micros `json:"as_of"`
}

// QuakeResponse is one server-wide repop, and the instant it was read.
type QuakeResponse struct {
	tod.Quake
	AsOf core.Micros `json:"as_of"`
}

// TargetStateResponse is one target's derived state.
type TargetStateResponse struct {
	projection.Derived
	AsOf core.Micros `json:"as_of"`
}

type createTodReportInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	Body     struct {
		TargetID                 string      `json:"target_id,omitempty" doc:"The target. Exactly one of target_id and target_name"`
		TargetName               string      `json:"target_name,omitempty" doc:"A parsed or hand-typed name; runs the resolve ladder" maxLength:"120"`
		Server                   string      `json:"server" doc:"Must be the circle's server. A mismatch is 422 server_mismatch" enum:"blue,green,red"`
		DiedAt                   core.Micros `json:"died_at" doc:"Game truth. May be backdated; may not be in the future beyond 120s of skew"`
		Source                   string      `json:"source,omitempty" doc:"Where the time came from. Defaults to manual" enum:"log_line,manual,api,import"`
		SelfConfidence           string      `json:"self_confidence,omitempty" doc:"How sure the reporter is. Defaults to certain" enum:"certain,probable,guess"`
		SourceLine               string      `json:"source_line,omitempty" doc:"The raw log line, verbatim" maxLength:"1024"`
		SourceCharacter          string      `json:"source_character,omitempty" doc:"The character named in the line" maxLength:"64"`
		LogCharacter             string      `json:"log_character,omitempty" doc:"Whose log file it came from" maxLength:"64"`
		KilledByGuild            string      `json:"killed_by_guild,omitempty" doc:"Self-asserted; the intel officers actually want" maxLength:"120"`
		ClientClockOffsetSeconds *int64      `json:"client_clock_offset_seconds,omitempty" doc:"The plugin's own skew estimate"`
	}
}

type createTodReportOutput struct {
	// Replayed marks a report that already existed: the same reporter, target and `died_at`. It
	// is the natural key answering behind a botched `Idempotency-Key`, and it is a REPLAY rather
	// than a conflict — the client asked for a row to exist and it does.
	Replayed string `header:"Idempotency-Replayed"`
	Body     TodReportResponse
}

type listTodReportsInput struct {
	CircleID         string `path:"circle_id" doc:"The circle"`
	Cursor           string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor"`
	Limit            int    `query:"limit" doc:"Page size, 1-200" minimum:"0" maximum:"200"`
	TargetID         string `query:"target_id" doc:"Only reports about this target"`
	DiedAfter        string `query:"died_after" doc:"Only reports whose died_at is at or after this instant"`
	DiedBefore       string `query:"died_before" doc:"Only reports whose died_at is at or before this instant"`
	Reporter         string `query:"reporter_membership_id" doc:"Only this member's reports"`
	IncludeRetracted bool   `query:"include_retracted" doc:"Bring back retracted kills and the retractions naming them"`
}

type listTodReportsOutput struct{ Body Page[tod.Report] }

type getTodReportInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	ReportID string `path:"report_id" doc:"The report"`
}

type getTodReportOutput struct{ Body TodReportResponse }

type retractTodReportInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	ReportID string `path:"report_id" doc:"The report to retract"`
	Body     struct {
		Reason string `json:"reason,omitempty" doc:"Why, kept on the retraction row" maxLength:"500"`
	}
}

type retractTodReportOutput struct{ Body RetractionResponse }

type listTargetStatesInput struct {
	CircleID    string `path:"circle_id" doc:"The circle"`
	IfNoneMatch string `header:"If-None-Match" doc:"Revalidate a cached copy; a match answers 304"`
	Cursor      string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor"`
	Limit       int    `query:"limit" doc:"Page size, 1-200" minimum:"0" maximum:"200"`
	Status      string `query:"status" doc:"Only targets in this state" enum:"unknown,no_timer,pre_window,in_window,overdue,up"`
	Expansion   string `query:"expansion" doc:"Only targets from this expansion" enum:"classic,kunark,velious"`
	Zone        string `query:"zone" doc:"Only targets in this zone; matched normalised"`
	// Contested is a string rather than a *bool because a tri-state query parameter has to be
	// distinguishable from an absent one, and "not contested" is a real thing to filter for.
	Contested string `query:"contested" doc:"Only contested targets, or only uncontested ones" enum:"true,false"`
	Query     string `query:"q" doc:"Substring of a target's name or one of its aliases, matched normalised"`
}

type listTargetStatesOutput struct {
	ETag string `header:"ETag"`
	Body Page[projection.BoardEntry]
}

type getTargetStateInput struct {
	CircleID    string `path:"circle_id" doc:"The circle"`
	TargetID    string `path:"target_id" doc:"The raid target"`
	IfNoneMatch string `header:"If-None-Match" doc:"Revalidate a cached copy; a match answers 304"`
}

type getTargetStateOutput struct {
	ETag string `header:"ETag"`
	Body TargetStateResponse
}

// registerTods attaches the report log and the board.
//
// There is no update-report operation here and there will not be one: `tod_report` is append-only
// by database trigger, corrections are new rows, and `report_immutable` exists as an error code
// for a client that goes looking for the operation anyway.
func (s *Server) registerTods() error {
	return errors.Join(
		registerFailure(OpCreateTodReport, Register(s.api, OpCreateTodReport,
			func(ctx context.Context, in *createTodReportInput) (*createTodReportOutput, error) {
				circleID, p, err := circlePrincipal(ctx, in.CircleID)
				if err != nil {
					return nil, err
				}
				created, err := s.cfg.Tods.Create(ctx, tod.CreateRequest{
					CircleID: circleID, Reporter: p.MembershipID,
					TargetID: in.Body.TargetID, TargetName: in.Body.TargetName,
					Server: in.Body.Server, DiedAt: in.Body.DiedAt,
					Source: in.Body.Source, SelfConfidence: in.Body.SelfConfidence,
					SourceLine: in.Body.SourceLine, SourceCharacter: in.Body.SourceCharacter,
					LogCharacter: in.Body.LogCharacter, KilledByGuild: in.Body.KilledByGuild,
					ClientClockOffsetSeconds: in.Body.ClientClockOffsetSeconds,
				})
				if err != nil {
					return nil, err
				}
				out := &createTodReportOutput{
					Body: TodReportResponse{Report: created.Report, AsOf: s.cfg.Clock.Now()},
				}
				if created.Replayed {
					out.Replayed = "true"
				}
				return out, nil
			})),

		registerFailure(OpListTodReports, Register(s.api, OpListTodReports,
			func(ctx context.Context, in *listTodReportsInput) (*listTodReportsOutput, error) {
				circleID, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				limit, err := NormaliseLimit(in.Limit)
				if err != nil {
					return nil, err
				}
				if _, err := ParseCursor(in.Cursor); err != nil {
					return nil, err
				}
				req := tod.ListRequest{
					CircleID: circleID, Cursor: in.Cursor, Limit: limit,
					IncludeRetracted: in.IncludeRetracted,
				}
				if req.TargetID, err = optionalID[core.RaidTarget](
					in.TargetID, "query.target_id"); err != nil {
					return nil, err
				}
				if req.Reporter, err = optionalID[core.Membership](
					in.Reporter, "query.reporter_membership_id"); err != nil {
					return nil, err
				}
				if req.DiedAfter, err = optionalMicros(in.DiedAfter, "query.died_after"); err != nil {
					return nil, err
				}
				if req.DiedBefore, err = optionalMicros(
					in.DiedBefore, "query.died_before"); err != nil {
					return nil, err
				}

				reports, hasMore, err := s.cfg.Tods.List(ctx, req)
				if err != nil {
					return nil, err
				}
				next := ""
				if len(reports) > 0 {
					next = reports[len(reports)-1].ID.String()
				}
				return &listTodReportsOutput{
					Body: NewPage(reports, next, hasMore, s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpGetTodReport, Register(s.api, OpGetTodReport,
			func(ctx context.Context, in *getTodReportInput) (*getTodReportOutput, error) {
				circleID, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				reportID, err := parseReportID(in.ReportID)
				if err != nil {
					return nil, err
				}
				report, err := s.cfg.Tods.Get(ctx, circleID, reportID)
				if err != nil {
					return nil, err
				}
				return &getTodReportOutput{
					Body: TodReportResponse{Report: report, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpRetractTodReport, Register(s.api, OpRetractTodReport,
			func(ctx context.Context, in *retractTodReportInput) (*retractTodReportOutput, error) {
				circleID, p, err := circlePrincipal(ctx, in.CircleID)
				if err != nil {
					return nil, err
				}
				reportID, err := parseReportID(in.ReportID)
				if err != nil {
					return nil, err
				}
				retracted, err := s.cfg.Tods.Retract(ctx, tod.RetractRequest{
					CircleID: circleID, ReportID: reportID, Actor: p.MembershipID,
					// Effective capability is `role permissions ∩ token scopes`, and this is the
					// only place both halves are in hand. The service is told the answer rather
					// than the principal, so it cannot accidentally consult only one of them.
					CanRetractAny: p.Can(authz.PermissionTodRetractAny),
					Reason:        in.Body.Reason,
				})
				if err != nil {
					return nil, err
				}
				return &retractTodReportOutput{
					Body: RetractionResponse{Retracted: retracted, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpListTargetStates, Register(s.api, OpListTargetStates,
			func(ctx context.Context, in *listTargetStatesInput) (*listTargetStatesOutput, error) {
				circleID, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				limit, err := NormaliseLimit(in.Limit)
				if err != nil {
					return nil, err
				}
				cursor, err := optionalID[core.RaidTarget](in.Cursor, "query.cursor")
				if err != nil {
					return nil, err
				}
				contested, err := optionalBool(in.Contested, "query.contested")
				if err != nil {
					return nil, err
				}
				filter := projection.BoardFilter{
					Status: in.Status, Expansion: in.Expansion, Zone: in.Zone,
					Query: in.Query, Contested: contested, Limit: limit,
				}
				if cursor != nil {
					filter.Cursor = *cursor
				}
				entries, hasMore, err := s.cfg.States.Board(ctx, circleID, filter)
				if err != nil {
					return nil, err
				}
				next := ""
				if len(entries) > 0 {
					next = entries[len(entries)-1].Target.ID.String()
				}
				page := NewPage(entries, next, hasMore, s.cfg.Clock.Now())
				// Everything the page says except `as_of` — `next_cursor` and `has_more`
				// included. See [ETagOfPage].
				etag, err := ETagOfPage(page)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &listTargetStatesOutput{ETag: etag, Body: page}, nil
			})),

		registerFailure(OpGetTargetState, Register(s.api, OpGetTargetState,
			func(ctx context.Context, in *getTargetStateInput) (*getTargetStateOutput, error) {
				circleID, p, err := circlePrincipal(ctx, in.CircleID)
				if err != nil {
					return nil, err
				}
				targetID, err := core.ParseID[core.RaidTarget](in.TargetID)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeNotFound, err, "no such raid target")
				}
				// `reporters[]` only for a principal that holds attribution. That separation IS
				// the observer role: a board can be shared with an allied guild without handing
				// over the identity of the trackers behind it.
				derived, err := s.cfg.States.Get(ctx, circleID, targetID,
					p.Can(authz.PermissionTodReadAttribution))
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(derived)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &getTargetStateOutput{
					ETag: etag,
					Body: TargetStateResponse{Derived: derived, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),
	)
}

// circlePrincipal reads the circle from the path and the caller from the context.
//
// The two always travel together on a circle-scoped write, and the middleware has already refused
// a request where they disagree — this is the handler-side half of that, checked anyway because
// "unreachable" is a claim about the middleware that a handler cannot verify.
func circlePrincipal(ctx context.Context, raw string) (core.CircleID, auth.Principal, error) {
	circleID, err := parseCircleID(raw)
	if err != nil {
		return core.CircleID{}, auth.Principal{}, err
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return core.CircleID{}, auth.Principal{},
			apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
	}
	return circleID, p, nil
}

// parseReportID reads a report id out of a path. A malformed one answers 404 for the same reason a
// malformed circle id does: the shape of a guess must not tell a prober whether the id existed.
func parseReportID(raw string) (core.TodReportID, error) {
	id, err := core.ParseID[core.TodReport](raw)
	if err != nil {
		return core.TodReportID{}, apierr.Wrap(apierr.CodeNotFound, err, "no such report")
	}
	return id, nil
}

// optionalID parses a filter that names an id, and answers `422` rather than `404` when it is not
// one: a bad FILTER is a bad request, where a bad path segment is a resource that is not there.
func optionalID[E core.Entity](raw, location string) (*core.ID[E], error) {
	if raw == "" {
		return nil, nil
	}
	id, err := core.ParseID[E](raw)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeValidationFailed, err, "not a valid identifier").
			WithField(location, "not a valid identifier")
	}
	return &id, nil
}

// optionalMicros parses a timestamp filter. The wire format is the one canonical §1 fixes: RFC 3339
// with microsecond precision, always Z.
func optionalMicros(raw, location string) (*core.Micros, error) {
	if raw == "" {
		return nil, nil
	}
	at, err := core.ParseMicros(raw)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeValidationFailed, err,
			"not an RFC 3339 timestamp with microsecond precision").
			WithField(location, "not a timestamp")
	}
	return &at, nil
}

// optionalBool parses a tri-state query flag: absent, `true` or `false`.
//
// Anything else is refused rather than read as false. A filter that silently ignored `contested=1`
// would answer with the whole board and look like it had filtered, which is the shape of bug this
// codebase spends its gates on.
func optionalBool(raw, location string) (*bool, error) {
	switch raw {
	case "":
		return nil, nil
	case "true":
		yes := true
		return &yes, nil
	case "false":
		no := false
		return &no, nil
	default:
		return nil, apierr.Newf(apierr.CodeValidationFailed,
			"%s must be true or false", location).WithField(location, "must be true or false")
	}
}
