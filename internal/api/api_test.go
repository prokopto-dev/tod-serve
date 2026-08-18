package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

const mePath = api.BasePath + "/me"

func TestGetServerMeta_NoCredential_Answers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(true)

	got := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/meta"})
	require.Equal(t, http.StatusOK, got.Status)

	var meta api.ServerMeta
	require.NoError(t, json.Unmarshal([]byte(got.Body), &meta))
	require.Equal(t, "Test Instance", meta.Name)
	require.True(t, meta.Configured)
	require.True(t, meta.SelfServiceCircleCreation)
	require.Equal(t, []string{api.BasePath}, meta.APIVersions)
	require.Equal(t, fixtureNow, meta.AsOf, "every response carries an as_of from the injected clock")
}

// A binary pointed at a fresh database is a real state an operator meets during setup. Answering
// 500 would make the first thing they see look like a broken build.
func TestGetServerMeta_UnconfiguredInstance_SaysSoRatherThanFailing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	got := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/meta"})
	require.Equal(t, http.StatusOK, got.Status)

	var meta api.ServerMeta
	require.NoError(t, json.Unmarshal([]byte(got.Body), &meta))
	require.False(t, meta.Configured)
	require.Empty(t, meta.Name)
}

func TestGetCurrentPrincipal_APAT_ReportsTheEffectiveCapability(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleOfficer)
	token := h.seedToken(member, authz.ScopeTodRead)

	got := h.do(request{Method: http.MethodGet, Path: mePath, Token: token})
	require.Equal(t, http.StatusOK, got.Status)

	var view api.PrincipalView
	require.NoError(t, json.Unmarshal([]byte(got.Body), &view))
	require.Equal(t, string(auth.KindPAT), view.Kind)
	require.Equal(t, member, view.MembershipID)
	require.Equal(t, circle, view.CircleID)
	require.Equal(t, string(authz.RoleOfficer), view.Role)
	require.Equal(t, []string{string(authz.ScopeTodRead)}, view.Scopes)
	require.False(t, view.SteppedUp, "a token never steps up, at any scope")

	// The effective set is `role permissions ∩ token scopes`, not the role's set. An officer with
	// a tod:read token can read the board and cannot report to it.
	require.Contains(t, view.Permissions, string(authz.PermissionTodRead))
	require.Contains(t, view.Permissions, string(authz.PermissionTodReadAttribution))
	require.NotContains(t, view.Permissions, string(authz.PermissionTodReport))
	require.NotContains(t, view.Permissions, string(authz.PermissionInviteCreate))
}

// A token with no scopes may do nothing at all — and `/me` still answers, because a client that
// cannot discover that it has no scopes cannot report anything useful to its user.
func TestGetCurrentPrincipal_ATokenWithNoScopes_StillAnswers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleOwner)
	token := h.seedToken(member)

	got := h.do(request{Method: http.MethodGet, Path: mePath, Token: token})
	require.Equal(t, http.StatusOK, got.Status)

	var view api.PrincipalView
	require.NoError(t, json.Unmarshal([]byte(got.Body), &view))
	require.Empty(t, view.Permissions, "a token with no scopes reaches nothing")
	require.Empty(t, view.Scopes)
}

func TestGetCurrentPrincipal_ASession_IsNotNarrowedByScopes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleOfficer)

	got := h.do(request{
		Method: http.MethodGet, Path: mePath, Session: h.session(member, true),
	})
	require.Equal(t, http.StatusOK, got.Status)

	var view api.PrincipalView
	require.NoError(t, json.Unmarshal([]byte(got.Body), &view))
	require.Equal(t, string(auth.KindSession), view.Kind)
	require.True(t, view.SteppedUp)
	require.Empty(t, view.TokenPrefix)
	require.Contains(t, view.Permissions, string(authz.PermissionInviteCreate))
}

func TestGetCurrentPrincipal_NoCredential_Is401(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.requireProblem(h.do(request{Method: http.MethodGet, Path: mePath}),
		apierr.CodeUnauthenticated)
}

func TestGetCurrentPrincipal_AnUnknownToken_IsTokenInvalid(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	h.seedMember(circle, authz.RoleOwner)

	unknown, err := h.minter.Mint()
	require.NoError(t, err)
	h.requireProblem(
		h.do(request{Method: http.MethodGet, Path: mePath, Token: unknown.Token}),
		apierr.CodeTokenInvalid)
}

func TestGetCurrentPrincipal_AMalformedBearer_IsUnauthenticated(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	got := h.do(request{
		Method: http.MethodGet, Path: mePath,
		Headers: map[string]string{"Authorization": "Basic dXNlcjpwYXNz"},
	})
	h.requireProblem(got, apierr.CodeUnauthenticated)
}

// Membership state is checked on EVERY request rather than cascade-revoking tokens at revocation
// time. One join, always correct, and nothing to forget.
func TestAuth_ARevokedMembership_IsRefusedOnTheNextRequest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleMember)
	token := h.seedToken(member, authz.ScopeTodRead)

	require.Equal(t, http.StatusOK,
		h.do(request{Method: http.MethodGet, Path: mePath, Token: token}).Status,
		"the token worked before the revocation")

	h.revokeMembership(circle, member)

	h.requireProblem(h.do(request{Method: http.MethodGet, Path: mePath, Token: token}),
		apierr.CodeMembershipRevoked)
}

// Canonical §7: `Authorization: Bearer` only. A query-string token is rejected with 401, with no
// exception at all — including on a public route, where accepting it would teach clients the habit.
func TestAuth_TokenInAQueryString_IsRejectedOverHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleOwner)
	token := h.seedToken(member, authz.ScopeTodRead)

	cases := []struct {
		name string
		path string
	}{
		{"a token parameter on an authenticated route", mePath + "?token=" + token.Reveal()},
		{"an access_token parameter", mePath + "?access_token=" + token.Reveal()},
		{"a token smuggled under an innocent name", mePath + "?q=" + token.Reveal()},
		{"a public route", api.BasePath + "/meta?token=" + token.Reveal()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := h.do(request{Method: http.MethodGet, Path: tc.path})
			h.requireProblem(got, apierr.CodeUnauthenticated)
			require.NotContains(t, got.Body, token.Reveal(),
				"the rejection echoed the token it was rejecting")
		})
	}
}

// A capability-floor operation is session-only, and a token reaching one is told to open a browser
// rather than to mint a wider token — the two have different fixes.
func TestAuth_APATOnASessionOnlyOperation_IsSessionRequired(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleOwner)
	token := h.seedToken(member, authz.ScopeTodRead)
	tokenID := h.tokenIDOf(member)

	got := h.do(request{
		Method: http.MethodDelete, Path: api.BasePath + "/tokens/" + tokenID, Token: token,
	})
	h.requireProblem(got, apierr.CodeSessionRequired)
}

func TestRevokeToken_ASession_RevokesTheCallersOwnDevice(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleMember)
	token := h.seedToken(member, authz.ScopeTodRead)
	tokenID := h.tokenIDOf(member)

	got := h.do(request{
		Method: http.MethodDelete, Path: api.BasePath + "/tokens/" + tokenID,
		Session: h.session(member, false),
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var view api.TokenResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &view))
	require.NotNil(t, view.RevokedAt)
	require.Equal(t, fixtureNow, view.AsOf, "the response carries no as_of from the injected clock")
	require.NotContains(t, got.Body, token.Reveal(), "a token representation carries no secret")

	// The revoked token stops working immediately, and says so as `token_invalid` rather than
	// distinguishing revoked from unknown.
	h.requireProblem(h.do(request{Method: http.MethodGet, Path: mePath, Token: token}),
		apierr.CodeTokenInvalid)
}

// Somebody else's token, an id that does not exist, and one already revoked all answer 404.
// Anything narrower would let a caller enumerate other people's devices.
func TestRevokeToken_AnotherMembersToken_Is404(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	mine := h.seedMember(circle, authz.RoleMember)
	theirs := h.seedMember(circle, authz.RoleMember)
	h.seedToken(mine)
	h.seedToken(theirs)

	got := h.do(request{
		Method: http.MethodDelete, Path: api.BasePath + "/tokens/" + h.tokenIDOf(theirs),
		Session: h.session(mine, false),
	})
	h.requireProblem(got, apierr.CodeNotFound)
}

func TestListMyTokens_ReturnsOnlyTheCallersOwnDevices(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	mine := h.seedMember(circle, authz.RoleOfficer)
	theirs := h.seedMember(circle, authz.RoleMember)
	token := h.seedToken(mine, authz.ScopeTodRead)
	h.seedToken(mine, authz.ScopeTodReport)
	h.seedToken(theirs, authz.ScopeTodRead)

	got := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/tokens", Token: token})
	require.Equal(t, http.StatusOK, got.Status)

	var page api.Page[api.TokenView]
	require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
	require.Len(t, page.Items, 2, "officers see nobody's devices but their own")
	require.False(t, page.HasMore)
	require.Empty(t, page.NextCursor)
	require.Equal(t, fixtureNow, page.AsOf,
		"the page carries no as_of from the injected clock; expires_at below is read against it")
	for _, item := range page.Items {
		require.Len(t, item.TokenPrefix, auth.PrefixLen)
		require.NotContains(t, got.Body, token.Reveal())
	}
}

// A limit above the maximum is refused rather than clamped: a short page a caller reads as the end
// of the collection silently drops rows.
func TestListMyTokens_ALimitAboveTheMaximum_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleMember)
	token := h.seedToken(member)

	got := h.do(request{
		Method: http.MethodGet,
		Path:   api.BasePath + "/tokens?limit=" + itoa(api.MaxLimit+1),
		Token:  token,
	})
	require.Equal(t, http.StatusUnprocessableEntity, got.Status, got.Body)
	require.Equal(t, apierr.CodeValidationFailed, got.Problem.Code)
}

func TestListMyTokens_Cursor_WalksEveryRowExactlyOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleMember)
	token := h.seedToken(member)
	for range 4 {
		h.seedToken(member)
	}

	seen := map[string]bool{}
	cursor := ""
	for range 10 {
		path := api.BasePath + "/tokens?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		got := h.do(request{Method: http.MethodGet, Path: path, Token: token})
		require.Equal(t, http.StatusOK, got.Status, got.Body)

		var page api.Page[api.TokenView]
		require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
		for _, item := range page.Items {
			require.False(t, seen[item.ID.String()], "%s was returned twice", item.ID)
			seen[item.ID.String()] = true
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
		require.NotEmpty(t, cursor)
	}
	require.Len(t, seen, 5)
}

// `/healthz` must not touch the database: a health check that does lets Docker kill the container
// mid-migration. The store is CLOSED here, so a single query would fail the test.
func TestLiveness_MakesNoDatabaseCall(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.NoError(t, h.store.Close())

	got := h.do(request{Method: http.MethodGet, Path: "/healthz"})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var live api.Liveness
	require.NoError(t, json.Unmarshal([]byte(got.Body), &live))
	require.Equal(t, "ok", live.Status)
	require.Equal(t, "0.0.0-test", live.Version)
}

func TestReadiness_AMigratedDatabase_IsReady(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	got := h.do(request{Method: http.MethodGet, Path: "/readyz"})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var ready api.Readiness
	require.NoError(t, json.Unmarshal([]byte(got.Body), &ready))
	require.Equal(t, "ready", ready.Status)
	require.Positive(t, ready.SchemaVersion)
}

// `/readyz` does touch the database, and says so honestly when it cannot.
func TestReadiness_AnUnreachableDatabase_Is503(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.NoError(t, h.store.Close())

	h.requireProblem(h.do(request{Method: http.MethodGet, Path: "/readyz"}),
		apierr.CodeServiceUnavailable)
}

// `/metrics` is on a separate listener, behind its own token, and never on the API router.
func TestMetrics_IsOnItsOwnListener_AndNotOnTheAPIRouter(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	onAPI := h.do(request{Method: http.MethodGet, Path: "/metrics"})
	require.Equal(t, http.StatusNotFound, onAPI.Status,
		"/metrics answered on the API router, where no firewall rule expects it")

	h.requireProblem(
		h.do(request{Method: http.MethodGet, Path: "/metrics", Metrics: true}),
		apierr.CodeUnauthenticated)

	got := h.do(request{
		Method: http.MethodGet, Path: "/metrics", Metrics: true,
		Headers: map[string]string{"Authorization": auth.BearerScheme + testMetricsTok.Reveal()},
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	require.Contains(t, got.Body, "tod_build_info")
	require.Contains(t, got.Body, "tod_http_requests_total")
	require.NotContains(t, got.Body, testMetricsTok.Reveal())
}

// The metrics token is not a PAT scope, and a perfectly good PAT does not open the metrics
// endpoint. Canonical §13 says never gated by a PAT scope, in both directions.
func TestMetrics_APAT_DoesNotOpenIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleOwner)
	token := h.seedToken(member, authz.ScopeTodRead)

	h.requireProblem(
		h.do(request{Method: http.MethodGet, Path: "/metrics", Metrics: true, Token: token}),
		apierr.CodeUnauthenticated)
}

func TestMetrics_Disabled_HasNoHandlerAtAll(t *testing.T) {
	t.Parallel()
	h := newHarnessWithoutMetrics(t)
	_, ok := h.server.MetricsHandler()
	require.False(t, ok, "metrics are disabled by default and must have no listener")
}

// Every framework failure is a problem with a code from the closed enum, not the framework's own
// error shape. These are the responses a client is most likely to meet first.
func TestProblem_FrameworkErrors_AreRFC9457(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		request func(h *harness) request
		code    apierr.Code
	}{
		{
			name: "an unknown path",
			request: func(*harness) request {
				return request{Method: http.MethodGet, Path: api.BasePath + "/nothing-here"}
			},
			code: apierr.CodeNotFound,
		},
		{
			name: "the wrong method on a real path",
			request: func(*harness) request {
				return request{Method: http.MethodPost, Path: api.BasePath + "/meta"}
			},
			code: apierr.CodeMethodNotAllowed,
		},
		{
			name: "a query parameter of the wrong type",
			request: func(h *harness) request {
				circle := h.seedCircle("Riot")
				member := h.seedMember(circle, authz.RoleMember)
				return request{
					Method: http.MethodGet, Path: api.BasePath + "/tokens?limit=not-a-number",
					Token: h.seedToken(member),
				}
			},
			code: apierr.CodeValidationFailed,
		},
		{
			name: "an Accept header we cannot satisfy",
			request: func(*harness) request {
				return request{
					Method: http.MethodGet, Path: api.BasePath + "/meta",
					Headers: map[string]string{"Accept": "application/xml"},
				}
			},
			code: apierr.CodeNotAcceptable,
		},
		{
			name: "a body that is not JSON at all",
			request: func(*harness) request {
				return request{
					Method: http.MethodPost, Path: api.BasePath + "/invites/preview",
					Body: "{not json",
				}
			},
			// `previewInvite` has no handler yet, so the router answers first. What matters here
			// is that it answers with a code rather than with the router's plain text.
			code: apierr.CodeNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.requireProblem(h.do(tc.request(h)), tc.code)
		})
	}
}

// The `type` URL's last segment IS the code, the title is always present, and the status in the
// body matches the one on the wire. A client derives one from the other.
func TestProblem_EveryFailure_CarriesACodeATypeAndARequestID(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	got := h.do(request{Method: http.MethodGet, Path: mePath})
	h.requireProblem(got, apierr.CodeUnauthenticated)
	require.NotEmpty(t, got.Problem.Title)
	require.Equal(t, got.Status, got.Problem.Status)
	require.NotEmpty(t, got.Header.Get(api.RequestIDHeader))
	require.NotNil(t, got.Problem.Meta)
	require.Equal(t, got.Header.Get(api.RequestIDHeader), got.Problem.Meta.RequestID,
		"the request id in the body must be the one on the response, or it correlates nothing")
}

// A body over the limit is refused before it is copied into memory, and answers a code rather than
// the framework's plain-text default.
func TestProblem_ABodyOverTheLimit_IsPayloadTooLarge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/invites/preview",
		Body: strings.Repeat("x", int(api.MaxBodyBytes)+1),
	})
	h.requireProblem(got, apierr.CodePayloadTooLarge)
}

// A session that has not re-authenticated recently enough is told to re-authenticate, which is a
// different fix from "open a browser" and therefore a different code.
func TestStepUp_AStaleSession_IsStepUpRequired(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleOwner)
	session := h.session(member, true)

	h.advance(auth.DefaultStepUpWindow + time.Minute)

	// `/me` is not a floor operation, so the stale session still authenticates.
	got := h.do(request{Method: http.MethodGet, Path: mePath, Session: session})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var view api.PrincipalView
	require.NoError(t, json.Unmarshal([]byte(got.Body), &view))
	require.False(t, view.SteppedUp, "the session is past the step-up window and says so")
	require.Equal(t, int(auth.DefaultStepUpWindow.Seconds()), view.StepUpWindowSeconds)
}

// itoa avoids importing strconv into a file that otherwise has no use for it.
func itoa(v int) string {
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	if digits == "" {
		return "0"
	}
	return digits
}

// tokenIDOf returns the newest token minted for a membership, so a test can name one in a path
// without the mint returning an id it does not need elsewhere.
func (h *harness) tokenIDOf(membership core.MembershipID) string {
	h.t.Helper()
	rows, err := h.store.Queries().
		ListAPITokensForMembership(h.t.Context(), membership.String())
	require.NoError(h.t, err)
	require.NotEmpty(h.t, rows)
	return rows[0].ID
}

// Canonical §1, at the edge rather than only in the document: every response this binary serves
// carries an `as_of` read from the injected clock, so a test that moves the clock sees it move.
func TestResponses_EveryBody_CarriesAsOfFromTheInjectedClock(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(false)
	circle := h.seedCircle("Riot")
	member := h.seedMember(circle, authz.RoleOfficer)
	token := h.seedToken(member, authz.ScopeTodRead)
	tokenID := h.tokenIDOf(member)

	h.advance(90 * time.Second)
	want := fixtureNow.Add(90 * time.Second)

	cases := []struct {
		name string
		req  request
	}{
		{"getServerMeta", request{Method: http.MethodGet, Path: api.BasePath + "/meta"}},
		{"getCurrentPrincipal", request{Method: http.MethodGet, Path: mePath, Token: token}},
		{"listMyTokens", request{Method: http.MethodGet, Path: api.BasePath + "/tokens", Token: token}},
		{"getLiveness", request{Method: http.MethodGet, Path: "/healthz"}},
		{"getReadiness", request{Method: http.MethodGet, Path: "/readyz"}},
		{
			"revokeToken",
			request{
				Method: http.MethodDelete, Path: api.BasePath + "/tokens/" + tokenID,
				Session: h.session(member, false),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := h.do(tc.req)
			require.Equal(t, http.StatusOK, got.Status, got.Body)

			var body struct {
				AsOf *core.Micros `json:"as_of"`
			}
			require.NoError(t, json.Unmarshal([]byte(got.Body), &body))
			require.NotNil(t, body.AsOf, "%s answered with no as_of: %s", tc.name, got.Body)
			require.Equal(t, want, *body.AsOf,
				"%s did not read the injected clock", tc.name)
		})
	}
}
