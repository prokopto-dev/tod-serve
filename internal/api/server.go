package api

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// Title and DocumentVersion name the API in the generated document. The version is the API's, not
// the binary's: within v1 the surface is additive only, so it moves when the base path does.
const (
	Title           = "tod-serve"
	DocumentVersion = "1.0.0"
)

// MetricsConfig is the metrics listener. It is disabled by default and never gated by a PAT scope
// — canonical §13.
type MetricsConfig struct {
	// Enabled mirrors `TOD_METRICS_ENABLED`, which defaults to false. A metrics endpoint that is
	// on by default is an information leak nobody chose.
	Enabled bool
	// Token mirrors `TOD_METRICS_TOKEN`. It is compared in constant time and is required whenever
	// the listener is enabled: an enabled endpoint with no token is refused at construction rather
	// than served openly.
	Token core.Secret
}

// Config is everything the API needs. Nothing here has a default: a component that silently
// invents a clock or a logger behaves differently in a test than in production, and the difference
// is discovered in production.
type Config struct {
	// Version is the binary's version, reported by `/meta` and `/healthz`.
	Version string
	// Store is the database. `/healthz` never touches it.
	Store *store.DB
	// Auth resolves credentials into principals.
	Auth *auth.Authenticator
	// Sessions signs the `__Host-tod_session` cookie `/join` and `/sessions` set.
	//
	// It is separate from Auth, which only ever READS a session. Minting one is a different
	// capability and lives on exactly the two operations that have just verified a credential:
	// the capability floor is session-only, so those two are the only doors into it, and a
	// console that could not open one could not revoke a member, revoke an invite, read the audit
	// log or configure a provider.
	Sessions *auth.SessionCodec
	// Circles, Members, Invites and Identities are the domain services the handlers call.
	//
	// They are required rather than optional, and a nil one is refused at construction: an API
	// that started with half its services wired would answer some routes and 500 on the rest,
	// which is a worse failure than not starting — the operator finds out at the first request
	// instead of at boot.
	Circles    *circle.Service
	Members    *membership.Service
	Invites    *invite.Service
	Identities *identity.Service
	// Catalogue owns raid-target identity, the per-server timers and the per-circle overrides.
	Catalogue *catalogue.Service
	// Tods appends to the report log; States derives and caches the board over it. They are two
	// services rather than one because they sit on opposite sides of the invariant: one may only
	// ever append, and the other holds nothing but a cache it is allowed to throw away.
	Tods   *tod.Service
	States *projection.Service
	// Invalidator is told when a route moved a respawn window. Required, and nil is a
	// construction error like every other dependency here: an API that started with a missing
	// invalidator would serve a board that silently stopped tracking timer edits, which is worse
	// than not starting.
	Invalidator TimerInvalidator
	// Clock is the only reader of the wall clock.
	Clock clock.Clock
	// Log is where problems go. Nothing secret is ever written to it.
	Log *slog.Logger
	// IDs mints ULIDs for request ids and idempotency records.
	IDs *core.Generator
	// Metrics configures the separate metrics listener.
	Metrics MetricsConfig
	// InviteRateLimit is the ONE bucket every public route that accepts an invite code draws on.
	// Its zero value is the default.
	InviteRateLimit RateLimit
	// OnResponseViolation installs the response validator and receives every response that breaks
	// the contract canonical §7 states. The integration suite sets it to something that fails the
	// test; production leaves it nil, where the same rules are held by the one place that renders
	// a problem rather than by a checker in the hot path.
	OnResponseViolation func(Violation)
}

func (c Config) validate() error {
	switch {
	case c.Store == nil:
		return errors.New("api config: store is nil")
	case c.Auth == nil:
		return errors.New("api config: authenticator is nil")
	case c.Sessions == nil:
		return errors.New("api config: session codec is nil")
	case c.Circles == nil:
		return errors.New("api config: circle service is nil")
	case c.Members == nil:
		return errors.New("api config: membership service is nil")
	case c.Invites == nil:
		return errors.New("api config: invite service is nil")
	case c.Identities == nil:
		return errors.New("api config: identity service is nil")
	case c.Catalogue == nil:
		return errors.New("api config: catalogue service is nil")
	case c.Tods == nil:
		return errors.New("api config: tod service is nil")
	case c.States == nil:
		return errors.New("api config: projection service is nil")
	case c.Invalidator == nil:
		return errors.New("api config: timer invalidator is nil")
	case c.Clock == nil:
		return errors.New("api config: clock is nil")
	case c.Log == nil:
		return errors.New("api config: logger is nil")
	case c.IDs == nil:
		return errors.New("api config: id generator is nil")
	case c.Metrics.Enabled && c.Metrics.Token.IsZero():
		// Refused rather than served openly. An operator who turned metrics on and forgot the
		// token must find out at startup, not from whoever scraped it.
		return errors.New("api config: metrics are enabled with no TOD_METRICS_TOKEN")
	}
	return nil
}

// Builder collects handlers onto one router. It exists so that [Register] can be a generic
// function — Go has no generic methods — while still holding the registration state.
type Builder struct {
	cfg        Config
	mux        *http.ServeMux
	api        huma.API
	registered map[OperationID]bool
	order      []OperationID
	metrics    *metrics
	// served are the media types THIS listener produces, for the `Accept` check. The API and the
	// metrics listener produce different things, and sharing one list refused every scraper.
	served []string
	// invites is the SHARED invite-code bucket. It is one limiter passed to every builder rather
	// than one per builder: two buckets is two guessing budgets, which is the whole failure this
	// limiter exists to prevent.
	invites *limiter
}

// API returns the underlying framework API, for the spec generator and for tests.
func (b *Builder) API() huma.API { return b.api }

// OpenAPI returns the document as it stands.
func (b *Builder) OpenAPI() *huma.OpenAPI { return b.api.OpenAPI() }

// Registered returns the operations that have a handler, in registration order.
//
// It is the honest answer to "what does this binary serve": hidden operations never reach the
// OpenAPI document's `paths`, so walking the document would quietly miss `/healthz` and the OAuth
// callback.
func (b *Builder) Registered() []OperationID { return append([]OperationID(nil), b.order...) }

// newBuilder wires a router and an API over it.
func newBuilder(
	cfg Config, metrics *metrics, invites *limiter, served []string, docs bool,
) *Builder {
	mux := http.NewServeMux()
	config := huma.DefaultConfig(Title, DocumentVersion)
	config.OpenAPIPath = ""
	config.SchemasPath = ""
	config.DocsPath = ""
	// The default configuration adds a transformer that injects a `$schema` member into every
	// response body and a `Link` header beside it. Neither is wanted: canonical §7 says pagination
	// is in the body envelope and never in a `Link` header, and a member nobody asked for in every
	// response is a member some client will start depending on.
	config.CreateHooks = nil
	registerSchemaAliases(config.Components.Schemas)
	if docs {
		config.Servers = []*huma.Server{{URL: BasePath}}
		config.Components.SecuritySchemes = securitySchemes()
	}
	return &Builder{
		cfg:        cfg,
		mux:        mux,
		api:        humago.New(mux, config),
		registered: map[OperationID]bool{},
		metrics:    metrics,
		invites:    invites,
		served:     served,
	}
}

// securitySchemes declares how a caller authenticates. There are exactly three, and the absence of
// a fourth is the point: `Authorization: Bearer` and the session cookie are the only API
// credentials, and the metrics token is not an API credential at all.
func securitySchemes() map[string]*huma.SecurityScheme {
	return map[string]*huma.SecurityScheme{
		SchemeBearer: {
			Type:        "http",
			Scheme:      "bearer",
			Description: "A personal access token, `tods_pat_…`. Never in a query string.",
		},
		SchemeSession: {
			Type:        "apiKey",
			In:          "cookie",
			Name:        auth.SessionCookie,
			Description: "A browser session. The only credential that reaches the capability floor.",
		},
		SchemeMetricsToken: {
			Type:        "http",
			Scheme:      "bearer",
			Description: "TOD_METRICS_TOKEN, on the separate metrics listener. Never a PAT scope.",
		},
	}
}

// handler wraps the router in the middleware every request passes through, outermost first.
func (b *Builder) handler() http.Handler {
	var h http.Handler = b.mux
	h = withAcceptableFormat(h, b.served, b.writeRawProblem)
	h = withConditionalGet(h)
	h = withFrameworkProblems(h, b.writeRawProblem)
	h = withIdempotencyCapture(h)
	h = withBufferedBody(h, MaxBodyBytes, b.writeRawProblem)
	if b.cfg.OnResponseViolation != nil {
		h = validateResponses(h, b.cfg.OnResponseViolation)
	}
	h = withSecurityHeaders(h)
	h = withRequestID(h, mintRequestID(b.cfg.IDs, b.cfg.Clock.Now))
	return h
}

// writeRawProblem renders a problem from outside the framework, for the middleware that runs
// before a route has been matched and therefore has no framework context to write through.
func (b *Builder) writeRawProblem(w http.ResponseWriter, err error) {
	e, ok := apierr.From(err)
	if !ok {
		e = apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	if id := w.Header().Get(RequestIDHeader); id != "" {
		e = e.WithRequestID(id)
	}
	body, marshalErr := e.MarshalJSON()
	if marshalErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for name, values := range e.GetHeaders() {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	w.Header().Set("Content-Type", apierr.ContentType)
	w.WriteHeader(e.GetStatus())
	// Deliberate waiver: this is the error path, and there is no second response to send.
	_, _ = w.Write(body)
}

// Server is the wired API: the routes this milestone owns, on the router they are served from, and
// the separate metrics listener beside it.
type Server struct {
	api     *Builder
	metrics *Builder
	cfg     Config
	counts  *metrics
	invites *limiter
}

// New wires the API.
//
// Every route it serves goes through [Register], so the registry is not a description of the
// surface — it IS the surface, and there is no second path a handler could take to reach a URL.
func New(cfg Config) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	counts := newMetrics(cfg.Version, cfg.Clock.Now())
	invites := newLimiter(cfg.InviteRateLimit)
	s := &Server{
		cfg:     cfg,
		counts:  counts,
		invites: invites,
		api:     newBuilder(cfg, counts, invites, apiMediaTypes(), true),
		metrics: newBuilder(cfg, counts, invites, metricsMediaTypes(), false),
	}
	if err := s.registerAll(); err != nil {
		return nil, err
	}
	return s, nil
}

// registerAll attaches every handler this package owns.
//
// Routes owned by other milestones are absent from here and present in [Routes]: the registry is
// the whole surface, and what is served is a subset of it that grows. `make status` and
// [Server.Unimplemented] are how that gap stays visible rather than being discovered by a client.
func (s *Server) registerAll() error {
	return errors.Join(
		s.registerMeta(),
		s.registerPrincipal(),
		s.registerTokens(),
		s.registerCircles(),
		s.registerMembers(),
		s.registerInvites(),
		s.registerCatalogue(),
		s.registerAdmin(),
		s.registerAuth(),
		s.registerJoin(),
		s.registerTods(),
		s.registerQuakes(),
		s.registerHealth(),
		s.registerMetrics(),
	)
}

// Handler returns the API router. It does NOT serve `/metrics`, which is on its own listener.
func (s *Server) Handler() http.Handler { return s.api.handler() }

// MetricsHandler returns the metrics router, and false when metrics are disabled.
//
// It is a separate handler because it belongs on a separate listener: a metrics endpoint reachable
// on the public port is one an operator has to remember to firewall, and canonical §13 does not
// leave that to memory.
func (s *Server) MetricsHandler() (http.Handler, bool) {
	if !s.cfg.Metrics.Enabled {
		return nil, false
	}
	return s.metrics.handler(), true
}

// OpenAPI returns the generated document.
func (s *Server) OpenAPI() *huma.OpenAPI { return s.api.OpenAPI() }

// Registered returns every operation this binary serves, across both listeners.
func (s *Server) Registered() []OperationID {
	return append(s.api.Registered(), s.metrics.Registered()...)
}

// Unimplemented returns the operations in the registry that no handler serves yet.
//
// It exists so the gap is enumerable. A registry that silently described routes nobody had written
// would make every architectural test over it look more complete than it is.
func (s *Server) Unimplemented() []OperationID {
	served := map[OperationID]bool{}
	for _, id := range s.Registered() {
		served[id] = true
	}
	var out []OperationID
	for _, r := range Routes() {
		if !served[r.ID] {
			out = append(out, r.ID)
		}
	}
	return out
}

// NewIDGenerator returns the ULID generator the API uses, over the system's randomness.
func NewIDGenerator() *core.Generator { return core.NewGenerator(rand.Reader) }

// checkMetricsToken compares the presented bearer against `TOD_METRICS_TOKEN`.
//
// The comparison is constant-time, which matters here more than almost anywhere else: a metrics
// endpoint is scraped on a timer, so an attacker gets an unlimited number of identically-shaped
// requests to measure.
func (b *Builder) checkMetricsToken(ctx huma.Context) error {
	presented, ok := cutBearer(ctx.Header("Authorization"))
	if !ok {
		return apierr.New(apierr.CodeUnauthenticated,
			"the metrics endpoint needs Authorization: Bearer <TOD_METRICS_TOKEN>")
	}
	if !b.cfg.Metrics.Token.Equal(core.Secret(presented)) {
		return apierr.New(apierr.CodeUnauthenticated, "the metrics token is not valid")
	}
	return nil
}

func cutBearer(header string) (string, bool) {
	if len(header) <= len(auth.BearerScheme) {
		return "", false
	}
	if header[:len(auth.BearerScheme)] != auth.BearerScheme {
		return "", false
	}
	return trimHeader(header[len(auth.BearerScheme):]), true
}

// registerFailure gives a wiring error the operation it came from, so a failure at startup names
// the route rather than only the rule it broke.
func registerFailure(id OperationID, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("wire %s: %w", id, err)
}
