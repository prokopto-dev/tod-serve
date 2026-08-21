package tod

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// FutureTolerance is the clock skew a `died_at` is allowed to be ahead by.
//
// It is the schema's `CHECK (died_at <= reported_at + 120000000)` spelled in Go, so a caller meets
// the same number with a readable error rather than a constraint violation. Both exist: this one
// is the message, the CHECK is the guarantee.
const FutureTolerance = 120 * time.Second

// MaxBackdate is how far back a `died_at` may be. Backdating is normal — game truth routinely lags
// system truth by hours — but past ninety days it is almost always a timezone or epoch bug, and a
// ToD that old is not intel for any raid target.
const MaxBackdate = 90 * 24 * time.Hour

// MaxSourceLineLen bounds `source_line`. A log line is one line of a game log; anything longer is
// a paste of the whole file, and the column is stored verbatim so a client can see what was parsed.
const MaxSourceLineLen = 1024

// Config is what a [Service] needs. Every field is required: a service that invented its own clock
// would behave differently in a test than in production, and the difference is found in production.
type Config struct {
	Store *store.DB
	Clock clock.Clock
	IDs   *core.Generator
	// Catalogue resolves a `target_name` through the ladder and a `target_id` by id. It is the
	// concrete service rather than a port: the ladder, its ranking rule and its problems belong to
	// `internal/catalogue`, and a port here would be an invitation to grow a second one.
	Catalogue *catalogue.Service
	Log       *slog.Logger
}

// Service appends to the report log.
type Service struct {
	db        *store.DB
	clock     clock.Clock
	ids       *core.Generator
	catalogue *catalogue.Service
	log       *slog.Logger
}

// New returns a service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("tod service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("tod service: no clock")
	case cfg.IDs == nil:
		return nil, errors.New("tod service: no id generator")
	case cfg.Catalogue == nil:
		return nil, errors.New("tod service: no catalogue")
	case cfg.Log == nil:
		return nil, errors.New("tod service: no logger")
	}
	return &Service{
		db: cfg.Store, clock: cfg.Clock, ids: cfg.IDs,
		catalogue: cfg.Catalogue, log: cfg.Log,
	}, nil
}

// Report is one row of the log, as a client reads it.
//
// It carries both timestamps, always, because a client that saw only one of them would eventually
// treat it as the other: `died_at` is game truth and may be hours behind, `reported_at` is system
// truth and never moves.
type Report struct {
	ID       core.TodReportID  `json:"id"`
	TargetID core.RaidTargetID `json:"target_id"`
	// Kind is `kill` or `retraction`. A retraction is a row of this log like any other.
	Kind       string      `json:"kind"`
	DiedAt     core.Micros `json:"died_at"`
	ReportedAt core.Micros `json:"reported_at"`
	// Reporter names the membership even after it is revoked: a revoked member's reports still
	// count and their retractions still apply, and hiding the name would be revocation quietly
	// rewriting history.
	Reporter core.MembershipID `json:"reporter_membership_id"`
	// ReporterRevoked is the revocation rule made visible rather than acted on.
	ReporterRevoked          bool              `json:"reporter_revoked"`
	Source                   string            `json:"source"`
	SelfConfidence           string            `json:"self_confidence"`
	SourceLine               string            `json:"source_line,omitempty"`
	SourceCharacter          string            `json:"source_character,omitempty"`
	LogCharacter             string            `json:"log_character,omitempty"`
	KilledByGuild            string            `json:"killed_by_guild,omitempty"`
	ClientClockOffsetSeconds *int64            `json:"client_clock_offset_seconds"`
	RetractsReportID         *core.TodReportID `json:"retracts_report_id"`
	// Retracted says a retraction row names this report. The report itself is untouched and stays
	// visible; this is how a client renders the strikethrough without inferring it from a second
	// query.
	Retracted bool `json:"retracted"`
}

// CreateRequest is `createTodReport`.
type CreateRequest struct {
	CircleID core.CircleID
	Reporter core.MembershipID
	// TargetID and TargetName are exclusive: exactly one is required. TargetName runs the resolve
	// ladder so the plugin can send the name it parsed and never has to hold a catalogue.
	TargetID   string
	TargetName string
	// Server must match the circle's. It is echoed by the client rather than inferred so that the
	// real fan-out failure — playing Blue with the Green destination ticked — is caught instead of
	// landing silently in the wrong board.
	Server                   string
	DiedAt                   core.Micros
	Source                   string
	SelfConfidence           string
	SourceLine               string
	SourceCharacter          string
	LogCharacter             string
	KilledByGuild            string
	ClientClockOffsetSeconds *int64
}

// Created is a report and whether the write was a replay of one already in the log.
//
// The natural key `ux_tod_report_natural` is a second line of defence behind `Idempotency-Key`:
// the same reporter cannot lodge the same kill twice even with a botched header. A duplicate is a
// **replay, not an error** — the client asked for a row to exist and it does.
type Created struct {
	Report   Report
	Replayed bool
}

// Create appends one kill report.
//
// The order of the checks is the rule. Tenancy is already settled by the time this runs, so what
// is left is: the target must exist, the server must be the circle's, and the time must be
// possible. A physically *implausible* time — one before the current cluster's window opened — is
// none of those things: it is accepted, stored, and flagged by the derivation, because derived
// state must never veto an observation.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Created, error) {
	circle, err := s.circle(ctx, req.CircleID)
	if err != nil {
		return Created{}, err
	}
	target, err := s.resolveTarget(ctx, req.TargetID, req.TargetName)
	if err != nil {
		return Created{}, err
	}
	if err := checkServer(req.Server, circle.Server); err != nil {
		return Created{}, err
	}
	now := s.clock.Now()
	if err := checkDiedAt(req.DiedAt, now); err != nil {
		return Created{}, err
	}
	source, err := validSource(req.Source)
	if err != nil {
		return Created{}, err
	}
	confidence, err := validSelfConfidence(req.SelfConfidence)
	if err != nil {
		return Created{}, err
	}
	if len(req.SourceLine) > MaxSourceLineLen {
		return Created{}, apierr.Newf(apierr.CodeValidationFailed,
			"source_line is %d bytes; the maximum is %d", len(req.SourceLine), MaxSourceLineLen).
			WithField("body.source_line", "too long")
	}

	// The natural-key read comes first so a botched `Idempotency-Key` replays rather than meeting a
	// constraint violation. It races: two identical requests can both miss it, and the unique index
	// is what settles that — the insert below reads the row back on a collision.
	if existing, found, lookupErr := s.byNaturalKey(ctx, req.CircleID, target.ID,
		req.Reporter, req.DiedAt); lookupErr != nil {
		return Created{}, lookupErr
	} else if found {
		return Created{Report: existing, Replayed: true}, nil
	}

	id, err := core.NewID[core.TodReport](s.ids, now)
	if err != nil {
		return Created{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	params := sqlitegen.CreateTodReportParams{
		ID: id.String(), CircleID: req.CircleID.String(), TargetID: target.ID.String(),
		Kind: schemaenum.TodReportKindKill, DiedAt: int64(req.DiedAt), ReportedAt: int64(now),
		ReporterMembershipID: req.Reporter.String(), Source: source, SelfConfidence: confidence,
		SourceLine: optional(req.SourceLine), SourceCharacter: optional(req.SourceCharacter),
		LogCharacter: optional(req.LogCharacter), KilledByGuild: optional(req.KilledByGuild),
		ClientClockOffsetSeconds: req.ClientClockOffsetSeconds,
	}

	var row sqlitegen.TodReport
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		created, txErr := q.CreateTodReport(ctx, params)
		if txErr != nil {
			return txErr
		}
		row = created
		// Invalidation happens in the SAME transaction as the append. A cache cleared afterwards
		// is a cache that survives a rollback of the write it was cleared for, which is the one
		// direction that leaves a board claiming a kill the log does not have.
		return invalidate(ctx, q, req.CircleID, target.ID)
	})
	if store.IsUniqueViolation(err) {
		// Two identical requests raced the read above. The index is the arbiter and the loser
		// replays, exactly as a botched header does.
		existing, found, lookupErr := s.byNaturalKey(ctx, req.CircleID, target.ID,
			req.Reporter, req.DiedAt)
		if lookupErr != nil {
			return Created{}, lookupErr
		}
		if found {
			return Created{Report: existing, Replayed: true}, nil
		}
		return Created{}, apierr.Wrap(apierr.CodeConflict, err, "that report already exists")
	}
	if err != nil {
		return Created{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	s.log.InfoContext(ctx, "tod report appended",
		slog.String("circle_id", req.CircleID.String()),
		slog.String("target_id", target.ID.String()),
		slog.String("report_id", id.String()),
		slog.String("source", source))

	report, err := s.view(ctx, req.CircleID, row, false)
	if err != nil {
		return Created{}, err
	}
	return Created{Report: report}, nil
}

// resolveTarget hands the caller's reference to the catalogue and returns its problem unchanged.
//
// Everything about which target a name means — the ladder, the rule that an exact hit is never
// ranked below a substring hit, the `422 ambiguous_target` that carries `meta.candidates[]` — is
// `internal/catalogue`'s, and its errors are already `*apierr.Error`. Re-deciding any of it here
// would be a second ladder for the plugin to disagree with, which is the whole reason the plugin
// holds no catalogue of its own.
//
// The one thing this function owns is a `target_id` that is not an id at all. It answers
// `422 unknown_target` on the same field, because from the client's side an id that names nothing
// and a string that is not an id have exactly the same fix.
func (s *Service) resolveTarget(ctx context.Context, rawID, name string) (catalogue.Target, error) {
	ref := catalogue.Ref{Name: name}
	if rawID != "" {
		id, err := core.ParseID[core.RaidTarget](rawID)
		if err != nil {
			return catalogue.Target{}, apierr.Wrap(apierr.CodeUnknownTarget, err,
				"no raid target with that id").
				WithField("body.target_id", "no such target")
		}
		ref.ID = id
	}
	// Both set and neither set are the catalogue's to refuse: `Ref` has exactly that fourth state
	// and one place decides it, so the two callers of this ladder cannot word it differently.
	resolved, err := s.catalogue.Resolve(ctx, ref)
	if err != nil {
		return catalogue.Target{}, err
	}
	return resolved.Target, nil
}

// checkServer is the guard against the real fan-out failure: a client playing Blue with the Green
// destination ticked. A circle is pinned to one server immutably and there is no combined view
// anywhere, so accepting the mismatch would be a wrong answer rather than a lenient one.
func checkServer(sent, circleServer string) error {
	if sent == "" {
		return apierr.New(apierr.CodeValidationFailed,
			"server is required, and must be the circle's").
			WithField("body.server", "required")
	}
	if sent != circleServer {
		return apierr.Newf(apierr.CodeServerMismatch,
			"this circle is pinned to %s and the report says %s", circleServer, sent).
			WithField("body.server", "does not match the circle's server")
	}
	return nil
}

// checkDiedAt applies the two hard rejections on a time, and only those two.
//
// A death in the future is impossible independent of any derivation, which is what makes it the
// one hard rejection; ninety days is the point past which a backdate is almost always a timezone
// bug rather than a real backfill. Everything else about a `died_at` — including one that cannot
// be true alongside the current cluster — is the derivation's to flag, not this function's to
// refuse.
func checkDiedAt(diedAt, now core.Micros) error {
	if diedAt.IsZero() {
		return apierr.New(apierr.CodeValidationFailed, "died_at is required").
			WithField("body.died_at", "required")
	}
	if diedAt > now.Add(FutureTolerance) {
		return apierr.Newf(apierr.CodeDiedAtInFuture,
			"died_at is %s and now is %s; the clock-skew tolerance is %s",
			diedAt, now, FutureTolerance).
			WithField("body.died_at", "is in the future")
	}
	if diedAt < now.Add(-MaxBackdate) {
		return apierr.Newf(apierr.CodeDiedAtTooOld,
			"died_at is %s, more than %d days ago", diedAt, int(MaxBackdate.Hours()/24)).
			WithField("body.died_at", "is more than 90 days old")
	}
	return nil
}

// enumHolds asks the enum catalogue rather than a local list. The catalogue generates the SQL
// CHECK and the OpenAPI enum from the same constants, so a value this accepts is a value the
// column accepts and a value the document publishes; a hand-written list here would be a third
// copy of one fact.
func enumHolds(name, value string) bool {
	e, ok := schemaenum.Lookup(name)
	return ok && e.Contains(value)
}

func validSource(source string) (string, error) {
	if source == "" {
		// `manual` is the honest default: a request that did not say where its time came from did
		// not come from a log line, and treating it as one would let it estimate alone under
		// consensus §5.
		return schemaenum.TodReportSourceManual, nil
	}
	if !enumHolds(schemaenum.NameTodReportSource, source) {
		return "", apierr.Newf(apierr.CodeValidationFailed, "source %q is not a report source",
			source).WithField("body.source", "not a report source")
	}
	return source, nil
}

func validSelfConfidence(value string) (string, error) {
	if value == "" {
		return schemaenum.TodReportSelfConfidenceCertain, nil
	}
	if !enumHolds(schemaenum.NameTodReportSelfConfidence, value) {
		return "", apierr.Newf(apierr.CodeValidationFailed,
			"self_confidence %q is not a confidence", value).
			WithField("body.self_confidence", "not a self-reported confidence")
	}
	return value, nil
}

// Get returns one report.
func (s *Service) Get(
	ctx context.Context, circleID core.CircleID, id core.TodReportID,
) (Report, error) {
	row, err := s.db.Queries().GetTodReport(ctx, sqlitegen.GetTodReportParams{
		CircleID: circleID.String(), ID: id.String(),
	})
	if store.IsNotFound(err) {
		return Report{}, apierr.New(apierr.CodeNotFound, "no such report")
	}
	if err != nil {
		return Report{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	retracted, err := s.isRetracted(ctx, circleID, id)
	if err != nil {
		return Report{}, err
	}
	return s.view(ctx, circleID, row, retracted)
}

// ListRequest is `listTodReports`.
type ListRequest struct {
	CircleID core.CircleID
	Cursor   string
	Limit    int
	// TargetID, DiedAfter, DiedBefore and Reporter are the filters the API design names. Each is a
	// pointer because "not filtered" and "filtered on the zero value" are different questions.
	TargetID   *core.RaidTargetID
	DiedAfter  *core.Micros
	DiedBefore *core.Micros
	Reporter   *core.MembershipID
	// IncludeRetracted brings back retracted kills and the retraction rows that name them. They
	// travel together on purpose: a retraction pointing at a report the caller cannot see is a
	// dangling reference the client would have to explain.
	IncludeRetracted bool
}

// List returns a page of the log, newest first, and whether another page follows.
func (s *Service) List(ctx context.Context, req ListRequest) ([]Report, bool, error) {
	params := sqlitegen.ListTodReportsParams{
		CircleID: req.CircleID.String(), AfterID: req.Cursor,
		IncludeRetracted: boolToInt(req.IncludeRetracted),
		// One more than asked for, so `has_more` is a fact rather than a guess.
		RowLimit: int64(req.Limit) + 1,
	}
	if req.TargetID != nil {
		params.TargetID = stringPtr(req.TargetID.String())
	}
	if req.DiedAfter != nil {
		params.DiedAfter = int64Ptr(int64(*req.DiedAfter))
	}
	if req.DiedBefore != nil {
		params.DiedBefore = int64Ptr(int64(*req.DiedBefore))
	}
	if req.Reporter != nil {
		params.ReporterMembershipID = stringPtr(req.Reporter.String())
	}

	rows, err := s.db.Queries().ListTodReports(ctx, params)
	if err != nil {
		return nil, false, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	hasMore := len(rows) > req.Limit
	if hasMore {
		rows = rows[:req.Limit]
	}

	revoked, err := s.revokedReporters(ctx, req.CircleID)
	if err != nil {
		return nil, false, err
	}
	reports := make([]Report, 0, len(rows))
	for _, row := range rows {
		report, convErr := toReport(sqlitegen.TodReport{
			ID: row.ID, CircleID: row.CircleID, TargetID: row.TargetID, Kind: row.Kind,
			DiedAt: row.DiedAt, ReportedAt: row.ReportedAt,
			ReporterMembershipID: row.ReporterMembershipID, Source: row.Source,
			SelfConfidence: row.SelfConfidence, SourceLine: row.SourceLine,
			SourceCharacter: row.SourceCharacter, LogCharacter: row.LogCharacter,
			KilledByGuild:            row.KilledByGuild,
			ClientClockOffsetSeconds: row.ClientClockOffsetSeconds,
			RetractsReportID:         row.RetractsReportID,
		}, revoked, row.Retracted)
		if convErr != nil {
			return nil, false, apierr.Wrap(apierr.CodeInternalError, convErr, "")
		}
		reports = append(reports, report)
	}
	return reports, hasMore, nil
}

// ReportsFor returns every report for one target, folded by nothing: retractions are rows here and
// the derivation is what applies them. A store that dropped rows would be deciding what counts as
// evidence, which is exactly the decision that belongs in one pure function.
func (s *Service) ReportsFor(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
) ([]consensus.Report, error) {
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
	return ToConsensusReports(rows, revoked)
}

// ToConsensusReports converts stored rows into the derivation's input.
//
// `revoked` is a set rather than a flag on the row because revocation lives on `membership` and
// must never be denormalised onto the log: the log is append-only, so a member revoked after their
// report was written could never have it corrected — and their reports still count either way.
func ToConsensusReports(
	rows []sqlitegen.TodReport, revoked map[string]bool,
) ([]consensus.Report, error) {
	out := make([]consensus.Report, 0, len(rows))
	for _, row := range rows {
		id, err := core.ParseID[core.TodReport](row.ID)
		if err != nil {
			return nil, fmt.Errorf("parse report id %s: %w", row.ID, err)
		}
		reporter, err := core.ParseID[core.Membership](row.ReporterMembershipID)
		if err != nil {
			return nil, fmt.Errorf("parse reporter id %s: %w", row.ReporterMembershipID, err)
		}
		report := consensus.Report{
			ID: id, Kind: consensus.ReportKind(row.Kind),
			DiedAt: core.Micros(row.DiedAt), ReportedAt: core.Micros(row.ReportedAt),
			ReporterMembershipID: reporter, ReporterRevoked: revoked[row.ReporterMembershipID],
			Source: consensus.Source(row.Source),
		}
		if row.RetractsReportID != nil {
			retracts, parseErr := core.ParseID[core.TodReport](*row.RetractsReportID)
			if parseErr != nil {
				return nil, fmt.Errorf("parse retracted report id %s: %w",
					*row.RetractsReportID, parseErr)
			}
			report.RetractsReportID = &retracts
		}
		out = append(out, report)
	}
	return out, nil
}

// circle reads the tenant's own row: the server the reports must match, and the threshold the
// derivation reads.
func (s *Service) circle(
	ctx context.Context, id core.CircleID,
) (sqlitegen.Circle, error) {
	row, err := s.db.Queries().GetCircle(ctx, id.String())
	if store.IsNotFound(err) {
		return sqlitegen.Circle{}, apierr.New(apierr.CodeNotFound, "no such circle")
	}
	if err != nil {
		return sqlitegen.Circle{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return row, nil
}

func (s *Service) byNaturalKey(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
	reporter core.MembershipID, diedAt core.Micros,
) (Report, bool, error) {
	row, err := s.db.Queries().GetTodReportByNaturalKey(ctx,
		sqlitegen.GetTodReportByNaturalKeyParams{
			CircleID: circleID.String(), TargetID: targetID.String(),
			ReporterMembershipID: reporter.String(), DiedAt: int64(diedAt),
		})
	if store.IsNotFound(err) {
		return Report{}, false, nil
	}
	if err != nil {
		return Report{}, false, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	id, err := core.ParseID[core.TodReport](row.ID)
	if err != nil {
		return Report{}, false, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	retracted, err := s.isRetracted(ctx, circleID, id)
	if err != nil {
		return Report{}, false, err
	}
	view, err := s.view(ctx, circleID, row, retracted)
	if err != nil {
		return Report{}, false, err
	}
	return view, true, nil
}

func (s *Service) isRetracted(
	ctx context.Context, circleID core.CircleID, id core.TodReportID,
) (bool, error) {
	reportID := id.String()
	_, err := s.db.Queries().GetRetractionForReport(ctx, sqlitegen.GetRetractionForReportParams{
		CircleID: circleID.String(), RetractsReportID: &reportID,
	})
	switch {
	case store.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, apierr.Wrap(apierr.CodeInternalError, err, "")
	default:
		return true, nil
	}
}

// revokedReporters is the circle's revoked memberships, as a set.
func (s *Service) revokedReporters(
	ctx context.Context, circleID core.CircleID,
) (map[string]bool, error) {
	rows, err := s.db.Queries().ListMemberships(ctx, circleID.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	revoked := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.RevokedAt != nil {
			revoked[row.ID] = true
		}
	}
	return revoked, nil
}

func (s *Service) view(
	ctx context.Context, circleID core.CircleID, row sqlitegen.TodReport, retracted bool,
) (Report, error) {
	revoked, err := s.revokedReporters(ctx, circleID)
	if err != nil {
		return Report{}, err
	}
	report, err := toReport(row, revoked, retracted)
	if err != nil {
		return Report{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return report, nil
}

func toReport(row sqlitegen.TodReport, revoked map[string]bool, retracted bool) (Report, error) {
	id, err := core.ParseID[core.TodReport](row.ID)
	if err != nil {
		return Report{}, err
	}
	targetID, err := core.ParseID[core.RaidTarget](row.TargetID)
	if err != nil {
		return Report{}, err
	}
	reporter, err := core.ParseID[core.Membership](row.ReporterMembershipID)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		ID: id, TargetID: targetID, Kind: row.Kind,
		DiedAt: core.Micros(row.DiedAt), ReportedAt: core.Micros(row.ReportedAt),
		Reporter: reporter, ReporterRevoked: revoked[row.ReporterMembershipID],
		Source: row.Source, SelfConfidence: row.SelfConfidence,
		SourceLine: deref(row.SourceLine), SourceCharacter: deref(row.SourceCharacter),
		LogCharacter: deref(row.LogCharacter), KilledByGuild: deref(row.KilledByGuild),
		ClientClockOffsetSeconds: row.ClientClockOffsetSeconds,
		Retracted:                retracted,
	}
	if row.RetractsReportID != nil {
		retracts, parseErr := core.ParseID[core.TodReport](*row.RetractsReportID)
		if parseErr != nil {
			return Report{}, parseErr
		}
		report.RetractsReportID = &retracts
	}
	return report, nil
}

// invalidate drops the cached state for one target.
//
// It is a DELETE rather than a flag: a stale row with a "stale" bit is still a row somebody reads
// by accident, and `target_state_cache` is droppable by construction. It takes the caller's query
// set so it runs inside the transaction that appended the row.
func invalidate(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID, targetID core.RaidTargetID,
) error {
	_, err := q.InvalidateTargetState(ctx, sqlitegen.InvalidateTargetStateParams{
		CircleID: circleID.String(), TargetID: targetID.String(),
	})
	if err != nil {
		return fmt.Errorf("invalidate cached state for target %s: %w", targetID, err)
	}
	return nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func stringPtr(s string) *string { return &s }

func int64Ptr(v int64) *int64 { return &v }

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
