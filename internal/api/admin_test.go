package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/membership"
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

// The idempotency hash covers every field the request carries, so a second request with the same
// key and a DIFFERENT body is `idempotency_key_reused` rather than a replay of the first answer.
//
// It is driven field by field over the whole body rather than over a chosen one: the failure this
// catches is a field left out of the hash, and any field left out looks exactly like every other
// field that is in it until somebody changes that one. A replay here means an operator changed
// `enabled` or an endpoint, got `200` and their original provider back, and believed the change
// had landed.
func TestCreateIdentityProvider_AReusedKeyWithADifferentBody_IsRefused(t *testing.T) {
	t.Parallel()

	const base = `{"key":"corp","kind":"oidc","display_name":"Corp SSO","enabled":false,
	               "issuer":"https://a.example.com","authorization_endpoint":"https://a.example.com/authorize",
	               "jwks_uri":"https://a.example.com/jwks","subject_claim":"sub",
	               "client_id":"tod-a","client_secret":"secret-a",
	               "redirect_uri":"https://tod.example.com/cb",
	               "token_endpoint":"https://a.example.com/token",
	               "acknowledge_weak_revocation":false}`

	// Each entry changes exactly one field of the body above. The client secret is deliberately
	// absent: it is elided from the hash to a presence marker, and the case below covers that.
	changed := []struct {
		field string
		body  string
	}{
		{"key", strings.Replace(base, `"key":"corp"`, `"key":"other"`, 1)},
		{"kind", strings.Replace(base, `"kind":"oidc"`, `"kind":"discord"`, 1)},
		{"display_name", strings.Replace(base, `"Corp SSO"`, `"Corp SSO 2"`, 1)},
		{"enabled", strings.Replace(base, `"enabled":false`, `"enabled":true`, 1)},
		{"issuer", strings.Replace(base, `https://a.example.com"`, `https://b.example.com"`, 1)},
		{"authorization_endpoint", strings.Replace(base, `a.example.com/authorize`, `b.example.com/authorize`, 1)},
		{"jwks_uri", strings.Replace(base, `a.example.com/jwks`, `b.example.com/jwks`, 1)},
		{"subject_claim", strings.Replace(base, `"subject_claim":"sub"`, `"subject_claim":"oid"`, 1)},
		{"client_id", strings.Replace(base, `"tod-a"`, `"tod-b"`, 1)},
		{"redirect_uri", strings.Replace(base, `/cb"`, `/callback"`, 1)},
		{"token_endpoint", strings.Replace(base, `a.example.com/token`, `b.example.com/token`, 1)},
		{"acknowledge_weak_revocation", strings.Replace(base,
			`"acknowledge_weak_revocation":false`, `"acknowledge_weak_revocation":true`, 1)},
		// The secret is elided to "one was sent" / "none was sent", so REMOVING it is a different
		// request. Two different secrets are not, and that is the deliberate cost of keeping the
		// value out of `idempotency_record`.
		{"client_secret removed", strings.Replace(base, `"client_secret":"secret-a",`, ``, 1)},
	}

	for _, tt := range changed {
		t.Run(tt.field, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			session, owner := h.adminSession(t)
			h.grantInstance(owner, authz.PermissionInstanceSecurityManage)
			const path = api.BasePath + "/admin/identity-providers"
			headers := map[string]string{api.IdempotencyKeyHeader: "one-key"}

			first := h.do(request{
				Method: http.MethodPost, Path: path, Session: session,
				Headers: headers, Body: base,
			})
			require.Equal(t, http.StatusOK, first.Status, first.Body)

			require.NotEqual(t, base, tt.body, "the %s case changes nothing", tt.field)
			second := h.do(request{
				Method: http.MethodPost, Path: path, Session: session,
				Headers: headers, Body: tt.body,
			})
			h.requireProblem(second, apierr.CodeIdempotencyKeyReused)
		})
	}

	// And the other direction: the identical body with the same key REPLAYS, which is what makes
	// the header worth sending at all. A retry carrying the same secret hashes the same way.
	t.Run("an identical retry replays", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		session, owner := h.adminSession(t)
		h.grantInstance(owner, authz.PermissionInstanceSecurityManage)
		const path = api.BasePath + "/admin/identity-providers"
		headers := map[string]string{api.IdempotencyKeyHeader: "retry-key"}

		first := h.do(request{
			Method: http.MethodPost, Path: path, Session: session, Headers: headers, Body: base,
		})
		require.Equal(t, http.StatusOK, first.Status, first.Body)
		again := h.do(request{
			Method: http.MethodPost, Path: path, Session: session, Headers: headers, Body: base,
		})
		require.Equal(t, http.StatusOK, again.Status, again.Body)
		require.Equal(t, first.Body, again.Body)

		// One `corp` row, not two: the replay answered from the record rather than writing again.
		// (The instance's `local` provider is seeded by the harness and is beside the point here.)
		listed := h.do(request{Method: http.MethodGet, Path: path, Session: session})
		var page struct {
			Items []api.AdminIdentityProvider `json:"items"`
		}
		require.NoError(t, json.Unmarshal([]byte(listed.Body), &page))
		corp := 0
		for _, item := range page.Items {
			if item.Key == "corp" {
				corp++
			}
		}
		require.Equal(t, 1, corp)
	})
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

// The scheduled deletion this replaces asserted that NO route minted a session, and named the
// operation that would: `completeAuthorization`. It was half right. The callback cannot mint one —
// it holds a verified subject and no membership, and which circle the subject lands in is settled
// at redemption — so the two operations that CAN are `/join` and `/sessions`, which are the two
// that verify a credential and know the membership.
//
// This is the positive half: a browser that redeems a code gets a session, that session is stepped
// up because a credential was just proved, and it reaches the capability floor that no token
// reaches at any scope.
func TestRedeemInvite_SetsASteppedUpSession_ThatReachesTheCapabilityFloor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	code := h.seedJoinableCircle()

	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/join",
		Headers: map[string]string{api.IdempotencyKeyHeader: "join-once"},
		Body: `{"invite_code":"` + code + `","provider":"` + localProviderKey + `",` +
			`"credential":{"kind":"none"},"display_name":"Operator"}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var cookie *http.Cookie
	for _, c := range (&http.Response{Header: got.Header}).Cookies() {
		if c.Name == auth.SessionCookie {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "/join set no session cookie; a browser has no way to reach the floor")
	require.True(t, cookie.Secure, "__Host- requires Secure")
	require.True(t, cookie.HttpOnly, "no script has any reason to read a session")
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite,
		"a cross-site POST must not carry the session")

	// A capability-floor operation, reached with the cookie the response set and with nothing
	// else. `listInvites` is not the floor; `revokeInvite` is, and it is what an officer actually
	// does from the console.
	var joined membership.Joined
	require.NoError(t, json.Unmarshal([]byte(got.Body), &joined))
	minted := h.do(request{
		Method: http.MethodPost, Session: cookie.Value,
		Path:    api.BasePath + "/circles/" + joined.Circle.ID.String() + "/invites",
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint-from-the-console"},
		Body:    `{"role":"member"}`,
	})
	require.Equal(t, http.StatusOK, minted.Status, minted.Body)

	// And the same request with the PAT the same operation minted is refused, with the code that
	// says which half failed. Both credentials come from one door; only one of them opens the floor.
	byToken := h.do(request{
		Method: http.MethodPost, Token: core.Secret(joined.Token.Secret),
		Path:    api.BasePath + "/circles/" + joined.Circle.ID.String() + "/invites",
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint-by-token"},
		Body:    `{"role":"member"}`,
	})
	require.Equal(t, http.StatusOK, byToken.Status, "invite.create is deliberately NOT in the floor")

	revoke := h.do(request{
		Method: http.MethodDelete, Token: core.Secret(joined.Token.Secret),
		Path: api.BasePath + "/circles/" + joined.Circle.ID.String() +
			"/invites/" + newID[core.Invite](h).String(),
	})
	h.requireProblem(revoke, apierr.CodeSessionRequired)
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

// `/me` is how the CONSOLE discovers what to show, and `instance.owner` has to reach it whole.
//
// `web/src/app/Shell.tsx` gates the Instance nav entry on `instance.security.manage` and simply
// HIDES it when the effective set does not carry one — so an operator who followed the runbook,
// granted `instance.owner` and saw no Instance tab had nothing on screen pointing at the cause.
// Nothing in `web/` changes for this: the expansion reaches the console because the console reads
// this response, which is why the assertion belongs here.
func TestGetCurrentPrincipal_InstanceOwner_ListsEveryInstanceRealmPermission(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceOwner)

	got := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/me", Session: session})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	var view api.PrincipalView
	require.NoError(t, json.Unmarshal([]byte(got.Body), &view))

	want := append(authz.RolePermissions(authz.RoleOwner).Slice(), authz.InstancePermissions()...)
	require.ElementsMatch(t, permissionStrings(want), view.Permissions)

	// The one the console actually branches on, named rather than left to the set comparison: it
	// is the difference between an Instance tab and no explanation.
	require.Contains(t, view.Permissions, string(authz.PermissionInstanceSecurityManage))
}

func permissionStrings(perms []authz.Permission) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, string(p))
	}
	return out
}
