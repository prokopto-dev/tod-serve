package api_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/identitysql"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// The fixture's fixed instant. Every time-dependent assertion here is relative to it, so a test
// that fails does not depend on when it ran.
const fixtureNow = core.Micros(1_755_483_247_000_000)

const (
	testPepper     = core.Secret("integration-test-pepper")
	testSessionKey = core.Secret("integration-test-session-key")
	testMetricsTok = core.Secret("integration-test-metrics-token")
)

// harness is a wired server over a real migrated SQLite database in t.TempDir().
//
// There are no mocks of the database anywhere in this file, deliberately: the rules being tested —
// membership state read on every request, tenancy resolved from the membership row, idempotency
// keyed on `(membership, key)` — are rules about rows, and a mock would let every one of them pass
// while the schema said otherwise.
type harness struct {
	t           *testing.T
	server      *api.Server
	store       *store.DB
	clock       *clock.Test
	ids         *core.Generator
	minter      *auth.Minter
	codec       *auth.SessionCodec
	handler     http.Handler
	provider    core.IdentityProviderID
	circles     *circle.Service
	invites     *invite.Service
	members     *membership.Service
	catalogue   *catalogue.Service
	tods        *tod.Service
	states      *projection.Service
	grants      *instancegrant.Service
	invalidator *recordingInvalidator
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := t.Context()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tod.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(ctx))

	clk := clock.NewTest(fixtureNow)
	ids := core.NewGenerator(rand.Reader)
	minter, err := auth.NewMinter(testPepper, rand.Reader)
	require.NoError(t, err)
	codec, err := auth.NewSessionCodec(testSessionKey)
	require.NoError(t, err)
	svc := newServices(t, db, clk, ids, minter, log)
	authn, err := auth.NewAuthenticator(
		db, minter, codec, svc.grants, clk, log, auth.DefaultStepUpWindow)
	require.NoError(t, err)
	// The recorder WRAPS the real projection rather than standing in for it. Both questions are
	// worth answering in this suite: "did the route push the invalidation" needs the record, and
	// "did the board actually stop serving the old window" needs the real thing behind it.
	invalidator := &recordingInvalidator{delegate: svc.states}

	server, err := api.New(api.Config{
		Version:     "0.0.0-test",
		Store:       db,
		Auth:        authn,
		Circles:     svc.circles,
		Members:     svc.members,
		Invites:     svc.invites,
		Identities:  svc.identities,
		Catalogue:   svc.catalogue,
		Tods:        svc.tods,
		States:      svc.states,
		Invalidator: invalidator,
		Clock:       clk,
		Log:         log,
		IDs:         ids,
		Metrics:     api.MetricsConfig{Enabled: true, Token: testMetricsTok},
		// Response validation runs across the WHOLE integration suite: every request any test in
		// this package makes is checked against the response contract, including the ones the
		// framework answers before a handler runs.
		OnResponseViolation: func(v api.Violation) { t.Errorf("response contract: %s", v) },
	})
	require.NoError(t, err)

	return &harness{
		t: t, server: server, store: db, clock: clk, ids: ids,
		minter: minter, codec: codec, handler: server.Handler(),
		circles: svc.circles, invites: svc.invites, members: svc.members,
		catalogue: svc.catalogue, tods: svc.tods, states: svc.states, grants: svc.grants,
		invalidator: invalidator,
	}
}

// wiredServices is the domain half of the harness, built exactly the way `cmd/tod-serve` builds
// it: real services over the real store, with no mock anywhere. The rules under test here are
// rules about rows, and a mock would let every one of them pass while the schema said otherwise.
type wiredServices struct {
	circles    *circle.Service
	invites    *invite.Service
	members    *membership.Service
	identities *identity.Service
	catalogue  *catalogue.Service
	tods       *tod.Service
	states     *projection.Service
	grants     *instancegrant.Service
}

func newServices(
	t *testing.T, db *store.DB, clk clock.Clock, ids *core.Generator,
	minter *auth.Minter, log *slog.Logger,
) wiredServices {
	t.Helper()
	circles, err := circle.New(circle.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	invites, err := invite.New(invite.Config{
		Store: db, Clock: clk, IDs: ids, Entropy: rand.Reader, Log: log,
	})
	require.NoError(t, err)

	identityStore, err := identitysql.New(db.Queries(), clk, invite.HashCode)
	require.NoError(t, err)
	clients, err := identity.NewGuardedClients(clk)
	require.NoError(t, err)
	identities, err := identity.New(identity.Config{
		Store: identityStore, Clients: clients, Clock: clk, IDs: ids,
		Entropy: rand.Reader, SPAJoinURL: "https://tod.example.com/join", Logger: log,
	})
	require.NoError(t, err)

	members, err := membership.New(membership.Config{
		Store: db, Clock: clk, IDs: ids, Minter: minter, Identity: identities,
		Log: log, Entropy: rand.Reader,
	})
	require.NoError(t, err)

	// The catalogue is wired and left EMPTY. That is the default state of this whole suite on
	// purpose: an instance with no targets and no timers is what an operator's VPS is on day one,
	// so every test that does not deliberately seed one is exercising it.
	catalogues, err := catalogue.New(catalogue.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	tods, err := tod.New(tod.Config{
		Store: db, Clock: clk, IDs: ids, Catalogue: catalogues, Log: log,
	})
	require.NoError(t, err)
	states, err := projection.New(projection.Config{
		Store: db, Clock: clk, Catalogue: catalogues, Log: log,
	})
	require.NoError(t, err)

	// The real ledger over the real table. There is no fake: what an instance-realm route answers
	// depends on rows, and a stub that returned a set would test the middleware against itself.
	grants, err := instancegrant.New(instancegrant.Config{
		Store: db, Clock: clk, IDs: ids, Log: log,
	})
	require.NoError(t, err)

	return wiredServices{
		circles: circles, invites: invites, members: members, identities: identities,
		catalogue: catalogues, tods: tods, states: states, grants: grants,
	}
}

// newHarnessWithoutMetrics builds the same server with metrics off, which is the DEFAULT: a
// metrics endpoint that is on unless somebody turns it off is an information leak nobody chose.
func newHarnessWithoutMetrics(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := newServices(t, h.store, h.clock, h.ids, h.minter, log)
	authn, err := auth.NewAuthenticator(
		h.store, h.minter, h.codec, svc.grants, h.clock, log, auth.DefaultStepUpWindow)
	require.NoError(t, err)

	server, err := api.New(api.Config{
		Version:             "0.0.0-test",
		Store:               h.store,
		Auth:                authn,
		Circles:             svc.circles,
		Members:             svc.members,
		Invites:             svc.invites,
		Identities:          svc.identities,
		Catalogue:           svc.catalogue,
		Tods:                svc.tods,
		States:              svc.states,
		Invalidator:         h.invalidator,
		Clock:               h.clock,
		Log:                 log,
		IDs:                 h.ids,
		OnResponseViolation: func(v api.Violation) { t.Errorf("response contract: %s", v) },
	})
	require.NoError(t, err)
	h.server, h.handler = server, server.Handler()
	return h
}

// newID mints a typed id at the fixture's instant.
func newID[E core.Entity](h *harness) core.ID[E] {
	h.t.Helper()
	id, err := core.NewID[E](h.ids, h.clock.Now())
	require.NoError(h.t, err)
	return id
}

// seedInstance writes the singleton row `/meta` reads.
func (h *harness) seedInstance(selfService bool) {
	h.t.Helper()
	flag := int64(0)
	if selfService {
		flag = 1
	}
	_, err := h.store.Queries().CreateInstance(h.t.Context(), sqlitegen.CreateInstanceParams{
		Name:                      "Test Instance",
		PublicUrl:                 "https://tod.example.com",
		Timezone:                  "UTC",
		SelfServiceCircleCreation: flag,
		CreatedAt:                 int64(fixtureNow),
		UpdatedAt:                 int64(fixtureNow),
	})
	require.NoError(h.t, err)
}

// seedCircle writes a circle. Every circle is pinned to one server, immutably.
func (h *harness) seedCircle(name string) core.CircleID {
	h.t.Helper()
	id := newID[core.Circle](h)
	_, err := h.store.Queries().CreateCircle(h.t.Context(), sqlitegen.CreateCircleParams{
		CircleID:                 id.String(),
		Name:                     name,
		NameNorm:                 name,
		Description:              "",
		Server:                   schemaenum.ServerBlue,
		Timezone:                 "UTC",
		MinReportersToSupersede:  2,
		RevokeInvalidatesInvites: 1,
		State:                    schemaenum.CircleStateActive,
		CreatedAt:                int64(fixtureNow),
		UpdatedAt:                int64(fixtureNow),
	})
	require.NoError(h.t, err)
	return id
}

// localProviderKey is the wire key the fixture gives the `local` provider, matching what
// `tod-serve init --local` writes.
const localProviderKey = "local"

// seedProvider writes the instance's local identity provider, which is the one that needs no third
// party. It is written once per harness: `identity_provider.kind` is unique, so an instance has at
// most one provider of each kind.
func (h *harness) seedProvider() core.IdentityProviderID {
	h.t.Helper()
	if !h.provider.IsZero() {
		return h.provider
	}
	id := newID[core.IdentityProvider](h)
	_, err := h.store.Queries().CreateIdentityProvider(h.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: id.String(),
			// The wire key `listIdentityProviders` publishes and `/join` dispatches on. It is the
			// plain kind because an instance holds at most one `local` row, so there is nothing
			// to disambiguate — and a suffixed key would be a key no client could guess from the
			// public discovery endpoint.
			Key:         localProviderKey,
			Kind:        schemaenum.IdentityProviderKindLocal,
			DisplayName: "Local",
			Enabled:     1,
			// `verifiable_subject` is a CHECK against `kind`, never a toggle: local is 0.
			VerifiableSubject: 0,
			CreatedAt:         int64(fixtureNow),
			UpdatedAt:         int64(fixtureNow),
		})
	require.NoError(h.t, err)
	h.provider = id
	return id
}

// seedMember writes an identity and a human membership in the circle.
func (h *harness) seedMember(circle core.CircleID, role authz.Role) core.MembershipID {
	h.t.Helper()
	provider := h.seedProvider()
	identity := newID[core.Identity](h)
	_, err := h.store.Queries().CreateIdentity(h.t.Context(), sqlitegen.CreateIdentityParams{
		ID:          identity.String(),
		ProviderID:  provider.String(),
		Subject:     identity.String(),
		DisplayName: string(role) + " " + identity.String()[:8],
		CreatedAt:   int64(fixtureNow),
		UpdatedAt:   int64(fixtureNow),
	})
	require.NoError(h.t, err)

	membership := newID[core.Membership](h)
	identityID := identity.String()
	_, err = h.store.Queries().CreateMembership(h.t.Context(), sqlitegen.CreateMembershipParams{
		ID:              membership.String(),
		CircleID:        circle.String(),
		IdentityID:      &identityID,
		Kind:            schemaenum.MembershipKindHuman,
		DisplayName:     string(role),
		DisplayNameNorm: string(role),
		Role:            string(role),
		JoinedAt:        int64(fixtureNow),
		CreatedAt:       int64(fixtureNow),
		UpdatedAt:       int64(fixtureNow),
	})
	require.NoError(h.t, err)
	return membership
}

// seedToken mints a token bound to a membership and returns the credential exactly once, which is
// the only moment it exists in plaintext anywhere.
func (h *harness) seedToken(membership core.MembershipID, scopes ...authz.Scope) core.Secret {
	h.t.Helper()
	minted, err := h.minter.Mint()
	require.NoError(h.t, err)
	scopesJSON, err := auth.ScopesJSON(scopes)
	require.NoError(h.t, err)

	_, err = h.store.Queries().CreateAPIToken(h.t.Context(), sqlitegen.CreateAPITokenParams{
		ID:           newID[core.APIToken](h).String(),
		MembershipID: membership.String(),
		TokenPrefix:  minted.Prefix,
		TokenHash:    minted.Hash,
		Name:         "test device",
		ScopesJson:   scopesJSON,
		CreatedAt:    int64(fixtureNow),
		UpdatedAt:    int64(fixtureNow),
	})
	require.NoError(h.t, err)
	return minted.Token
}

// revokeMembership revokes a membership directly, which is what an officer's `revokeMember` will
// do once that route exists. Membership state is read on every request, so this takes effect on
// the next one.
func (h *harness) revokeMembership(circle core.CircleID, membership core.MembershipID) {
	h.t.Helper()
	revoker := h.seedMember(circle, authz.RoleOwner)
	at := int64(h.clock.Now())
	by := revoker.String()
	_, err := h.store.Queries().RevokeMembership(h.t.Context(), sqlitegen.RevokeMembershipParams{
		RevokedAt:             &at,
		RevokedByMembershipID: &by,
		UpdatedAt:             at,
		CircleID:              circle.String(),
		ID:                    membership.String(),
	})
	require.NoError(h.t, err)
}

// identityOf returns the identity behind a human membership, which is the key an instance grant
// hangs off. A service membership has none and this fails rather than returning a zero id: a test
// that granted an instance permission to nobody would pass for the wrong reason.
func (h *harness) identityOf(member core.MembershipID) core.IdentityID {
	h.t.Helper()
	row, err := h.store.Queries().GetMembershipByID(h.t.Context(), member.String())
	require.NoError(h.t, err)
	require.NotNil(h.t, row.IdentityID, "membership %s has no identity", member)
	id, err := core.ParseID[core.Identity](*row.IdentityID)
	require.NoError(h.t, err)
	return id
}

// grantInstance records `granted` for the identity behind a membership, through the real ledger
// over the real table. There is no shortcut past the service here on purpose: an instance-realm
// route's answer depends on rows, and inserting them by hand would let the test pass against a
// schema the service could not actually write.
func (h *harness) grantInstance(member core.MembershipID, perms ...authz.Permission) {
	h.t.Helper()
	identity := h.identityOf(member)
	for _, p := range perms {
		_, err := h.grants.Decide(h.t.Context(), instancegrant.DecideRequest{
			IdentityID: identity, Permission: p,
			Decision: instancegrant.DecisionGranted,
			Reason:   "fixture",
		})
		require.NoError(h.t, err)
	}
}

// revokeInstance records `revoked` for the identity behind a membership.
func (h *harness) revokeInstance(member core.MembershipID, perm authz.Permission) {
	h.t.Helper()
	_, err := h.grants.Decide(h.t.Context(), instancegrant.DecideRequest{
		IdentityID: h.identityOf(member), Permission: perm,
		Decision: instancegrant.DecisionRevoked,
		Reason:   "fixture",
	})
	require.NoError(h.t, err)
}

// session returns a signed session cookie value for a membership.
func (h *harness) session(membership core.MembershipID, steppedUp bool) string {
	h.t.Helper()
	s := auth.Session{
		MembershipID: membership.String(),
		IssuedAt:     h.clock.Now(),
		ExpiresAt:    h.clock.Now().Add(auth.DefaultSessionTTL),
	}
	if steppedUp {
		s.SteppedUpAt = h.clock.Now()
	}
	value, err := h.codec.Encode(s)
	require.NoError(h.t, err)
	return value
}

// request describes one call to the API.
type request struct {
	Method  string
	Path    string
	Token   core.Secret
	Session string
	Body    string
	Headers map[string]string
	Metrics bool
}

// response is what the API answered, with the problem already parsed when there was one.
type response struct {
	Status  int
	Header  http.Header
	Body    string
	Problem apierr.Problem
}

// do issues a request against the wired server.
func (h *harness) do(req request) response {
	h.t.Helper()
	var body io.Reader
	if req.Body != "" {
		body = stringReader(req.Body)
	}
	r := httptest.NewRequestWithContext(h.t.Context(), req.Method, req.Path, body)
	if req.Body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if !req.Token.IsZero() {
		r.Header.Set("Authorization", auth.BearerScheme+req.Token.Reveal())
	}
	if req.Session != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: req.Session})
	}
	for k, v := range req.Headers {
		r.Header.Set(k, v)
	}

	handler := h.handler
	if req.Metrics {
		metrics, ok := h.server.MetricsHandler()
		require.True(h.t, ok, "metrics are disabled in this harness")
		handler = metrics
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r.WithContext(context.WithoutCancel(h.t.Context())))

	out := response{Status: rec.Code, Header: rec.Header(), Body: rec.Body.String()}
	if rec.Code >= http.StatusBadRequest {
		// Unmarshalled unconditionally on a failure: every error response in this API is a
		// problem, and a body that does not parse as one is a finding rather than a special case.
		require.NoError(h.t, json.Unmarshal(rec.Body.Bytes(), &out.Problem),
			"a %d response did not parse as a problem: %s", rec.Code, rec.Body.String())
	}
	return out
}

// requireProblem asserts a response is a problem with the given code, checked against the closed
// enum rather than against a string.
func (h *harness) requireProblem(got response, code apierr.Code) {
	h.t.Helper()
	def, ok := apierr.Lookup(code)
	require.True(h.t, ok, "%s is not in the catalogue", code)
	require.Equal(h.t, def.Status, got.Status, "body was: %s", got.Body)
	require.Equal(h.t, code, got.Problem.Code, "body was: %s", got.Body)
	require.Equal(h.t, def.TypeURL(), got.Problem.Type)
	require.Equal(h.t, apierr.ContentType, contentTypeOf(got))
}

func contentTypeOf(r response) string {
	ct := r.Header.Get("Content-Type")
	if i := indexByte(ct, ';'); i >= 0 {
		return ct[:i]
	}
	return ct
}

func indexByte(s string, c byte) int {
	for i := range len(s) {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func stringReader(s string) io.Reader { return &sliceReader{data: []byte(s)} }

// sliceReader is a minimal reader so a request body needs no extra dependency.
type sliceReader struct {
	data []byte
	off  int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

// advance moves the fixture clock, for the tests that need time to pass.
func (h *harness) advance(d time.Duration) { h.clock.Advance(d) }

// seedMemberIn writes a membership in a circle the fixture did not create, for the tests that build
// a circle through the real service rather than through seedCircle.
func (h *harness) seedMemberIn(circleID core.CircleID, role authz.Role) core.MembershipID {
	h.t.Helper()
	return h.seedMember(circleID, role)
}

// timerChange is one push at the projection: which circle, which target, and whether it was the
// instance-wide catalogue timer that moved rather than one circle's override.
type timerChange struct {
	Circle core.CircleID
	Target core.RaidTargetID
	Server core.Server
	Scope  string
}

// recordingInvalidator is the [api.TimerInvalidator] the suite wires.
//
// It records rather than no-ops, because "did this route push the invalidation" is the question
// TestRouteRegistry_EveryTimerWritingRoute_PushesTheInvalidation exists to answer, and a no-op
// fake would let every route pass it. The failure it guards is a route that writes a window and
// tells nobody, which is invisible from the response.
type recordingInvalidator struct {
	mu      sync.Mutex
	changes []timerChange
	// delegate is the REAL projection. Recording and doing are both wanted: the record answers
	// "did this route push the invalidation", and the delegate is what makes the next board read
	// in the same test show the new window rather than the cached old one.
	delegate api.TimerInvalidator
	// err, when set, is returned by both methods INSTEAD of delegating. A write whose invalidation
	// failed must not report success, and that is only assertable if the fake can fail.
	err error
}

func (r *recordingInvalidator) OnTimerChange(
	ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes,
		timerChange{Circle: circleID, Target: targetID, Scope: "circle"})
	if r.err != nil {
		return r.err
	}
	return r.delegate.OnTimerChange(ctx, circleID, targetID)
}

func (r *recordingInvalidator) OnCatalogueTimerChange(
	ctx context.Context, server core.Server, targetID core.RaidTargetID,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes,
		timerChange{Server: server, Target: targetID, Scope: "instance"})
	if r.err != nil {
		return r.err
	}
	return r.delegate.OnCatalogueTimerChange(ctx, server, targetID)
}

// recorded returns the pushes so far.
func (r *recordingInvalidator) recorded() []timerChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]timerChange(nil), r.changes...)
}

// reset clears the record, so a test can drive one route and assert on that route alone.
func (r *recordingInvalidator) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = nil
}

// failWith makes both methods fail, for the assertion that a write whose invalidation failed
// answers with the failure rather than reporting success.
func (r *recordingInvalidator) failWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}
