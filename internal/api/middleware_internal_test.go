package api

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

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// These tests exercise the MIDDLEWARE, not the handlers.
//
// Most of the routes the middleware guards belong to milestones that have not landed: there is no
// `createTodReport` handler yet, so there is nothing to drive the idempotency path or the
// cross-circle path through. Rather than wait — which would mean shipping the middleware untested
// and discovering its bugs from another worker's failing suite — each test here attaches a STUB
// handler to a real registry row. The row is the real one, with its real permission, scopes,
// tenancy flag and idempotency scope; only the body of the handler is a stub.
//
// This is deliberately NOT what TestTenancy_CrossCircle_EveryOperationDenies does. That test walks
// what the binary actually serves, and says so when the answer is nothing.

const stubNow = core.Micros(1_755_483_247_000_000)

// stubRig is a wired API with stub handlers on chosen registry routes.
type stubRig struct {
	t        *testing.T
	server   *Server
	handler  http.Handler
	db       *store.DB
	clock    *clock.Test
	ids      *core.Generator
	minter   *auth.Minter
	provider string
	calls    int
	// beforeReturn runs inside the stub handler, after it has "committed", and is how a test
	// arranges for the client to disconnect at the one moment that matters.
	beforeReturn func()
}

func newStubRig(t *testing.T, ops ...OperationID) *stubRig {
	t.Helper()
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tod.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(ctx))

	clk := clock.NewTest(stubNow)
	ids := core.NewGenerator(rand.Reader)
	minter, err := auth.NewMinter("stub-pepper", rand.Reader)
	require.NoError(t, err)
	codec, err := auth.NewSessionCodec("stub-session-key")
	require.NoError(t, err)
	grants, err := instancegrant.New(instancegrant.Config{
		Store: db, Clock: clk, IDs: ids, Log: log,
	})
	require.NoError(t, err)
	authn, err := auth.NewAuthenticator(
		db, minter, codec, grants, clk, log, auth.DefaultStepUpWindow)
	require.NoError(t, err)

	cfg := Config{Version: "stub", Store: db, Auth: authn, Clock: clk, Log: log, IDs: ids}
	counts := newMetrics(cfg.Version, stubNow)
	invites := newLimiter(cfg.InviteRateLimit)
	server := &Server{
		cfg: cfg, counts: counts, invites: invites,
		api:     newBuilder(cfg, counts, invites, apiMediaTypes(), true),
		metrics: newBuilder(cfg, counts, invites, metricsMediaTypes(), false),
	}

	rig := &stubRig{t: t, server: server, db: db, clock: clk, ids: ids, minter: minter}
	for _, op := range ops {
		require.NoError(t, Register(server.api, op,
			func(ctx context.Context, _ *stubInput) (*stubOutput, error) {
				rig.calls++
				if rig.beforeReturn != nil {
					rig.beforeReturn()
				}
				return &stubOutput{Body: stubBody{OK: true, Call: rig.calls}}, nil
			}))
	}
	rig.handler = server.api.handler()
	return rig
}

type stubInput struct{}

type stubBody struct {
	OK   bool `json:"ok"`
	Call int  `json:"call"`
}

type stubOutput struct{ Body stubBody }

func (r *stubRig) seedCircleAndMember(role authz.Role) (core.CircleID, core.MembershipID) {
	r.t.Helper()
	circleID, err := core.NewID[core.Circle](r.ids, stubNow)
	require.NoError(r.t, err)
	_, err = r.db.Queries().CreateCircle(r.t.Context(), sqlitegen.CreateCircleParams{
		CircleID: circleID.String(), Name: circleID.String(), NameNorm: circleID.String(),
		Server: schemaenum.ServerBlue, Timezone: "UTC", MinReportersToSupersede: 2,
		RevokeInvalidatesInvites: 1, State: schemaenum.CircleStateActive,
		CreatedAt: int64(stubNow), UpdatedAt: int64(stubNow),
	})
	require.NoError(r.t, err)
	return circleID, r.seedMemberIn(circleID, role)
}

// seedMemberIn adds a member to an existing circle, so a test can have two principals whose
// requests are about the same rows.
func (r *stubRig) seedMemberIn(circleID core.CircleID, role authz.Role) core.MembershipID {
	r.t.Helper()
	ctx := r.t.Context()

	if r.provider == "" {
		providerID, provErr := core.NewID[core.IdentityProvider](r.ids, stubNow)
		require.NoError(r.t, provErr)
		_, provErr = r.db.Queries().CreateIdentityProvider(ctx,
			sqlitegen.CreateIdentityProviderParams{
				ID: providerID.String(), Key: "local", Kind: schemaenum.IdentityProviderKindLocal,
				DisplayName: "Local", Enabled: 1, VerifiableSubject: 0,
				CreatedAt: int64(stubNow), UpdatedAt: int64(stubNow),
			})
		require.NoError(r.t, provErr)
		r.provider = providerID.String()
	}

	identityID, err := core.NewID[core.Identity](r.ids, stubNow)
	require.NoError(r.t, err)
	_, err = r.db.Queries().CreateIdentity(ctx, sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: r.provider, Subject: identityID.String(),
		DisplayName: "member", CreatedAt: int64(stubNow), UpdatedAt: int64(stubNow),
	})
	require.NoError(r.t, err)

	membershipID, err := core.NewID[core.Membership](r.ids, stubNow)
	require.NoError(r.t, err)
	identity := identityID.String()
	_, err = r.db.Queries().CreateMembership(ctx, sqlitegen.CreateMembershipParams{
		ID: membershipID.String(), CircleID: circleID.String(), IdentityID: &identity,
		Kind: schemaenum.MembershipKindHuman, DisplayName: "member", DisplayNameNorm: "member",
		Role: string(role), JoinedAt: int64(stubNow),
		CreatedAt: int64(stubNow), UpdatedAt: int64(stubNow),
	})
	require.NoError(r.t, err)
	return membershipID
}

func (r *stubRig) token(membership core.MembershipID, scopes ...authz.Scope) core.Secret {
	r.t.Helper()
	minted, err := r.minter.Mint()
	require.NoError(r.t, err)
	scopesJSON, err := auth.ScopesJSON(scopes)
	require.NoError(r.t, err)
	tokenID, err := core.NewID[core.APIToken](r.ids, stubNow)
	require.NoError(r.t, err)
	_, err = r.db.Queries().CreateAPIToken(r.t.Context(), sqlitegen.CreateAPITokenParams{
		ID: tokenID.String(), MembershipID: membership.String(), TokenPrefix: minted.Prefix,
		TokenHash: minted.Hash, Name: "stub", ScopesJson: scopesJSON,
		CreatedAt: int64(stubNow), UpdatedAt: int64(stubNow),
	})
	require.NoError(r.t, err)
	return minted.Token
}

func (r *stubRig) post(path string, token core.Secret, key, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	return r.postWithContext(r.t.Context(), path, token, key, body)
}

// postWithContext issues the same request against a caller-supplied context, so a test can cancel
// it the way a client hanging up cancels one.
func (r *stubRig) postWithContext(
	ctx context.Context, path string, token core.Secret, key, body string,
) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, stringBody(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth.BearerScheme+token.Reveal())
	if key != "" {
		req.Header.Set(IdempotencyKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

// publicPost issues an unauthenticated POST from a fixed caller address.
func (r *stubRig) publicPost(path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	return r.publicPostFrom("192.0.2.1:1234", path, body)
}

// publicPostFrom issues an unauthenticated POST from a named caller, so a test can prove the
// bucket is per caller rather than global.
func (r *stubRig) publicPostFrom(remoteAddr, path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequestWithContext(r.t.Context(), http.MethodPost, path, stringBody(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

func stringBody(s string) io.Reader { return io.NopCloser(newStringReader(s)) }

func newStringReader(s string) io.Reader { return &sliceReaderInternal{data: []byte(s)} }

type sliceReaderInternal struct {
	data []byte
	off  int
}

func (r *sliceReaderInternal) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) apierr.Problem {
	t.Helper()
	var p apierr.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p), "body: %s", rec.Body.String())
	return p
}

func reportPath(circle core.CircleID) string {
	return BasePath + "/circles/" + circle.String() + "/tod-reports"
}

// The middleware answers 404 — never 403 — for a circle that is not the caller's. A 403 would
// confirm that the circle exists and that the id is real, which canonical §7 hides.
func TestTenancy_TheMiddleware_AnswersNotFoundAcrossCircles(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	_, member := rig.seedCircleAndMember(authz.RoleOfficer)
	otherCircle, _ := rig.seedCircleAndMember(authz.RoleOfficer)
	token := rig.token(member, authz.ScopeTodReport)

	rec := rig.post(reportPath(otherCircle), token, "key-1", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeNotFound, problemOf(t, rec).Code)
	require.Zero(t, rig.calls, "the handler ran for another circle's request")
}

// A circle id that is not even a ULID answers the same way. One rule with no branch: the moment
// there are two answers here, one of them tells a prober that their guess was well-formed.
func TestTenancy_AMalformedCircleID_AnswersNotFoundToo(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	_, member := rig.seedCircleAndMember(authz.RoleOfficer)
	token := rig.token(member, authz.ScopeTodReport)

	rec := rig.post(BasePath+"/circles/not-a-ulid/tod-reports", token, "key-1", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeNotFound, problemOf(t, rec).Code)
}

// Tenancy is decided BEFORE permission. A caller with no permission at all, asking about somebody
// else's circle, must still get 404 — otherwise the 403 leaks the circle's existence to exactly the
// caller who should learn least.
func TestTenancy_IsDecidedBeforePermission(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	_, member := rig.seedCircleAndMember(authz.RoleObserver)
	otherCircle, _ := rig.seedCircleAndMember(authz.RoleOwner)
	token := rig.token(member, authz.ScopeTodReport)

	rec := rig.post(reportPath(otherCircle), token, "key-1", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// Within the caller's own circle, insufficient permission is 403 — the other half of the rule.
func TestAuthorize_WithinTheCircle_InsufficientPermissionIs403(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	circle, member := rig.seedCircleAndMember(authz.RoleObserver)
	token := rig.token(member, authz.ScopeTodReport)

	rec := rig.post(reportPath(circle), token, "key-1", `{}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeForbidden, problemOf(t, rec).Code,
		"an observer's ROLE does not hold tod.report; the fix is a role, not a scope")
}

// The role holds it and the token does not reach it: a different failure with a different fix, and
// therefore a different code.
func TestAuthorize_TheRoleHoldsItAndTheTokenDoesNot_IsInsufficientScope(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	circle, member := rig.seedCircleAndMember(authz.RoleOfficer)
	token := rig.token(member, authz.ScopeTodRead)

	rec := rig.post(reportPath(circle), token, "key-1", `{}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeInsufficientScope, problemOf(t, rec).Code)
}

// `Idempotency-Key` is required on every POST that creates domain state. Without it a retry after a
// timeout — the normal case on a domestic connection — puts a duplicate kill into an append-only
// log that is never edited.
func TestIdempotency_AStateCreatingPostWithoutAKey_IsRefused(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	circle, member := rig.seedCircleAndMember(authz.RoleOfficer)
	token := rig.token(member, authz.ScopeTodReport)

	rec := rig.post(reportPath(circle), token, "", `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeIdempotencyKeyRequired, problemOf(t, rec).Code)
	require.Zero(t, rig.calls)
}

// The same key with the same request replays the stored response and does NOT run the handler
// again. This is the whole point.
func TestIdempotency_TheSameRequestTwice_ReplaysAndRunsOnce(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	circle, member := rig.seedCircleAndMember(authz.RoleOfficer)
	token := rig.token(member, authz.ScopeTodReport)

	first := rig.post(reportPath(circle), token, "retry-me", `{}`)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, 1, rig.calls)

	second := rig.post(reportPath(circle), token, "retry-me", `{}`)
	require.Equal(t, first.Code, second.Code)
	require.JSONEq(t, first.Body.String(), second.Body.String())
	require.Equal(t, 1, rig.calls, "the handler ran twice for one logical operation")
	require.Equal(t, "true", second.Header().Get(IdempotencyReplayedHeader),
		"a replay must say so, or a client cannot tell a retry that worked from a duplicate")
}

// The same key with a DIFFERENT request is refused rather than answered with the first request's
// response — which would report success for something that never happened.
func TestIdempotency_TheSameKeyWithADifferentRequest_IsRefused(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	circle, member := rig.seedCircleAndMember(authz.RoleOfficer)
	token := rig.token(member, authz.ScopeTodReport)

	require.Equal(t, http.StatusOK, rig.post(reportPath(circle), token, "k", `{"a":1}`).Code)

	rec := rig.post(reportPath(circle), token, "k", `{"a":2}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeIdempotencyKeyReused, problemOf(t, rec).Code)
	require.Equal(t, 1, rig.calls)
}

// Uniqueness is `(membership, key)`, keyed on the MEMBERSHIP and never on the token, so rotating a
// token mid-retry still replays instead of duplicating.
func TestIdempotency_ARotatedToken_StillReplays(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	circle, member := rig.seedCircleAndMember(authz.RoleOfficer)
	first := rig.token(member, authz.ScopeTodReport)
	second := rig.token(member, authz.ScopeTodReport)

	original := rig.post(reportPath(circle), first, "same-key", `{}`)
	require.Equal(t, http.StatusOK, original.Code, original.Body.String())

	replayed := rig.post(reportPath(circle), second, "same-key", `{}`)
	require.Equal(t, "true", replayed.Header().Get(IdempotencyReplayedHeader))
	require.JSONEq(t, original.Body.String(), replayed.Body.String())
	require.Equal(t, 1, rig.calls)
}

// Two members of one circle do not share a key space: the record is keyed on the principal, so one
// person's retry can never be answered with another person's response.
func TestIdempotency_TwoMembers_DoNotShareAKeySpace(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	circle, first := rig.seedCircleAndMember(authz.RoleOfficer)
	second := rig.seedMemberIn(circle, authz.RoleOfficer)

	firstRec := rig.post(reportPath(circle), rig.token(first, authz.ScopeTodReport), "shared", `{}`)
	require.Equal(t, http.StatusOK, firstRec.Code, firstRec.Body.String())
	require.Equal(t, 1, rig.calls)

	secondRec := rig.post(reportPath(circle), rig.token(second, authz.ScopeTodReport), "shared", `{}`)
	require.Equal(t, http.StatusOK, secondRec.Code, secondRec.Body.String())
	require.Equal(t, 2, rig.calls, "one member's key replayed for another member")
	require.Empty(t, secondRec.Header().Get(IdempotencyReplayedHeader))
}

// A key that cannot be stored or read back is refused at the edge rather than written.
func TestIdempotency_AnUnusableKey_IsRefused(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	circle, member := rig.seedCircleAndMember(authz.RoleOfficer)
	token := rig.token(member, authz.ScopeTodReport)

	for _, key := range []string{
		string(make([]byte, maxIdempotencyKeyLen+1)),
		"has\na newline",
	} {
		rec := rig.post(reportPath(circle), token, key, `{}`)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		require.Equal(t, apierr.CodeValidationFailed, problemOf(t, rec).Code)
	}
	require.Zero(t, rig.calls)
}

// An operation whose principal is not a membership still REQUIRES the header — the registry says so
// and the middleware enforces it — and hands it to the handler, which owns the replay because
// `idempotency_record.principal_membership_id` is NOT NULL and there is no membership yet.
func TestIdempotency_AHandlerScopedOperation_RequiresTheKeyAndPassesItThrough(t *testing.T) {
	t.Parallel()
	var seen string
	rig := newStubRig(t)
	require.NoError(t, Register(rig.server.api, OpRedeemInvite,
		func(ctx context.Context, _ *stubInput) (*stubOutput, error) {
			key, ok := IdempotencyKeyFrom(ctx)
			require.True(t, ok, "the handler was not given the key it is required to use")
			seen = key
			return &stubOutput{Body: stubBody{OK: true}}, nil
		}))
	rig.handler = rig.server.api.handler()

	without := httptest.NewRequestWithContext(t.Context(), http.MethodPost, BasePath+"/join", stringBody(`{}`))
	without.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.handler.ServeHTTP(rec, without)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeIdempotencyKeyRequired, problemOf(t, rec).Code)

	with := httptest.NewRequestWithContext(t.Context(), http.MethodPost, BasePath+"/join", stringBody(`{}`))
	with.Header.Set("Content-Type", "application/json")
	with.Header.Set(IdempotencyKeyHeader, "join-once")
	rec = httptest.NewRecorder()
	rig.handler.ServeHTTP(rec, with)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "join-once", seen)
}

// Registering a handler for an operation the registry does not hold is impossible, not merely
// discouraged: there is no path from a handler to a URL that does not go through the lookup.
func TestRegister_AnOperationOutsideTheRegistry_IsRefused(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t)
	err := Register(rig.server.api, OperationID("inventARoute"),
		func(ctx context.Context, _ *stubInput) (*stubOutput, error) { return nil, nil })
	require.ErrorIs(t, err, ErrUnknownOperation)
}

// Two handlers on one operation is not a merge; it is a bug that would silently keep whichever ran
// last.
func TestRegister_TheSameOperationTwice_IsRefused(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	err := Register(rig.server.api, OpCreateTodReport,
		func(ctx context.Context, _ *stubInput) (*stubOutput, error) { return nil, nil })
	require.ErrorIs(t, err, ErrAlreadyRegistered)
}

// `previewInvite` and `createAuthorizationURL` both reveal whether an invite code is live, so they
// draw on ONE bucket keyed on the caller. Two buckets would simply hand a code-guesser twice the
// guessing budget, which is precisely what `previewInvite`'s hard limit exists to make expensive.
func TestInviteOracle_PreviewAndAuthorizationURL_ShareOneBucket(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpPreviewInvite, OpCreateAuthorizationURL)

	// Exhaust the bucket entirely through ONE of the two routes.
	for range DefaultInviteBurst {
		rec := rig.publicPost(BasePath+"/invites/preview", `{"invite_code":"TODI-AAAAA-BBBBB"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	// The other route is now exhausted too, which is the whole assertion: one bucket, not two.
	rec := rig.publicPost(BasePath+"/auth/authorization-url", `{"provider":"local"}`)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeRateLimited, problemOf(t, rec).Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"),
		"a rate limit that does not say when to come back is a rate limit clients hammer")
	require.Equal(t, DefaultInviteBurst, rig.calls,
		"the rejected request reached the handler, so it could have written an auth_flow row")
}

// The limit is enforced BEFORE the handler runs. `createAuthorizationURL` writes an `auth_flow`
// row, so a limited caller reaching it at all would let an unauthenticated flood grow the table —
// which is what TestAuthFlow_RateLimitedCaller_CreatesNoRows asserts at the other end.
func TestInviteOracle_ARateLimitedCaller_ReachesNoHandler(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateAuthorizationURL)
	for range DefaultInviteBurst {
		require.Equal(t, http.StatusOK,
			rig.publicPost(BasePath+"/auth/authorization-url", `{}`).Code)
	}
	before := rig.calls

	require.Equal(t, http.StatusTooManyRequests,
		rig.publicPost(BasePath+"/auth/authorization-url", `{}`).Code)
	require.Equal(t, before, rig.calls, "a rate-limited request ran the handler")
}

// The bucket refills on the injected clock, so a caller who waits is served rather than locked out.
func TestInviteOracle_TheBucket_RefillsOverTime(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpPreviewInvite)
	for range DefaultInviteBurst {
		require.Equal(t, http.StatusOK, rig.publicPost(BasePath+"/invites/preview", `{}`).Code)
	}
	require.Equal(t, http.StatusTooManyRequests,
		rig.publicPost(BasePath+"/invites/preview", `{}`).Code)

	rig.clock.Advance(DefaultInviteRefill)
	require.Equal(t, http.StatusOK, rig.publicPost(BasePath+"/invites/preview", `{}`).Code,
		"the bucket did not refill")
}

// One caller's probing must not lock out everybody else, or the limiter becomes the outage it
// exists to prevent.
func TestInviteOracle_TheBucket_IsPerCaller(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpPreviewInvite)
	for range DefaultInviteBurst {
		require.Equal(t, http.StatusOK,
			rig.publicPostFrom("10.0.0.1:1234", BasePath+"/invites/preview", `{}`).Code)
	}
	require.Equal(t, http.StatusTooManyRequests,
		rig.publicPostFrom("10.0.0.1:1234", BasePath+"/invites/preview", `{}`).Code)
	require.Equal(t, http.StatusOK,
		rig.publicPostFrom("10.0.0.2:1234", BasePath+"/invites/preview", `{}`).Code)
}

// The metered set is exactly the two public routes that accept an invite code. A third such route
// joins this bucket by setting the flag; it does not get one of its own.
func TestInviteOracle_TheMeteredSet_IsEveryPublicRouteThatTakesACode(t *testing.T) {
	t.Parallel()
	var ids []OperationID
	for _, r := range InviteOracleRoutes() {
		ids = append(ids, r.ID)
		require.Equal(t, AuthPublic, r.Auth,
			"%s is metered as an invite oracle and is not public", r.ID)
	}
	require.ElementsMatch(t, []OperationID{OpPreviewInvite, OpCreateAuthorizationURL}, ids)
}

// A client that hangs up after the handler has committed must still get a replay when it retries.
//
// This is the case idempotency exists for, and it is the one where the naive implementation fails:
// the domain write has landed, the request's context is cancelled, and recording the outcome on
// that context would fail. The record would stay incomplete, every retry would answer
// `idempotency_conflict` until it expired, and the retry after that would run the handler a SECOND
// time — appending a duplicate row to a log that is never edited.
func TestIdempotency_AClientThatDisconnects_StillReplaysOnRetry(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t, OpCreateTodReport)
	circle, member := rig.seedCircleAndMember(authz.RoleOfficer)
	token := rig.token(member, authz.ScopeTodReport)

	ctx, cancel := context.WithCancel(t.Context())
	// The handler "commits" and the client goes away before the response is recorded, which is
	// exactly the ordering net/http produces when a browser tab closes mid-request.
	rig.beforeReturn = cancel
	first := rig.postWithContext(ctx, reportPath(circle), token, "hung-up", `{}`)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, 1, rig.calls)
	rig.beforeReturn = nil

	// The record must be complete despite the cancellation, or the retry below cannot replay.
	record, err := rig.db.Queries().GetIdempotencyRecord(t.Context(),
		sqlitegen.GetIdempotencyRecordParams{
			PrincipalMembershipID: member.String(), Key: "hung-up",
		})
	require.NoError(t, err)
	require.NotNil(t, record.CompletedAt,
		"the outcome was not recorded, so a retry cannot replay and will run the handler again")

	retry := rig.post(reportPath(circle), token, "hung-up", `{}`)
	require.Equal(t, "true", retry.Header().Get(IdempotencyReplayedHeader))
	require.JSONEq(t, first.Body.String(), retry.Body.String())
	require.Equal(t, 1, rig.calls, "the retry ran the handler a second time")
}

// The same disconnect on a request that FAILED must clear the record rather than complete it: the
// request did not happen, so the retry has to run rather than replay a failure.
func TestIdempotency_ADisconnectOnAFailedRequest_ClearsTheRecord(t *testing.T) {
	t.Parallel()
	rig := newStubRig(t)
	_, member := rig.seedCircleAndMember(authz.RoleOfficer)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rig.recordOutcomeDirect(ctx, member, "failed-key", http.StatusInternalServerError)

	_, err := rig.db.Queries().GetIdempotencyRecord(t.Context(),
		sqlitegen.GetIdempotencyRecordParams{
			PrincipalMembershipID: member.String(), Key: "failed-key",
		})
	require.ErrorIs(t, err, store.ErrNoRows,
		"a 5xx left a record behind, which answers every retry with a conflict until it expires")
}

// recordOutcomeDirect writes a record and then reports an outcome against an already-cancelled
// context, which is the narrowest possible statement of "the bookkeeping outlives the request".
func (r *stubRig) recordOutcomeDirect(
	ctx context.Context, member core.MembershipID, key string, status int,
) {
	r.t.Helper()
	id, err := core.NewID[core.IdempotencyRecord](r.ids, stubNow)
	require.NoError(r.t, err)
	created, err := r.db.Queries().CreateIdempotencyRecord(r.t.Context(),
		sqlitegen.CreateIdempotencyRecordParams{
			ID: id.String(), PrincipalMembershipID: member.String(), Key: key,
			RequestHash: []byte("hash"), ExpiresAt: int64(stubNow.Add(idempotencyTTL)),
			CreatedAt: int64(stubNow), UpdatedAt: int64(stubNow),
		})
	require.NoError(r.t, err)
	r.server.api.recordOutcome(ctx, created.ID, status, []byte(`{"ok":true}`))
}
