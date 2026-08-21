package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// adminSession seeds an owner in a circle and returns a stepped-up session for them, plus their
// membership, so a test can decide whether to grant anything.
func (h *harness) adminSession(t *testing.T) (string, core.MembershipID) {
	t.Helper()
	circleID := h.seedCircle("Ops")
	owner := h.seedMember(circleID, authz.RoleOwner)
	return h.session(owner, true), owner
}

// The provider registry is instance-realm: `instance.security.manage` reaches it, no circle role
// grants that, and no PAT reaches it at any scope. This drives all four operations from both
// sides — the same principal refused without a grant and served with one — because a test that
// only asserted the success half would pass with the permission check deleted.
func TestAdminIdentityProviders_AGrant_IsWhatMakesThemReachable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedProvider()
	session, owner := h.adminSession(t)
	const base = api.BasePath + "/admin/identity-providers"

	refused := h.do(request{Method: http.MethodGet, Path: base, Session: session})
	h.requireProblem(refused, apierr.CodeForbidden)

	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)

	listed := h.do(request{Method: http.MethodGet, Path: base, Session: session})
	require.Equal(t, http.StatusOK, listed.Status, listed.Body)
	var page struct {
		Items []api.AdminIdentityProvider `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(listed.Body), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, "local", page.Items[0].Key)

	created := h.do(request{
		Method: http.MethodPost, Path: base, Session: session,
		Headers: map[string]string{api.IdempotencyKeyHeader: "add-oidc"},
		Body: `{"key":"corp","kind":"oidc","display_name":"Corp SSO",
		        "issuer":"https://sso.example.com","jwks_uri":"https://sso.example.com/jwks",
		        "client_id":"tod-serve"}`,
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)
	var provider api.AdminIdentityProviderResponse
	require.NoError(t, json.Unmarshal([]byte(created.Body), &provider))
	require.Equal(t, "corp", provider.Key)
	// Added disabled: a half-configured OAuth application must not be briefly live.
	require.False(t, provider.Enabled)

	updated := h.do(request{
		Method: http.MethodPatch, Path: base + "/" + provider.ID, Session: session,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"display_name":"Corp SSO (staging)"}`,
	})
	require.Equal(t, http.StatusOK, updated.Status, updated.Body)
	require.Contains(t, updated.Body, "Corp SSO (staging)")
	require.NotEmpty(t, updated.Header.Get(api.ETagHeader))

	removed := h.do(request{
		Method: http.MethodDelete, Path: base + "/" + provider.ID, Session: session,
	})
	require.Equal(t, http.StatusOK, removed.Status, removed.Body)

	gone := h.do(request{Method: http.MethodGet, Path: base + "/" + provider.ID, Session: session})
	// There is no getIdentityProvider operation, so the router answers rather than a handler. The
	// point of the probe is that the row is gone from the listing below.
	require.Equal(t, http.StatusMethodNotAllowed, gone.Status, gone.Body)

	after := h.do(request{Method: http.MethodGet, Path: base, Session: session})
	require.NoError(t, json.Unmarshal([]byte(after.Body), &page))
	require.Len(t, page.Items, 1)
}

// The whole reason a leaked PAT must not reach `instance.security.manage`: adding a hostile OIDC
// issuer is a pivot into every identity on the instance. This checks the refusal is
// `session_required` rather than `forbidden`, because those point at different fixes.
func TestAdminIdentityProviders_AToken_ReachesThemAtNoScope(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedProvider()
	circleID := h.seedCircle("Ops")
	owner := h.seedMember(circleID, authz.RoleOwner)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)

	every := make([]authz.Scope, 0, len(authz.Scopes()))
	for _, def := range authz.Scopes() {
		every = append(every, def.Key)
	}
	token := h.seedToken(owner, every...)

	got := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/admin/identity-providers", Token: token,
	})
	h.requireProblem(got, apierr.CodeSessionRequired)
}

// A stale session is refused too, and differently again. `instance.security.manage` is in the
// capability floor: a grant is not a way past it.
func TestAdminIdentityProviders_AStaleSession_NeedsStepUp(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedProvider()
	circleID := h.seedCircle("Ops")
	owner := h.seedMember(circleID, authz.RoleOwner)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)

	got := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/admin/identity-providers",
		Session: h.session(owner, false),
	})
	h.requireProblem(got, apierr.CodeStepUpRequired)
}

// The client secret goes in and never comes out — not from the create, not from the read, not
// from the update that rotated it. The API says whether one is SET and nothing else.
func TestAdminIdentityProviders_TheClientSecret_NeverComesBackOut(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)
	const base = api.BasePath + "/admin/identity-providers"
	const secret = "super-secret-discord-value"
	const rotated = "rotated-discord-value"

	created := h.do(request{
		Method: http.MethodPost, Path: base, Session: session,
		Headers: map[string]string{api.IdempotencyKeyHeader: "secret-check"},
		Body: `{"key":"discord","kind":"discord","client_id":"1234","client_secret":"` +
			secret + `","redirect_uri":"https://tod.example.com/cb"}`,
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)
	require.NotContains(t, created.Body, secret)
	var provider api.AdminIdentityProviderResponse
	require.NoError(t, json.Unmarshal([]byte(created.Body), &provider))
	require.True(t, provider.ClientSecretSet)

	listed := h.do(request{Method: http.MethodGet, Path: base, Session: session})
	require.NotContains(t, listed.Body, secret)

	updated := h.do(request{
		Method: http.MethodPatch, Path: base + "/" + provider.ID, Session: session,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"client_secret":"` + rotated + `"}`,
	})
	require.Equal(t, http.StatusOK, updated.Status, updated.Body)
	require.NotContains(t, updated.Body, secret)
	require.NotContains(t, updated.Body, rotated)

	// And the rotation reached the database rather than being dropped along with the rendering.
	stored, err := h.store.Queries().GetIdentityProvider(h.t.Context(), provider.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.ClientSecret)
	require.Equal(t, rotated, *stored.ClientSecret)
}

// Enabling a provider with no verifiable subject takes an explicit acknowledgement, at the API as
// well as at the command line. The failure it guards is not technical: an officer revokes a
// leaker, the leaker redeems another invite as "Tanky", and the officers believe it worked.
//
// It runs against the instance's real `local` row rather than a fresh one, because there is at
// most one `local` provider — and turning an existing one back on is the transition an operator
// actually makes.
func TestAdminIdentityProviders_EnablingAWeakProvider_NeedsAnAcknowledgement(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	provider := h.seedProvider()
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)
	path := api.BasePath + "/admin/identity-providers/" + provider.String()

	// Turning it OFF needs no acknowledgement: nothing new can join through a disabled provider,
	// which is the direction that makes revocation stronger rather than weaker.
	off := h.do(request{
		Method: http.MethodPatch, Path: path, Session: session,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"enabled":false}`,
	})
	require.Equal(t, http.StatusOK, off.Status, off.Body)

	turnOn := h.do(request{
		Method: http.MethodPatch, Path: path, Session: session,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"enabled":true}`,
	})
	h.requireProblem(turnOn, apierr.CodeAcknowledgementRequired)

	acknowledged := h.do(request{
		Method: http.MethodPatch, Path: path, Session: session,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"enabled":true,"acknowledge_weak_revocation":true}`,
	})
	require.Equal(t, http.StatusOK, acknowledged.Status, acknowledged.Body)
	var view api.AdminIdentityProviderResponse
	require.NoError(t, json.Unmarshal([]byte(acknowledged.Body), &view))
	require.True(t, view.Enabled)
	// Still unverifiable. It is a CHECK against the kind and no request can move it.
	require.False(t, view.VerifiableSubject)

	// And an unrelated edit afterwards does NOT need it again. Re-acknowledging on every change
	// would train an operator to send the flag by reflex, which is the opposite of what it is for.
	renamed := h.do(request{
		Method: http.MethodPatch, Path: path, Session: session,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"display_name":"This server"}`,
	})
	require.Equal(t, http.StatusOK, renamed.Status, renamed.Body)
}

// At most one `discord` row and at most one `local` row, and the refusal names the row already
// there. The schema refuses it too; this is what makes the caller read a sentence rather than a
// constraint name behind a 500.
func TestCreateIdentityProvider_ASecondProviderOfAKind_IsRefusedWithItsName(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedProvider()
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)
	const base = api.BasePath + "/admin/identity-providers"

	got := h.do(request{
		Method: http.MethodPost, Path: base, Session: session,
		Headers: map[string]string{api.IdempotencyKeyHeader: "second-local"},
		Body:    `{"key":"lan","kind":"local"}`,
	})
	h.requireProblem(got, apierr.CodeConflict)
	require.Contains(t, got.Problem.Detail, `"local"`)

	// A second `oidc` is fine: an instance can federate with more than one issuer.
	first := h.do(request{
		Method: http.MethodPost, Path: base, Session: session,
		Headers: map[string]string{api.IdempotencyKeyHeader: "oidc-a"},
		Body: `{"key":"corp","kind":"oidc","issuer":"https://a.example.com",
		        "jwks_uri":"https://a.example.com/jwks","client_id":"tod-a"}`,
	})
	require.Equal(t, http.StatusOK, first.Status, first.Body)
	second := h.do(request{
		Method: http.MethodPost, Path: base, Session: session,
		Headers: map[string]string{api.IdempotencyKeyHeader: "oidc-b"},
		Body: `{"key":"partner","kind":"oidc","issuer":"https://b.example.com",
		        "jwks_uri":"https://b.example.com/jwks","client_id":"tod-b"}`,
	})
	require.Equal(t, http.StatusOK, second.Status, second.Body)
}

// A provider whose shape does not match its kind is refused with a message an operator can act on,
// not with "inconsistent with its kind". An `oidc` row with no client id has no audience to check,
// so an id token minted for a different relying party at the same issuer would verify here.
func TestCreateIdentityProvider_AKindMismatch_SaysWhatIsMissing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)

	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/admin/identity-providers",
		Session: session,
		Headers: map[string]string{api.IdempotencyKeyHeader: "bad-oidc"},
		Body: `{"key":"corp","kind":"oidc","issuer":"https://sso.example.com",
		        "jwks_uri":"https://sso.example.com/jwks"}`,
	})
	h.requireProblem(got, apierr.CodeValidationFailed)
	require.Contains(t, got.Problem.Detail, "client id")
}

// A provider somebody has joined through cannot be deleted, because foreign keys are NO ACTION
// everywhere and removing it would orphan their identities. The 409 says to disable it instead,
// which is the operation that actually stops new joins.
func TestDeleteIdentityProvider_OneWithIdentities_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	provider := h.seedProvider()
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)

	got := h.do(request{
		Method: http.MethodDelete, Session: session,
		Path: api.BasePath + "/admin/identity-providers/" + provider.String(),
	})
	h.requireProblem(got, apierr.CodeConflict)
	require.Contains(t, strings.ToLower(got.Problem.Detail), "disable it instead")
}

func TestAdminIdentityProviders_AnUnknownProvider_Is404(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)
	missing := newID[core.IdentityProvider](h).String()

	got := h.do(request{
		Method: http.MethodPatch, Session: session,
		Path:    api.BasePath + "/admin/identity-providers/" + missing,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"display_name":"Nope"}`,
	})
	h.requireProblem(got, apierr.CodeNotFound)
}

// TestSessionMinting_NoRouteMintsOne_IsStillTrue is a SCHEDULED DELETION, and it is the honest
// half of ADR-0012's operator story.
//
// The instance-realm routes are session-only, and `completeAuthorization` — the OAuth callback —
// is the operation that will set `__Host-tod_session`. It is in the route registry and served by
// nothing, so today the only way to hold a session is to encode one with the server's own codec,
// which is exactly what the end-to-end test in cmd/tod-serve does and says.
//
// This pins that gap so it is visible rather than assumed. When the OAuth flow lands, this goes
// red, and whoever lands it is sent to TestEndToEnd_FreshDatabaseToConfiguringAnIdentityProvider
// to replace the minted cookie with the real one.
func TestSessionMinting_NoRouteMintsOne_IsStillTrue(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	unimplemented := map[api.OperationID]bool{}
	for _, id := range h.server.Unimplemented() {
		unimplemented[id] = true
	}
	require.True(t, unimplemented[api.OpCompleteAuthorization],
		"completeAuthorization is served now, so a browser can obtain a real session: replace the "+
			"codec-minted cookie in the cmd/tod-serve end-to-end test and delete this test")

	// And the routes that need one are registered, so the gap is about the credential rather than
	// about the surface.
	registered := map[api.OperationID]bool{}
	for _, id := range h.server.Registered() {
		registered[id] = true
	}
	require.True(t, registered[api.OpListAdminIdentityProviders])
	require.True(t, registered[api.OpCreateIdentityProvider])
}

// `/me` is how a client discovers what it may do, and an instance grant has to reach it: an admin
// console that could not tell whether this operator holds `instance.security.manage` would have to
// probe the route and read the 403.
//
// It asserts the effective set is the ROLE'S PERMISSIONS PLUS THE GRANT and nothing else, so a
// union that reached into the role matrix — handing over whatever else the granted identity's
// circle role implied — is a red test rather than a surprise.
func TestGetCurrentPrincipal_AnInstanceGrant_ReachesTheEffectivePermissions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, owner := h.adminSession(t)

	before := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/me", Session: session})
	require.Equal(t, http.StatusOK, before.Status, before.Body)
	var view api.PrincipalView
	require.NoError(t, json.Unmarshal([]byte(before.Body), &view))
	require.ElementsMatch(t, permissionStrings(authz.RolePermissions(authz.RoleOwner).Slice()),
		view.Permissions)

	h.grantInstance(owner, authz.PermissionOpsRead)

	after := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/me", Session: session})
	require.NoError(t, json.Unmarshal([]byte(after.Body), &view))
	want := append(authz.RolePermissions(authz.RoleOwner).Slice(), authz.PermissionOpsRead)
	require.ElementsMatch(t, permissionStrings(want), view.Permissions)

	// A token belonging to the same identity's membership sees none of it: an instance grant is on
	// the identity and a token is bound to a membership.
	token := h.seedToken(owner, authz.ScopeCircleRead)
	byToken := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/me", Token: token})
	require.NoError(t, json.Unmarshal([]byte(byToken.Body), &view))
	require.NotContains(t, view.Permissions, string(authz.PermissionOpsRead))
}

func permissionStrings(perms []authz.Permission) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, string(p))
	}
	return out
}
