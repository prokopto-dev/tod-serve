package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// SpecPath is where the generated document is checked in, relative to the repository root.
//
// It is checked in rather than served-and-forgotten because the thing that breaks a client is a
// change nobody saw: `oasdiff` compares this file against the base branch's copy, and a renamed
// `operationId` fails as a breaking change even though the HTTP surface is unchanged.
const SpecPath = "openapi/openapi.json"

// specClockInstant is the instant the documentation server's clock reads. The document must be
// byte-identical on every run — `make gen` writes it and a test compares it — so the generator
// cannot be allowed a real clock. Nothing in the document depends on the value.
const specClockInstant = core.Micros(0)

// SpecJSON renders the OpenAPI document for the operations this binary serves.
//
// The document is generated from the handlers, which are registered from the route registry, which
// is compared against docs/design/02-api-design.md. There is therefore no copy of the API surface
// that a person maintains: the document, the middleware and the design table are one fact seen from
// three sides.
func SpecJSON() ([]byte, error) {
	s, err := newDocumentServer()
	if err != nil {
		return nil, err
	}
	raw, err := s.OpenAPI().MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal openapi document: %w", err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		return nil, fmt.Errorf("indent openapi document: %w", err)
	}
	// A trailing newline, because every other text file in this repository has one and a diff that
	// says "\ No newline at end of file" on a generated file is noise nobody can act on.
	indented.WriteByte('\n')
	return indented.Bytes(), nil
}

// newDocumentServer builds a server for the sole purpose of describing itself.
//
// It has no store and a fixed clock. Registration never calls a handler, so the nil store is
// unreachable rather than merely unused — and giving the generator a real database would make
// `make gen` depend on one, which is how a generated file starts differing between machines.
func newDocumentServer() (*Server, error) {
	cfg := Config{
		Version: "0.0.0",
		Clock:   clock.NewTest(specClockInstant),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		IDs:     NewIDGenerator(),
	}
	counts := newMetrics(cfg.Version, specClockInstant)
	invites := newLimiter(cfg.InviteRateLimit)
	s := &Server{
		cfg:     cfg,
		counts:  counts,
		invites: invites,
		api:     newBuilder(cfg, counts, invites, true),
		metrics: newBuilder(cfg, counts, invites, false),
	}
	if err := s.registerAll(); err != nil {
		return nil, err
	}
	return s, nil
}
