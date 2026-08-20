package tod

import (
	"context"
	"log/slog"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// MaxQuakeNoteLen bounds the note an officer leaves on a quake.
const MaxQuakeNoteLen = 500

// Quake is one server-wide repop, as a client reads it.
//
// An earthquake is ONE event, not sixty kills nobody witnessed. Modelling it as sixty reports
// would corrupt every confidence figure on the board for a week, and wrong *confidently* is the
// failure mode this project is built against.
type Quake struct {
	ID core.QuakeEventID `json:"id"`
	// OccurredAt is game truth and may be backdated — somebody reports the quake an hour later.
	OccurredAt core.Micros `json:"occurred_at"`
	// ReportedAt is system truth and never is.
	ReportedAt core.Micros       `json:"reported_at"`
	Reporter   core.MembershipID `json:"reported_by_membership_id"`
	// ReporterRevoked is the same rule the report log carries: revocation controls access, never
	// history.
	ReporterRevoked bool   `json:"reporter_revoked"`
	Source          string `json:"source"`
	Note            string `json:"note,omitempty"`
}

// ReportQuakeRequest is `reportQuake`.
type ReportQuakeRequest struct {
	CircleID core.CircleID
	Reporter core.MembershipID
	// OccurredAt defaults to now. A quake is usually reported as it happens, and the common case
	// should not need a timestamp the client has to format.
	OccurredAt core.Micros
	Source     string
	Note       string
}

// ReportQuake appends a quake and invalidates the whole circle's cached state.
//
// The invalidation is circle-wide because the event is: a quake repops every raid target on the
// server at once, so every target's answer changes even though only one row was written. It is
// `tod.quake.report` rather than `tod.report` for the same reason — a false quake wipes the whole
// board, and that is an officer's mistake to be able to make, not a member's.
func (s *Service) ReportQuake(ctx context.Context, req ReportQuakeRequest) (Quake, error) {
	if _, err := s.circle(ctx, req.CircleID); err != nil {
		return Quake{}, err
	}
	now := s.clock.Now()
	occurred := req.OccurredAt
	if occurred.IsZero() {
		occurred = now
	}
	// The same rule and the same tolerance a ToD carries, and for the same reason: a quake in the
	// future is impossible independent of any derivation. The schema says so too.
	if occurred > now.Add(FutureTolerance) {
		return Quake{}, apierr.Newf(apierr.CodeDiedAtInFuture,
			"occurred_at is %s and now is %s; the clock-skew tolerance is %s",
			occurred, now, FutureTolerance).
			WithField("body.occurred_at", "is in the future")
	}
	if occurred < now.Add(-MaxBackdate) {
		return Quake{}, apierr.Newf(apierr.CodeDiedAtTooOld,
			"occurred_at is %s, more than %d days ago", occurred, int(MaxBackdate.Hours()/24)).
			WithField("body.occurred_at", "is more than 90 days old")
	}
	source, err := validSource(req.Source)
	if err != nil {
		return Quake{}, err
	}
	if len(req.Note) > MaxQuakeNoteLen {
		return Quake{}, apierr.Newf(apierr.CodeValidationFailed,
			"note is %d bytes; the maximum is %d", len(req.Note), MaxQuakeNoteLen).
			WithField("body.note", "too long")
	}

	id, err := core.NewID[core.QuakeEvent](s.ids, now)
	if err != nil {
		return Quake{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	var row sqlitegen.QuakeEvent
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		created, txErr := q.CreateQuakeEvent(ctx, sqlitegen.CreateQuakeEventParams{
			ID: id.String(), CircleID: req.CircleID.String(),
			OccurredAt: int64(occurred), ReportedAt: int64(now),
			ReportedByMembershipID: req.Reporter.String(), Source: source, Note: req.Note,
		})
		if txErr != nil {
			return txErr
		}
		row = created
		if _, txErr = q.InvalidateCircleTargetStates(ctx, req.CircleID.String()); txErr != nil {
			return txErr
		}
		return nil
	})
	if err != nil {
		return Quake{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	s.log.WarnContext(ctx, "quake reported",
		slog.String("circle_id", req.CircleID.String()),
		slog.String("quake_id", id.String()),
		slog.String("reported_by_membership_id", req.Reporter.String()))

	revoked, err := s.revokedReporters(ctx, req.CircleID)
	if err != nil {
		return Quake{}, err
	}
	quake, err := toQuake(row, revoked)
	if err != nil {
		return Quake{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return quake, nil
}

// ListQuakes returns a page of the quake log, newest first, and whether another page follows.
func (s *Service) ListQuakes(
	ctx context.Context, circleID core.CircleID, cursor string, limit int,
) ([]Quake, bool, error) {
	rows, err := s.db.Queries().ListQuakeEventsPage(ctx, sqlitegen.ListQuakeEventsPageParams{
		CircleID: circleID.String(), AfterID: cursor, RowLimit: int64(limit) + 1,
	})
	if err != nil {
		return nil, false, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	revoked, err := s.revokedReporters(ctx, circleID)
	if err != nil {
		return nil, false, err
	}
	quakes := make([]Quake, 0, len(rows))
	for _, row := range rows {
		quake, convErr := toQuake(row, revoked)
		if convErr != nil {
			return nil, false, apierr.Wrap(apierr.CodeInternalError, convErr, "")
		}
		quakes = append(quakes, quake)
	}
	return quakes, hasMore, nil
}

// LatestQuake returns the circle's truncation point: the quake with the greatest `occurred_at`.
//
// One row rather than the whole log, because that is all the derivation reads — §2 takes the
// latest quake and moves everything before it to history — and because `occurred_at` is game truth
// and may be backdated, so "latest" is a question the database answers with an index rather than
// one the caller answers by scanning.
func (s *Service) LatestQuake(
	ctx context.Context, circleID core.CircleID,
) ([]consensus.Quake, error) {
	row, err := s.db.Queries().GetLatestQuakeEvent(ctx, circleID.String())
	if store.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	id, err := core.ParseID[core.QuakeEvent](row.ID)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return []consensus.Quake{{ID: id, OccurredAt: core.Micros(row.OccurredAt)}}, nil
}

func toQuake(row sqlitegen.QuakeEvent, revoked map[string]bool) (Quake, error) {
	id, err := core.ParseID[core.QuakeEvent](row.ID)
	if err != nil {
		return Quake{}, err
	}
	reporter, err := core.ParseID[core.Membership](row.ReportedByMembershipID)
	if err != nil {
		return Quake{}, err
	}
	return Quake{
		ID: id, OccurredAt: core.Micros(row.OccurredAt),
		ReportedAt: core.Micros(row.ReportedAt), Reporter: reporter,
		ReporterRevoked: revoked[row.ReportedByMembershipID],
		Source:          row.Source, Note: row.Note,
	}, nil
}
