package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// signOutPath is the operation's served path, taken from the registry rather than written out, so
// a route that moved is a compile-time lookup rather than a 404 nobody reads.
func signOutPath(t *testing.T) string {
	t.Helper()
	route, err := api.MustLookup(api.OpSignOut)
	require.NoError(t, err)
	return route.FullPath()
}

// TestSignOut_TheSameCookie_IsRefusedAfterwards is the load-bearing test on this route.
//
// A `200` and a cleared cookie prove only that the BROWSER was asked to forget the session. That is
// the weaker half and it is the half an attacker never has to honour: a cookie copied off a shared
// machine, or read out of a backup of a profile directory, is not in the cookie jar the response
// cleared. So the assertion here is made with the SAME cookie value the caller signed out with,
// replayed by hand after the fact — which is exactly what somebody holding a copy would do.
//
// It is asserted against a real route rather than against the authenticator directly, because the
// question is whether the server refuses the credential, not whether one function does.
func TestSignOut_TheSameCookie_IsRefusedAfterwards(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(true)
	circleID := h.seedCircle("Sign Out")
	member := h.seedMember(circleID, authz.RoleOwner)
	session := h.session(member, true)

	// The cookie works before the sign-out, so the refusal below is about the sign-out and not
	// about a fixture that never authenticated in the first place.
	before := h.do(request{Method: http.MethodGet, Path: "/api/v1/me", Session: session})
	require.Equal(t, http.StatusOK, before.Status, "body was: %s", before.Body)

	out := h.do(request{Method: http.MethodDelete, Path: signOutPath(t), Session: session})
	require.Equal(t, http.StatusOK, out.Status, "body was: %s", out.Body)

	after := h.do(request{Method: http.MethodGet, Path: "/api/v1/me", Session: session})
	h.requireProblem(after, apierr.CodeUnauthenticated)
}

// The cookie the browser holds is cleared too. Without this the console would keep presenting a
// credential the server now refuses, and every screen would render a 401 instead of a sign-in page.
func TestSignOut_ClearsTheSessionCookie(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(true)
	circleID := h.seedCircle("Sign Out")
	member := h.seedMember(circleID, authz.RoleOwner)

	out := h.do(request{
		Method: http.MethodDelete, Path: signOutPath(t), Session: h.session(member, true),
	})
	require.Equal(t, http.StatusOK, out.Status, "body was: %s", out.Body)

	var cleared *http.Cookie
	for _, c := range (&http.Response{Header: out.Header}).Cookies() {
		if c.Name == auth.SessionCookie {
			cleared = c
		}
	}
	require.NotNil(t, cleared, "no %s cookie in the response", auth.SessionCookie)
	require.Empty(t, cleared.Value)
	require.Negative(t, cleared.MaxAge, "a cleared cookie needs a negative Max-Age to be deleted")
	// The `__Host-` prefix still constrains the CLEARING cookie: a browser refuses to act on one
	// that is not Secure with Path=/ and no Domain, which would leave the session cookie in place.
	require.True(t, cleared.Secure)
	require.Equal(t, "/", cleared.Path)
	require.Empty(t, cleared.Domain)
}

// TestSignOut_APersonalAccessToken_IsUntouched is the promise the issue puts first.
//
// `internal/auth` holds sessions and personal access tokens together and ADR-0005 binds a PAT to a
// membership, so "end this membership's credentials" is one wrong loop away from being true. A
// raider's nParse+ destination going silent because somebody signed out of the website is the worst
// kind of surprise: the plugin fails at raid time, for a reason that happened hours earlier on a
// different device.
//
// Both halves are asserted. The token still authenticates AFTER the sign-out — which is the
// behaviour — and `tokens_kept` says so in the response, which is what the person clicking the
// button actually sees.
func TestSignOut_APersonalAccessToken_IsUntouched(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(true)
	circleID := h.seedCircle("Sign Out")
	member := h.seedMember(circleID, authz.RoleOwner)
	plugin := h.seedToken(member, authz.ScopeTodRead)

	reads := request{Method: http.MethodGet, Path: "/api/v1/me", Token: plugin}
	require.Equal(t, http.StatusOK, h.do(reads).Status)

	out := h.do(request{
		Method: http.MethodDelete, Path: signOutPath(t), Session: h.session(member, true),
	})
	require.Equal(t, http.StatusOK, out.Status, "body was: %s", out.Body)
	require.JSONEq(t,
		`{"tokens_kept":1,"as_of":"`+h.clock.Now().String()+`"}`, out.Body)

	after := h.do(reads)
	require.Equal(t, http.StatusOK, after.Status,
		"signing out of the console revoked a personal access token. body was: %s", after.Body)
}

// TestSignOut_AnotherSessionOfTheSameMembership_StillWorks is what "this session only" MEANS.
//
// It is the documented default and this is the assertion that makes it one rather than an intention:
// two live sessions for one membership — a laptop and a phone — and signing out of the first leaves
// the second signed in. If this route ever grew sign-out-everywhere semantics by accident, this is
// the test that would go red.
func TestSignOut_AnotherSessionOfTheSameMembership_StillWorks(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(true)
	circleID := h.seedCircle("Sign Out")
	member := h.seedMember(circleID, authz.RoleOwner)

	laptop := h.session(member, true)
	phone := h.session(member, true)
	require.NotEqual(t, laptop, phone, "the fixture minted one session twice, not two sessions")

	out := h.do(request{Method: http.MethodDelete, Path: signOutPath(t), Session: laptop})
	require.Equal(t, http.StatusOK, out.Status, "body was: %s", out.Body)

	h.requireProblem(
		h.do(request{Method: http.MethodGet, Path: "/api/v1/me", Session: laptop}),
		apierr.CodeUnauthenticated)
	still := h.do(request{Method: http.MethodGet, Path: "/api/v1/me", Session: phone})
	require.Equal(t, http.StatusOK, still.Status,
		"signing out on one device signed the other out too. body was: %s", still.Body)
}

// A retry, a double click, or a second tab signing out after the first must not be an error: the
// fact is already recorded and there is nothing else to do.
func TestSignOut_Twice_IsNotAnError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(true)
	circleID := h.seedCircle("Sign Out")
	member := h.seedMember(circleID, authz.RoleOwner)
	session := h.session(member, true)

	first := h.do(request{Method: http.MethodDelete, Path: signOutPath(t), Session: session})
	require.Equal(t, http.StatusOK, first.Status, "body was: %s", first.Body)

	// The second request presents a session the server now refuses, so it never reaches the
	// handler — 401 is the right answer and a 500 from a duplicate-key insert is not. The handler's
	// own idempotence is proved at the query, below.
	h.requireProblem(
		h.do(request{Method: http.MethodDelete, Path: signOutPath(t), Session: session}),
		apierr.CodeUnauthenticated)

	// The row was written once. A second `RevokeSession` for the same session moves `updated_at`
	// rather than failing on the primary key or appending a second row.
	err := h.store.Queries().RevokeSession(h.t.Context(), revokeParamsFor(t, h, session))
	require.NoError(t, err)
	n, err := h.store.Queries().CountSessionRevocations(h.t.Context(), sessionIDOf(t, h, session))
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "signing out twice wrote two rows")
}

// A personal access token has no browser session to end, and the registry says so: `signOut`
// declares no scope, so the capability floor refuses every token before the handler runs. This is
// the direction that matters — a stolen token must not be able to sign its owner out.
func TestSignOut_APersonalAccessToken_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(true)
	circleID := h.seedCircle("Sign Out")
	member := h.seedMember(circleID, authz.RoleOwner)

	got := h.do(request{
		Method: http.MethodDelete, Path: signOutPath(t), Token: h.seedToken(member, allScopes()...),
	})
	h.requireProblem(got, apierr.CodeSessionRequired)
}

// sessionIDOf decodes a cookie value the harness minted and returns the session id inside it.
func sessionIDOf(t *testing.T, h *harness, value string) string {
	t.Helper()
	s, err := h.codec.Decode(value, h.clock.Now())
	require.NoError(t, err)
	return s.ID
}

// revokeParamsFor rebuilds the write the handler performs, so a test can repeat it exactly.
func revokeParamsFor(t *testing.T, h *harness, value string) sqlitegen.RevokeSessionParams {
	t.Helper()
	s, err := h.codec.Decode(value, h.clock.Now())
	require.NoError(t, err)
	now := h.clock.Now()
	return sqlitegen.RevokeSessionParams{
		SessionID: s.ID,
		ExpiresAt: int64(s.ExpiresAt),
		CreatedAt: int64(now),
		UpdatedAt: int64(now),
	}
}
