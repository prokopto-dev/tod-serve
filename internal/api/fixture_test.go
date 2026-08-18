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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
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
	t        *testing.T
	server   *api.Server
	store    *store.DB
	clock    *clock.Test
	ids      *core.Generator
	minter   *auth.Minter
	codec    *auth.SessionCodec
	handler  http.Handler
	provider core.IdentityProviderID
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
	authn, err := auth.NewAuthenticator(db, minter, codec, clk, log, auth.DefaultStepUpWindow)
	require.NoError(t, err)

	server, err := api.New(api.Config{
		Version: "0.0.0-test",
		Store:   db,
		Auth:    authn,
		Clock:   clk,
		Log:     log,
		IDs:     ids,
		Metrics: api.MetricsConfig{Enabled: true, Token: testMetricsTok},
		// Response validation runs across the WHOLE integration suite: every request any test in
		// this package makes is checked against the response contract, including the ones the
		// framework answers before a handler runs.
		OnResponseViolation: func(v api.Violation) { t.Errorf("response contract: %s", v) },
	})
	require.NoError(t, err)

	return &harness{
		t: t, server: server, store: db, clock: clk, ids: ids,
		minter: minter, codec: codec, handler: server.Handler(),
	}
}

// newHarnessWithoutMetrics builds the same server with metrics off, which is the DEFAULT: a
// metrics endpoint that is on unless somebody turns it off is an information leak nobody chose.
func newHarnessWithoutMetrics(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	authn, err := auth.NewAuthenticator(
		h.store, h.minter, h.codec, h.clock, log, auth.DefaultStepUpWindow)
	require.NoError(t, err)

	server, err := api.New(api.Config{
		Version:             "0.0.0-test",
		Store:               h.store,
		Auth:                authn,
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
			ID:          id.String(),
			Key:         "local-" + id.String()[:8],
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
