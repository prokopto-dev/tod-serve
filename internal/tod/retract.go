package tod

import (
	"context"
	"log/slog"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// RetractRequest is `retractTodReport`.
type RetractRequest struct {
	CircleID core.CircleID
	ReportID core.TodReportID
	// Actor is the membership retracting. `tod.retract` covers their own reports; retracting
	// somebody else's needs `tod.retract.any`.
	Actor core.MembershipID
	// CanRetractAny is whether the actor holds `tod.retract.any`, resolved from the principal by
	// the caller. It is passed in rather than read here because effective capability is
	// `role permissions ∩ token scopes`, and only the edge holds both halves.
	CanRetractAny bool
	Reason        string
}

// Retracted is the retraction row and the report it names.
//
// Both, because the original **stays visible**: a client that was shown only the retraction would
// have to fetch the report again to render what was withdrawn, and a client shown only the report
// would not know who withdrew it or when.
type Retracted struct {
	Retraction Report `json:"retraction"`
	Original   Report `json:"original"`
}

// Retract appends a retraction row. It never touches the original.
//
// A retraction of a retraction is not supported. The unique index `ux_tod_report_retracts` makes a
// second retraction of one report unrepresentable, and a retraction naming a retraction is refused
// here — posting a fresh kill report is the way to say "the original was right after all", because
// an undo of an undo is a state nobody can read off the log.
func (s *Service) Retract(ctx context.Context, req RetractRequest) (Retracted, error) {
	original, err := s.Get(ctx, req.CircleID, req.ReportID)
	if err != nil {
		return Retracted{}, err
	}
	if original.Kind != schemaenum.TodReportKindKill {
		return Retracted{}, apierr.New(apierr.CodeAlreadyRetracted,
			"that row is itself a retraction; a retraction of a retraction is not supported — "+
				"post a fresh report instead")
	}
	if original.Retracted {
		return Retracted{}, apierr.New(apierr.CodeAlreadyRetracted,
			"that report has already been retracted")
	}
	if original.Reporter != req.Actor && !req.CanRetractAny {
		// A within-circle permission failure, so 403 and not 404: the wrong tenant never reaches
		// this handler at all.
		return Retracted{}, apierr.Newf(apierr.CodeRetractNotPermitted,
			"that report is somebody else's; retracting it needs %s",
			authz.PermissionTodRetractAny)
	}

	now := s.clock.Now()
	id, err := core.NewID[core.TodReport](s.ids, now)
	if err != nil {
		return Retracted{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	retracts := req.ReportID.String()
	params := sqlitegen.CreateTodReportParams{
		ID: id.String(), CircleID: req.CircleID.String(),
		TargetID: original.TargetID.String(),
		Kind:     schemaenum.TodReportKindRetraction,
		// A retraction is an act, not an observation: it carries the original's `died_at` so the
		// row is well-formed and the CHECK against `reported_at` holds, and the derivation reads
		// only `retracts_report_id` from it. Inventing a fresh `died_at` here would put a time in
		// the log that nothing ever observed.
		DiedAt: int64(original.DiedAt), ReportedAt: int64(now),
		ReporterMembershipID: req.Actor.String(),
		Source:               schemaenum.TodReportSourceAPI,
		SelfConfidence:       schemaenum.TodReportSelfConfidenceCertain,
		SourceLine:           optional(req.Reason),
		RetractsReportID:     &retracts,
	}

	var row sqlitegen.TodReport
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		created, txErr := q.CreateTodReport(ctx, params)
		if txErr != nil {
			return txErr
		}
		row = created
		return invalidate(ctx, q, req.CircleID, original.TargetID)
	})
	if store.IsUniqueViolation(err) {
		// `ux_tod_report_retracts` got there first: two officers retracted the same report at
		// once. It is done, which is what the code says.
		return Retracted{}, apierr.Wrap(apierr.CodeAlreadyRetracted, err,
			"that report has already been retracted")
	}
	if err != nil {
		return Retracted{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	s.log.InfoContext(ctx, "tod report retracted",
		slog.String("circle_id", req.CircleID.String()),
		slog.String("report_id", req.ReportID.String()),
		slog.String("retraction_id", id.String()),
		slog.String("actor_membership_id", req.Actor.String()))

	retraction, err := s.view(ctx, req.CircleID, row, false)
	if err != nil {
		return Retracted{}, err
	}
	original.Retracted = true
	return Retracted{Retraction: retraction, Original: original}, nil
}
