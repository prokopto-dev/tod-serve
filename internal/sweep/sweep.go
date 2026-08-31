package sweep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// Grace is how long a row stays after it expired before the sweep takes it.
//
// See the package comment for why it is not zero: a `credential_ticket` the server can still see
// answers `auth_ticket_expired`, and one it cannot answers `auth_ticket_invalid`. A day is far
// longer than any client retries a 120-second ticket and far shorter than unbounded.
const Grace = 24 * time.Hour

// SweptMessage is the log message every run emits, once per run.
//
// A constant because it is what an operator greps for and what a log-based alert matches on. A
// message assembled at the call site stops matching the day somebody rewords it, and nobody edits
// the alert.
const SweptMessage = "swept expired rows"

// Config is what a [Service] needs.
type Config struct {
	Store *store.DB
	Clock clock.Clock
	Log   *slog.Logger
}

// Service removes expired rows from the four prunable tables.
type Service struct {
	db    *store.DB
	clock clock.Clock
	log   *slog.Logger
}

// New returns a service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("sweep service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("sweep service: no clock")
	case cfg.Log == nil:
		return nil, errors.New("sweep service: no logger")
	}
	return &Service{db: cfg.Store, clock: cfg.Clock, log: cfg.Log}, nil
}

// Report is what one run removed, per table.
//
// The counts are the point, not a nicety: a sweep that quietly removed rows is the thing the house
// rule against hiding a row silently forbids. Each table is named separately because "deleted 4000
// rows" does not distinguish a normal night from an OAuth callback that stopped completing and left
// ten thousand flows behind.
type Report struct {
	AuthFlows          int64 `json:"auth_flows"`
	CredentialTickets  int64 `json:"credential_tickets"`
	IdempotencyRecords int64 `json:"idempotency_records"`
	// SessionRevocations is how many signed-out sessions were forgotten. A revocation stops
	// meaning anything once the session it names has expired, because the codec refuses an expired
	// cookie without consulting anything.
	SessionRevocations int64 `json:"session_revocations"`
	// Before is the cutoff used: rows with `expires_at` strictly before it were removed.
	Before core.Micros `json:"before"`
	AsOf   core.Micros `json:"as_of"`
}

// Total is how many rows the run removed across all four tables.
func (r Report) Total() int64 {
	return r.AuthFlows + r.CredentialTickets + r.IdempotencyRecords + r.SessionRevocations
}

// Sweep deletes every row that expired more than [Grace] ago and reports what it took.
//
// The four deletes share one transaction so the returned counts and the database cannot disagree:
// on an error nothing is removed and the report is empty, rather than the sweep having deleted rows
// it then failed to tell anybody about. That is cheap here — these are four indexed deletes
// against litter, not a table walk.
//
// It is safe to run at any time and safe to run concurrently with the server. Every reader of these
// tables already refuses a row past `expires_at`, so this removes rows that are dead to the
// application either way; the request path deletes an expired `idempotency_record` on sight, and
// this is the same deletion done on a schedule instead of on a collision.
func (s *Service) Sweep(ctx context.Context) (Report, error) {
	now := s.clock.Now()
	before := now.Add(-Grace)

	report := Report{Before: before, AsOf: now}
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		flows, err := q.DeleteExpiredAuthFlows(ctx, int64(before))
		if err != nil {
			return fmt.Errorf("delete expired auth flows: %w", err)
		}
		tickets, err := q.DeleteExpiredCredentialTickets(ctx, int64(before))
		if err != nil {
			return fmt.Errorf("delete expired credential tickets: %w", err)
		}
		records, err := q.DeleteExpiredIdempotencyRecords(ctx, int64(before))
		if err != nil {
			return fmt.Errorf("delete expired idempotency records: %w", err)
		}
		revocations, err := q.DeleteExpiredSessionRevocations(ctx, int64(before))
		if err != nil {
			return fmt.Errorf("delete expired session revocations: %w", err)
		}
		report.AuthFlows, report.CredentialTickets, report.IdempotencyRecords = flows, tickets, records
		report.SessionRevocations = revocations
		return nil
	})
	if err != nil {
		return Report{}, err
	}

	// Logged on every run, including the one that took nothing. A sweep only ever visible when it
	// deletes something is a sweep whose silence is ambiguous between "nothing to do" and "not
	// running for three weeks", and the second is the one worth noticing.
	s.log.LogAttrs(ctx, slog.LevelInfo, SweptMessage,
		slog.Int64("auth_flows", report.AuthFlows),
		slog.Int64("credential_tickets", report.CredentialTickets),
		slog.Int64("idempotency_records", report.IdempotencyRecords),
		slog.Int64("session_revocations", report.SessionRevocations),
		slog.Int64("total", report.Total()),
		slog.String("before", before.String()),
	)
	return report, nil
}
