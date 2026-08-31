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
	"github.com/prokopto-dev/tod-serve/internal/instancesettings"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/setup"
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

// SetupConfig is first-run setup: the token that authorises it, and nothing else.
//
// The token is the ONLY thing between a fresh public instance and whoever loads the page first, so
// this type is where "unset" and "wrong" are made into the same answer. ADR-0016.
type SetupConfig struct {
	// Token mirrors `TOD_SETUP_TOKEN`. Empty means first-run setup is unreachable: the operator
	// has not armed it, or has disarmed it since, and there is no default and no unauthenticated
	// mode.
	Token core.Secret
}

// authorises reports whether the presented value reaches first-run setup.
//
// **The comparison runs before the configured-at-all check, and that order is the mechanism.**
// `core.Secret.Equal` is `subtle.ConstantTimeCompare`, and both refusals leave through the same
// return — so an instance with no `TOD_SETUP_TOKEN` set and one with a wrong token guessed do the
// same work and answer the same way. Telling those two apart would tell a stranger which hosts are
// worth guessing at.
//
// The empty-token case is checked rather than left to the comparison, because an unset token
// matching an empty presented value is the failure this whole route is a takeover surface for.
func (c SetupConfig) authorises(presented string) bool {
	match := c.Token.Equal(core.Secret(presented))
	return !c.Token.IsZero() && match
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
	// InstanceSettings reads and changes the instance-wide policy switches, and is the only
	// writer of the ledger that records those changes. Required, like every other service here:
	// an API that started without it would answer `getInstanceSettings` with a 500 on an instance
	// whose administrator is trying to find out what their own instance is configured to do.
	InstanceSettings *instancesettings.Service
	// Catalogue owns raid-target identity, the per-server timers and the per-circle overrides.
	Catalogue *catalogue.Service
	// Tods appends to the report log; States derives and caches the board over it. They are two
	// services rather than one because they sit on opposite sides of the invariant: one may only
	// ever append, and the other holds nothing but a cache it is allowed to throw away.
	Tods   *tod.Service
	States *projection.Service
	// Invalidator is handed to the write that moved a respawn window, and enlisted in that
	// write's own transaction — it is not called from here. This layer holds it because this
	// layer is where the wiring hands it in, and because a ROUTE is one of the two things that
	// can move a window; the other is `tod-serve seed timers`, which hands the same port to
	// [catalogue.Service.ApplySeed].
	//
	// Required, and nil is a construction error like every other dependency here: an API that
	// started with a missing invalidator would serve a board that silently stopped tracking timer
	// edits, which is worse than not starting. [catalogue.TimerInvalidator] refuses a nil one at
	// the write as well, because construction is not the only way in.
	Invalidator catalogue.TimerInvalidator
	// Clock is the only reader of the wall clock.
	Clock clock.Clock
	// Log is where problems go. Nothing secret is ever written to it.
	Log *slog.Logger
	// IDs mints ULIDs for request ids and idempotency records.
	IDs *core.Generator
	// Metrics configures the separate metrics listener.
	Metrics MetricsConfig
	// Setup runs first-run setup, and is required: an API wired without it would answer the
	// wizard's routes with a 500 on a database where nothing else can answer at all.
	Setup *setup.Service
	// SetupToken configures what authorises those routes. Its ZERO VALUE is the safe one — an
	// unset `TOD_SETUP_TOKEN` means setup is unreachable — which is why it is a value rather than
	// a pointer and why [Config.validate] does not require it.
	SetupToken SetupConfig
	// InviteRateLimit is the ONE bucket every public route that accepts an invite code draws on.
	// Its zero value is the default.
	InviteRateLimit RateLimit
	// Console is the embedded admin console, from `internal/ui`. Optional: a binary built with
	// no web assets serves the API alone and says so at startup, rather than serving a blank
	// page that looks like a broken console.
	//
	// It is a plain [http.Handler] rather than the package, so `internal/api` does not import
	// `internal/ui` and the console can be swapped for a stub in a test.
	Console http.Handler
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
	case c.InstanceSettings == nil:
		return errors.New("api config: instance settings service is nil")
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
	case c.Setup == nil:
		return errors.New("api config: setup service is nil")
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

// securitySchemes declares how a caller authenticates. There are exactly four, and the shape of the
// list is the point: `Authorization: Bearer` and the session cookie are the only API credentials,
// and the other two are environment variables that reach one operational surface each and no
// domain route at all.
func securitySchemes() map[string]*huma.SecurityScheme {
	// The OpenAPI spellings, named because three of the four schemes share them and a typo in one
	// would publish a scheme a generated client cannot use.
	const (
		httpScheme   = "http"
		bearerScheme = "bearer"
	)
	return map[string]*huma.SecurityScheme{
		SchemeBearer: {
			Type:        httpScheme,
			Scheme:      bearerScheme,
			Description: "A personal access token, `tods_pat_…`. Never in a query string.",
		},
		SchemeSession: {
			Type:        "apiKey",
			In:          "cookie",
			Name:        auth.SessionCookie,
			Description: "A browser session. The only credential that reaches the capability floor.",
		},
		SchemeMetricsToken: {
			Type:        httpScheme,
			Scheme:      bearerScheme,
			Description: "TOD_METRICS_TOKEN, on the separate metrics listener. Never a PAT scope.",
		},
		SchemeSetupToken: {
			Type:   httpScheme,
			Scheme: bearerScheme,
			Description: "TOD_SETUP_TOKEN, for first-run setup only. Never a PAT scope, and it " +
				"stops working the moment this instance has an administrator.",
		},
	}
}

// checkSetupToken is the whole authorisation of first-run setup.
//
// Every refusal here is ONE answer — `404 not_found` with the same detail — because two of them
// have to be indistinguishable and the third is easiest to keep that way by not branching at all.
// A missing header, a malformed one, a wrong token and an instance with `TOD_SETUP_TOKEN` unset
// are the same sentence to a caller who does not hold the token, which is the only caller this
// message is written for.
func (b *Builder) checkSetupToken(ctx huma.Context) error {
	presented, _ := cutBearer(ctx.Header("Authorization"))
	if b.cfg.SetupToken.authorises(presented) {
		return nil
	}
	return apierr.New(apierr.CodeNotFound, "no such operation")
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
		s.registerSetup(),
		s.registerPrincipal(),
		s.registerTokens(),
		s.registerCircles(),
		s.registerMembers(),
		s.registerInvites(),
		s.registerCatalogue(),
		s.registerInstance(),
		s.registerAdmin(),
		s.registerAuth(),
		s.registerJoin(),
		s.registerSignOut(),
		s.registerTods(),
		s.registerQuakes(),
		s.registerHealth(),
		s.registerMetrics(),
	)
}

// Handler returns everything this listener serves: the API, and the console behind it when one
// was built in. It does NOT serve `/metrics`, which is on its own listener.
func (s *Server) Handler() http.Handler {
	api := s.api.handler()
	if s.cfg.Console == nil {
		return api
	}
	return withConsole(api, s.cfg.Console)
}

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
