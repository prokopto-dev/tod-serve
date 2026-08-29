package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// TestTenancy_BeatsEveryOtherRefusal_OnEveryCircleScopedRoute asserts the ORDER inside
// `Builder.authorize`, over the whole registry rather than over one route.
//
// The order is the rule: tenancy, then session-only, then step-up, then role, then scope. Every
// one of the four refusals below names a different code, and every one of them would confirm that
// the circle in the path EXISTS — which is the thing canonical §7 hides and the reason law 5 says
// 404 and never 403. A caller who learns "you need to re-authenticate" about circle B has learned
// that circle B is there.
//
// TestTenancy_IsDecidedBeforePermission covers one of the four, on one route, against a stub
// handler. This drives all four against every served circle-scoped route on the real server, so a
// route whose shape reaches a different branch of that switch — session-only, or step-up, or
// neither — is covered by the shape rather than by somebody having thought of it.
//
// Each credential is deliberately deficient in a DIFFERENT way, and each would produce its own
// non-404 answer inside the caller's own circle. That is asserted too, at the bottom: a credential
// that was simply broken would make every case here pass for no reason.
func TestTenancy_BeatsEveryOtherRefusal_OnEveryCircleScopedRoute(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// An OBSERVER, which is the role that holds least. Every deficiency below stacks on top of a
	// role that already fails the permission check for most of these operations.
	mine := h.seedCircle("Mine")
	observer := h.seedMember(mine, authz.RoleObserver)

	theirs := h.seedCircle("Theirs")
	h.seedMember(theirs, authz.RoleOwner)

	deficient := []struct {
		name string
		// wouldBe is the code this credential earns INSIDE its own circle, and therefore the code
		// that must not leak out of another one.
		wouldBe apierr.Code
		cred    credential
	}{
		{
			name:    "a token carrying no scopes at all",
			wouldBe: apierr.CodeInsufficientScope,
			cred:    credential{name: "pat-no-scopes", token: h.seedToken(observer)},
		},
		{
			name:    "a token on an operation no token reaches at any scope",
			wouldBe: apierr.CodeSessionRequired,
			cred:    credential{name: "pat-all-scopes", token: h.seedToken(observer, allScopes()...)},
		},
		{
			name:    "a session that has not re-authenticated recently",
			wouldBe: apierr.CodeStepUpRequired,
			cred:    credential{name: "session-stale", session: h.session(observer, false)},
		},
		{
			name:    "a session whose role holds nothing",
			wouldBe: apierr.CodeForbidden,
			cred:    credential{name: "session-fresh", session: h.session(observer, true)},
		},
	}

	served := map[api.OperationID]bool{}
	for _, id := range h.server.Registered() {
		served[id] = true
	}

	sessionOnly, stepUp, driven := 0, 0, 0
	for _, route := range api.CircleScopedRoutes() {
		if !served[route.ID] {
			continue
		}
		if route.SessionOnly() {
			sessionOnly++
		}
		if route.RequiresStepUp() {
			stepUp++
		}
		driven++

		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			for _, d := range deficient {
				got := h.do(request{
					Method: route.Method,
					Path:   theirCirclePath(route, theirs),
					Token:  d.cred.token, Session: d.cred.session, Body: bodyFor(route),
					Headers: map[string]string{
						api.IdempotencyKeyHeader: string(route.ID) + "-" + d.cred.name,
						api.IfMatchHeader:        "*",
					},
				})
				require.Equal(t, http.StatusNotFound, got.Status,
					"%s answered %d to %s pointed at another circle. Every refusal that is not "+
						"404 confirms the circle exists to the caller who should learn least. "+
						"Body: %s", route.ID, got.Status, d.name, got.Body)
				require.Equal(t, apierr.CodeNotFound, got.Problem.Code,
					"%s answered %q to %s pointed at another circle; %q leaks the circle",
					route.ID, got.Problem.Code, d.name, d.wouldBe)
			}
		})
	}

	// The branches only get exercised if routes of each shape exist. Without this the test would
	// keep passing after the last session-only or step-up circle-scoped route went away, and
	// nobody would know it had stopped testing the ordering it is named for.
	require.Positive(t, driven, "no served circle-scoped routes; the filter is wrong")
	require.Positive(t, sessionOnly,
		"no served circle-scoped route is session-only, so the session_required branch of the "+
			"ordering was never reached")
	require.Positive(t, stepUp,
		"no served circle-scoped route requires step-up, so the step_up_required branch of the "+
			"ordering was never reached")
	t.Logf("%d circle-scoped operations, %d of them session-only and %d step-up, each driven "+
		"with four differently-deficient credentials", driven, sessionOnly, stepUp)
}

// Each deficient credential really does earn its own refusal inside the caller's OWN circle.
//
// This is the half that stops the test above passing for the wrong reason. Four credentials that
// were merely invalid would answer 401 everywhere, and every 404 above would be a 404 about a
// broken caller rather than about tenancy. Here the same four are pointed at their own circle and
// required to produce four DIFFERENT codes — which is also the assertion that the ordering has
// four distinct branches to get wrong in the first place.
func TestAuthorize_WithinTheCircle_EachDeficiency_EarnsItsOwnCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	observer := h.seedMember(mine, authz.RoleObserver)
	officer := h.seedMember(mine, authz.RoleOfficer)

	cases := []struct {
		name   string
		path   string
		method string
		token  core.Secret
		sess   string
		want   apierr.Code
	}{
		{
			// The role holds `tod.report` and the token's scopes do not reach it: mint a scope.
			name: "a token whose scopes do not reach the operation",
			path: reportsPath(mine), method: http.MethodPost,
			token: h.seedToken(officer), want: apierr.CodeInsufficientScope,
		},
		{
			// `member.revoke` is in the capability floor: no token reaches it at any scope.
			name: "a token on a capability-floor operation",
			path: membersPath(mine) + "/" + officer.String() + "/revoke", method: http.MethodPost,
			token: h.seedToken(officer, allScopes()...), want: apierr.CodeSessionRequired,
		},
		{
			name: "a session that has not re-authenticated recently",
			path: membersPath(mine) + "/" + officer.String() + "/revoke", method: http.MethodPost,
			sess: h.session(officer, false), want: apierr.CodeStepUpRequired,
		},
		{
			// An observer's ROLE does not hold `tod.report`: the fix is a role, not a scope.
			name: "a session whose role does not hold the permission",
			path: reportsPath(mine), method: http.MethodPost,
			sess: h.session(observer, true), want: apierr.CodeForbidden,
		},
	}

	seen := map[apierr.Code]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := h.do(request{
				Method: tc.method, Path: tc.path, Token: tc.token, Session: tc.sess, Body: "{}",
				Headers: map[string]string{
					api.IdempotencyKeyHeader: "own-circle",
					api.IfMatchHeader:        "*",
				},
			})
			require.Equal(t, tc.want, got.Problem.Code,
				"inside the caller's own circle, %s must answer %q. If it does not, the "+
					"cross-circle test above is asserting 404 about a credential that was "+
					"simply broken. Body: %s", tc.name, tc.want, got.Body)
		})
		seen[tc.want] = true
	}
	require.Len(t, seen, len(cases), "two cases assert the same code; one of them proves nothing")
}

// theirCirclePath points a route at another circle, with a well-formed placeholder for every other
// path parameter so a 404 is the tenancy answer rather than a parse failure wearing its clothes.
func theirCirclePath(r api.Route, theirs core.CircleID) string {
	return fillRemainingPathParams(
		strings.ReplaceAll(r.FullPath(), api.CirclePathParam, theirs.String()))
}

// The strongest principal this instance can produce still gets 404 on another circle.
//
// `instance.owner` expands to the whole instance realm (ADR-0015), so this caller holds every
// instance-realm key there is, on a session, freshly stepped up — every narrowing in
// `Builder.authorize` satisfied except the first one. Nothing in the product is more privileged.
//
// It matters because the shape of the argument that keeps it out is indirect:
// TestRouteRegistry_EveryInstanceRealmRoute_IsSessionOnly asserts that no instance-realm route is
// circle-scoped, and `checkTenancy` compares the path against the membership's circle before any
// permission is consulted. Both halves are true today and neither says, by itself, that an
// instance owner cannot read circle B — that conclusion is drawn by a reader joining them up.
// This asserts it as behaviour, over every circle-scoped route, so a future instance-realm key
// that reached a circle route would be a red test rather than a re-derivation nobody redid.
func TestTenancy_AnInstanceOwner_StillGets404OnAnotherCircle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	h.grantInstance(owner, authz.PermissionInstanceOwner)
	session := h.session(owner, true)

	theirs := h.seedCircle("Theirs")
	h.seedMember(theirs, authz.RoleOwner)

	// The grant is real and reaches something, so a 404 below is tenancy rather than a grant that
	// silently did nothing. `listAdminIdentityProviders` needs `instance.security.manage`, which
	// this caller holds only through the expansion.
	admin := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/admin/identity-providers", Session: session,
	})
	require.Equal(t, http.StatusOK, admin.Status,
		"the instance grant reached nothing, so every 404 below would prove nothing: %s", admin.Body)

	served := map[api.OperationID]bool{}
	for _, id := range h.server.Registered() {
		served[id] = true
	}
	driven := 0
	for _, route := range api.CircleScopedRoutes() {
		if !served[route.ID] {
			continue
		}
		driven++
		got := h.do(request{
			Method: route.Method, Path: theirCirclePath(route, theirs),
			Session: session, Body: bodyFor(route),
			Headers: map[string]string{
				api.IdempotencyKeyHeader: string(route.ID) + "-instance-owner",
				api.IfMatchHeader:        "*",
			},
		})
		require.Equal(t, http.StatusNotFound, got.Status,
			"%s answered %d to an instance owner asking about another circle. An instance grant "+
				"is about the instance's configuration, never about one circle's data — the "+
				"README promises the operator can read the database, not that the API hands a "+
				"circle over. Body: %s", route.ID, got.Status, got.Body)
		require.Equal(t, apierr.CodeNotFound, got.Problem.Code)
	}
	require.Positive(t, driven, "no served circle-scoped routes; the filter is wrong")
	t.Logf("an identity holding instance.owner was refused %d circle-scoped operations", driven)
}

// The same grant, presented by a TOKEN, reaches none of it. ADR-0012's floor, over HTTP.
//
// `auth.Principal.Holds` refuses an instance-realm key on `Kind == KindPAT` whatever the ledger
// says, and the authenticator never populates `InstanceGrants` on the token path — two mechanisms,
// and the second is the one a refactor removes. Driving it here means the guarantee is checked
// where a client would actually meet it rather than only on the struct.
func TestInstanceGrant_APAT_ReachesNoInstanceRealmRouteAtAnyScope(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	h.grantInstance(owner, authz.PermissionInstanceOwner)

	// Every scope in the catalogue, so the refusal is about the credential KIND and never about a
	// scope this token happened not to carry.
	token := h.seedToken(owner, allScopes()...)

	served := map[api.OperationID]bool{}
	for _, id := range h.server.Registered() {
		served[id] = true
	}

	driven := 0
	for _, route := range api.Routes() {
		if !served[route.ID] {
			// `getDoctorReport` and `listJobs` are in the registry and have no handler yet, so
			// the router answers 404 before any credential is examined. Asserting
			// `session_required` against that would be asserting the absence of a route.
			continue
		}
		instanceRealm := false
		for _, p := range route.Permissions {
			if authz.IsInstanceRealm(p) {
				instanceRealm = true
			}
		}
		if !instanceRealm {
			continue
		}
		driven++
		got := h.do(request{
			Method: route.Method, Path: fillRemainingPathParams(route.FullPath()),
			Token: token, Body: bodyFor(route),
			Headers: map[string]string{
				api.IdempotencyKeyHeader: string(route.ID) + "-pat-instance",
				api.IfMatchHeader:        "*",
			},
		})
		require.Equal(t, apierr.CodeSessionRequired, got.Problem.Code,
			"%s admitted a token whose identity holds instance.owner. An instance grant hangs "+
				"off an IDENTITY and a token is bound to a MEMBERSHIP (ADR-0005), so a leaked "+
				"token must reach none of them however the ledger reads. Body: %s",
			route.ID, got.Body)
	}
	require.Positive(t, driven, "no instance-realm routes; the filter is wrong")
	t.Logf("a token whose identity holds instance.owner was refused %d instance-realm operations",
		driven)
}
