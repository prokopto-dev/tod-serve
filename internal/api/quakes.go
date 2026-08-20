package api

import (
	"context"
	"errors"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

type reportQuakeInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	Body     struct {
		OccurredAt core.Micros `json:"occurred_at,omitempty" doc:"Game truth, may be backdated. Defaults to now"`
		Source     string      `json:"source,omitempty" doc:"Where it came from. Defaults to manual" enum:"log_line,manual,api,import"`
		Note       string      `json:"note,omitempty" doc:"Free text for the officers who read this later" maxLength:"500"`
	}
}

type reportQuakeOutput struct{ Body QuakeResponse }

type listQuakesInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	Cursor   string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor"`
	Limit    int    `query:"limit" doc:"Page size, 1-200" minimum:"0" maximum:"200"`
}

type listQuakesOutput struct{ Body Page[tod.Quake] }

type listCircleAuditInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	Cursor   string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor"`
	Limit    int    `query:"limit" doc:"Page size, 1-200" minimum:"0" maximum:"200"`
}

type listCircleAuditOutput struct{ Body Page[audit.Record] }

// registerQuakes attaches the quake log and the audit read side.
//
// They share a file because they share a shape: both are append-only logs a client reads and
// nobody edits, and both are one write operation and one paged read. There is no delete on either,
// anywhere, at any permission level.
func (s *Server) registerQuakes() error {
	return errors.Join(
		registerFailure(OpReportQuake, Register(s.api, OpReportQuake,
			func(ctx context.Context, in *reportQuakeInput) (*reportQuakeOutput, error) {
				circleID, p, err := circlePrincipal(ctx, in.CircleID)
				if err != nil {
					return nil, err
				}
				quake, err := s.cfg.Tods.ReportQuake(ctx, tod.ReportQuakeRequest{
					CircleID: circleID, Reporter: p.MembershipID,
					OccurredAt: in.Body.OccurredAt, Source: in.Body.Source, Note: in.Body.Note,
				})
				if err != nil {
					return nil, err
				}
				return &reportQuakeOutput{
					Body: QuakeResponse{Quake: quake, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpListQuakes, Register(s.api, OpListQuakes,
			func(ctx context.Context, in *listQuakesInput) (*listQuakesOutput, error) {
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
				quakes, hasMore, err := s.cfg.Tods.ListQuakes(ctx, circleID, in.Cursor, limit)
				if err != nil {
					return nil, err
				}
				next := ""
				if len(quakes) > 0 {
					next = quakes[len(quakes)-1].ID.String()
				}
				return &listQuakesOutput{
					Body: NewPage(quakes, next, hasMore, s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpListCircleAudit, Register(s.api, OpListCircleAudit,
			func(ctx context.Context, in *listCircleAuditInput) (*listCircleAuditOutput, error) {
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
				records, hasMore, err := audit.List(
					ctx, s.cfg.Store.Queries(), circleID, in.Cursor, limit)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				next := ""
				if len(records) > 0 {
					next = records[len(records)-1].ID.String()
				}
				return &listCircleAuditOutput{
					Body: NewPage(records, next, hasMore, s.cfg.Clock.Now()),
				}, nil
			})),
	)
}
