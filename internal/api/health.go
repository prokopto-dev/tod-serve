package api

import (
	"context"
	"errors"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
)

// Liveness is what `/healthz` answers.
type Liveness struct {
	// Status is always `ok`: reaching this handler at all is the whole check.
	Status string `json:"status" example:"ok"`
	// Version is the running build.
	Version string `json:"version"`
	// AsOf is the instant the answer was computed. Every response here carries one.
	AsOf core.Micros `json:"as_of"`
}

// Readiness is what `/readyz` answers.
type Readiness struct {
	// Status is `ready`, or the failure is a 503 and there is no body.
	Status string `json:"status" example:"ready"`
	// SchemaVersion is the migration the database is at.
	SchemaVersion int64 `json:"schema_version"`
	// AsOf is the instant the answer was computed.
	AsOf core.Micros `json:"as_of"`
}

type livenessInput struct{}

type livenessOutput struct{ Body Liveness }

type readinessInput struct{}

type readinessOutput struct{ Body Readiness }

// registerHealth attaches the two operational endpoints.
//
// `/healthz` deliberately does NOT touch the database, and this is the file where that is easy to
// break: the store is in scope, and adding one reachability check here would look like an
// improvement. It is not. A container `HEALTHCHECK` that touches the database lets Docker kill the
// container mid-migration, which is how a half-migrated database happens.
// TestLiveness_MakesNoDatabaseCall drives it against a CLOSED store and asserts it still answers.
func (s *Server) registerHealth() error {
	return errors.Join(
		registerFailure(OpGetLiveness, Register(s.api, OpGetLiveness,
			func(ctx context.Context, _ *livenessInput) (*livenessOutput, error) {
				return &livenessOutput{Body: Liveness{
					Status:  "ok",
					Version: s.cfg.Version,
					AsOf:    s.cfg.Clock.Now(),
				}}, nil
			})),

		registerFailure(OpGetReadiness, Register(s.api, OpGetReadiness,
			func(ctx context.Context, _ *readinessInput) (*readinessOutput, error) {
				if err := s.cfg.Store.Ready(ctx); err != nil {
					// The detail names which half failed — unreachable, or behind on migrations —
					// because a deploy gate that only knows "not ready" tells nobody what to do.
					detail := "the database is not reachable"
					if errors.Is(err, store.ErrSchemaBehind) {
						detail = "the database is behind the migrations this binary embeds"
					}
					return nil, apierr.Wrap(apierr.CodeServiceUnavailable, err, detail)
				}
				version, err := s.cfg.Store.SchemaVersion(ctx)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeServiceUnavailable, err,
						"the database is not reachable")
				}
				return &readinessOutput{Body: Readiness{
					Status:        "ready",
					SchemaVersion: version,
					AsOf:          s.cfg.Clock.Now(),
				}}, nil
			})),
	)
}
