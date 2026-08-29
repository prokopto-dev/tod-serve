package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
)

const (
	providersPath = api.BasePath + "/identity-providers"
	authURLPath   = api.BasePath + "/auth/authorization-url"
)

// `listIdentityProviders` exists so a client can discover providers at RUNTIME. A console that
// hardcoded Discord would be a console an OIDC-only instance cannot sign into, and the operator
// who deployed it has no way to find that out except by watching people fail to log in.
//
// It is reachable with no credential at all, deliberately: it is what a browser reads before it
// holds anything.
func TestListIdentityProviders_NoCredential_ListsTheEnabledOnes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedProvider()

	got := h.do(request{Method: http.MethodGet, Path: providersPath})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var page api.Page[api.PublicIdentityProvider]
	require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, localProviderKey, page.Items[0].Key)
	require.Equal(t, "local", page.Items[0].Kind)
	// `verifiable_subject` is a CHECK against `kind`, never a toggle, and it is the whole of a
	// circle's revocation guarantee. A join page that cannot see it cannot tell anybody the truth.
	require.False(t, page.Items[0].VerifiableSubject)
	// `local` has no OAuth redirect to start. A client that could not tell offers a button that
	// goes nowhere.
	require.False(t, page.Items[0].BrowserFlow)
}

// A provider the operator has DISABLED admits nobody, so it is not offered. The instance's own
// admin endpoint still lists it — disabling is not deleting — which is why the two representations
// are different types rather than one with a flag.
func TestListIdentityProviders_ADisabledProvider_IsNotOffered(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedProvider()
	h.seedOIDCProvider(false)

	got := h.do(request{Method: http.MethodGet, Path: providersPath})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var page api.Page[api.PublicIdentityProvider]
	require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
	require.Len(t, page.Items, 1, "a disabled provider was offered to a stranger: %s", got.Body)
	require.Equal(t, localProviderKey, page.Items[0].Key)
}

// The secret is absent from the STRUCT rather than blanked by a renderer, which is what makes this
// assertable over the wire: there is no field to get wrong. The check is against the raw body and
// against the stored value, because a renderer that emitted `***` would pass a check for the
// field name alone.
func TestListIdentityProviders_TheClientSecret_NeverReachesTheWire(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const secret = "an-operators-discord-client-secret"
	h.seedProviderWithSecret(secret)

	got := h.do(request{Method: http.MethodGet, Path: providersPath})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	require.NotContains(t, got.Body, secret)
	require.NotContains(t, got.Body, "client_secret")
	// The client ID is public and must be present: it travels in every authorization URL a browser
	// follows, and a client that has to guess it cannot start a flow.
	require.Contains(t, got.Body, "client_id")
}

// `createAuthorizationURL` is the second oracle for invite-code validity, and it is held to
// `previewInvite`'s disclosure as a CEILING rather than reasoned about separately. An unissued
// code gets the same answer from both — status and code — so a guesser learns nothing from the
// newer route that the metered one did not already tell them.
//
// This is the name docs/design/04-identity-and-revocation.md §5,
// docs/design/02-api-design.md and docs/concepts/invariants.md all cite it by.
func TestCreateAuthorizationURL_RevealsNoMoreThanPreviewInvite(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedOIDCProvider(true)

	viaPreview := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/invites/preview",
		Body: `{"code":"TODI-4KQ7M-9XPB2"}`,
	})
	viaAuth := h.do(request{
		Method: http.MethodPost, Path: authURLPath,
		Body: `{"provider":"oidc-test","invite_code":"TODI-4KQ7M-9XPB2"}`,
	})

	require.Equal(t, viaPreview.Status, viaAuth.Status,
		"the two invite-code routes answer differently for an unissued code: preview said %s, "+
			"authorization-url said %s", viaPreview.Body, viaAuth.Body)
	require.Equal(t, viaPreview.Problem.Code, viaAuth.Problem.Code)
}

// The route takes NO circle id, and that absence is the design rather than an omission: a public
// route answering differently for a real circle than an unknown one is a circle-existence oracle,
// including through which OAuth scopes the returned URL asks for.
func TestCreateAuthorizationURL_TakesNoCircleID(t *testing.T) {
	t.Parallel()
	route, err := api.MustLookup(api.OpCreateAuthorizationURL)
	require.NoError(t, err)
	require.NotContains(t, route.Path, api.CirclePathParam)

	h := newHarness(t)
	h.seedOIDCProvider(true)
	// Not ignored — REFUSED. The body schema is closed, so a caller who sends a circle id is told
	// the field does not exist rather than being quietly served an answer that did not use it. A
	// silently-dropped parameter is how a client comes to believe a circle was involved.
	got := h.do(request{
		Method: http.MethodPost, Path: authURLPath,
		Body: `{"provider":"oidc-test","circle_id":"01K3TGT8N9M4X0Q7R2VB6C5D1E"}`,
	})
	require.Equal(t, http.StatusUnprocessableEntity, got.Status, got.Body)
	require.Equal(t, "body.circle_id", got.Problem.Errors[0].Location)
}

// A provider with no browser flow has no authorization URL to build, and saying so is better than
// building one that goes nowhere.
func TestCreateAuthorizationURL_AProviderWithNoBrowserFlow_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedProvider()

	got := h.do(request{
		Method: http.MethodPost, Path: authURLPath,
		Body: `{"provider":"` + localProviderKey + `"}`,
	})
	require.Equal(t, http.StatusUnprocessableEntity, got.Status, got.Body)
	require.Equal(t, apierr.CodeValidationFailed, got.Problem.Code)
}

// The authorization URL is what the browser is sent to, and the flow expires. `as_of` is on the
// response because every countdown in this API is a signed offset from one — a client that
// subtracted `expires_at` from its own clock would show the wrong number on a machine that is four
// minutes fast, and the right one in the database.
func TestCreateAuthorizationURL_AnEnabledOIDCProvider_ReturnsAURLAndAnAsOf(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedOIDCProvider(true)

	got := h.do(request{
		Method: http.MethodPost, Path: authURLPath, Body: `{"provider":"oidc-test"}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var start api.AuthorizationStart
	require.NoError(t, json.Unmarshal([]byte(got.Body), &start))
	parsed, err := url.Parse(start.AuthorizationURL)
	require.NoError(t, err)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "S256", parsed.Query().Get("code_challenge_method"),
		"the PKCE verifier stays server-side, so the challenge has to be on the URL")
	require.NotEmpty(t, parsed.Query().Get("state"))
	require.Equal(t, h.clock.Now(), start.AsOf)
	require.True(t, start.AsOf.Before(start.ExpiresAt))

	// The `state` is a CSRF nonce and not a credential, but it is unguessable and the client has no
	// use for it: the provider hands it back to the callback directly.
	require.NotContains(t, got.Body, `"state"`)
}

// The callback redirects on FAILURE as well as on success — one rule for the redirect rather than
// one per outcome — and the code rides in the FRAGMENT, which no browser transmits to any server.
//
// A problem body here would leave somebody on a blank callback page holding JSON, with the only
// thing they can act on ("try again") on a screen they are not looking at.
func TestCompleteAuthorization_AnExpiredFlow_RedirectsWithTheErrorInTheFragment(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedOIDCProvider(true)

	got := h.do(request{
		Method: http.MethodGet,
		Path:   api.BasePath + "/auth/callback/oidc-test?state=never-issued&code=whatever",
	})
	require.Equal(t, http.StatusFound, got.Status, got.Body)
	require.Empty(t, strings.TrimSpace(got.Body), "a redirect must not carry a body")
	require.Equal(t, "no-store", got.Header.Get("Cache-Control"),
		"a cached 302 is a replayable one")

	location := got.Header.Get("Location")
	require.NotEmpty(t, location)
	parsed, err := url.Parse(location)
	require.NoError(t, err)
	require.Equal(t, "/join", parsed.Path)
	require.Empty(t, parsed.RawQuery, "nothing about the outcome may travel in a query string")
	require.Equal(t, "error="+string(identity.CodeAuthFlowExpired), parsed.Fragment)
}

// A callback carrying the provider's own refusal — somebody clicked Cancel — is the same shape.
// It is a real outcome rather than an error in this instance, and the SPA renders it as one.
func TestCompleteAuthorization_AProviderRefusal_UsesTheSameFragmentRule(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedOIDCProvider(true)

	got := h.do(request{
		Method: http.MethodGet,
		Path:   api.BasePath + "/auth/callback/oidc-test?state=never-issued&error=access_denied",
	})
	require.Equal(t, http.StatusFound, got.Status)
	parsed, err := url.Parse(got.Header.Get("Location"))
	require.NoError(t, err)
	require.Empty(t, parsed.RawQuery)
	require.True(t, strings.HasPrefix(parsed.Fragment, "error="), parsed.Fragment)
}

// The callback is `Hidden: true` — it is a redirect target for a browser, not an operation any
// client calls — and it is one of exactly four operations permitted to be.
func TestCompleteAuthorization_IsHiddenFromTheDocument(t *testing.T) {
	t.Parallel()
	route, err := api.MustLookup(api.OpCompleteAuthorization)
	require.NoError(t, err)
	require.True(t, route.Hidden)
}

// seedOIDCProvider writes an OIDC provider through the real service, so the row is one the service
// could actually have written rather than one a test assembled past its validation.
func (h *harness) seedOIDCProvider(enabled bool) core.IdentityProviderID {
	h.t.Helper()
	created, err := h.identity.AddProvider(h.t.Context(), identity.AddProviderRequest{
		Key:                   "oidc-test",
		Kind:                  identity.KindOIDC,
		DisplayName:           "Test OIDC",
		Enabled:               enabled,
		Issuer:                "https://issuer.example.com",
		AuthorizationEndpoint: "https://issuer.example.com/authorize",
		JWKSURI:               "https://issuer.example.com/jwks",
		SubjectClaim:          "sub",
		ClientID:              "test-client-id",
		ClientSecret:          core.Secret("test-client-secret"),
		RedirectURI:           "https://tod.example.com/api/v1/auth/callback/oidc-test",
		TokenEndpoint:         "https://issuer.example.com/token",
	})
	require.NoError(h.t, err)
	id, err := core.ParseID[core.IdentityProvider](created.ID)
	require.NoError(h.t, err)
	return id
}

// seedProviderWithSecret writes an enabled OIDC provider holding a known secret, so a test can
// assert that exact string never reaches the wire.
func (h *harness) seedProviderWithSecret(secret string) {
	h.t.Helper()
	_, err := h.identity.AddProvider(h.t.Context(), identity.AddProviderRequest{
		Key:                   "oidc-secret",
		Kind:                  identity.KindOIDC,
		DisplayName:           "Test OIDC",
		Enabled:               true,
		Issuer:                "https://issuer.example.com",
		AuthorizationEndpoint: "https://issuer.example.com/authorize",
		JWKSURI:               "https://issuer.example.com/jwks",
		SubjectClaim:          "sub",
		ClientID:              "public-client-id",
		ClientSecret:          core.Secret(secret),
		RedirectURI:           "https://tod.example.com/api/v1/auth/callback/oidc-secret",
		TokenEndpoint:         "https://issuer.example.com/token",
	})
	require.NoError(h.t, err)
}

// The redirect URI an operator pastes into Discord's developer portal has to be the URL this
// binary actually serves the callback at, character for character. Deriving it from the route
// registry is what makes that true; this asserts the derivation rather than re-spelling its
// result, so moving the route moves the string an operator is told to register.
func TestCallbackBaseURL_IsDerivedFromTheRouteRegistry(t *testing.T) {
	t.Parallel()

	route, ok := api.Lookup(api.OpCompleteAuthorization)
	require.True(t, ok)
	require.True(t, strings.HasSuffix(route.FullPath(), api.CallbackPathParam),
		"CallbackBaseURL derives its path by removing %q; the route no longer ends in it",
		api.CallbackPathParam)

	got, err := api.CallbackBaseURL("https://tod.example.com")
	require.NoError(t, err)

	// The whole round trip: base + key must reproduce the path the router serves, with the
	// parameter filled in. Comparing against a literal here would be a second copy of the fact.
	require.Equal(t,
		"https://tod.example.com"+strings.Replace(route.FullPath(), api.CallbackPathParam, "/discord", 1),
		got+"/discord")
}

// Every spelling of a public URL an operator might put in `.env`, and the one answer each must
// produce. A trailing slash is the common one, and `…/callback//discord` is a different URI to
// every party that compares one.
func TestCallbackBaseURL_NormalisesThePublicURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		public string
		want   string
	}{
		{"the plain case", "https://tod.example.com", "https://tod.example.com/api/v1/auth/callback"},
		{"a trailing slash", "https://tod.example.com/", "https://tod.example.com/api/v1/auth/callback"},
		{"surrounding space, as a .env file produces", "  https://tod.example.com  ", "https://tod.example.com/api/v1/auth/callback"},
		{"a non-default port", "https://tod.example.com:8443", "https://tod.example.com:8443/api/v1/auth/callback"},
		{"a path prefix, behind a shared front", "https://example.com/tod", "https://example.com/tod/api/v1/auth/callback"},
		{"plaintext, which local development is", "http://localhost:8080", "http://localhost:8080/api/v1/auth/callback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := api.CallbackBaseURL(tt.public)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	for _, bad := range []string{"", "tod.example.com", "/api/v1", "https://"} {
		_, err := api.CallbackBaseURL(bad)
		require.Error(t, err, "a public URL of %q is not something to guess an origin from", bad)
	}
}

// The second half of the invite-oracle defence, at the end the documents actually claim.
//
// `TestInviteOracle_ARateLimitedCaller_ReachesNoHandler` proves the limiter runs before the
// handler, against a stub. That is the mechanism; this is the CONSEQUENCE, and the consequence is
// what the design writes down: "it writes an `auth_flow` row only for a request that passes the
// limit, so a rejected probe stores nothing."
//
// Asserting it against the real table rather than inferring it from the stub matters because the
// inference has a hidden premise — that nothing else on the request path writes one. That premise
// is true today and is exactly the kind of thing a future middleware breaks silently: an
// unauthenticated flood that grows a table is a denial of service with no principal to rate-limit
// afterwards.
func TestAuthFlow_RateLimitedCaller_CreatesNoRows(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedOIDCProvider(true)

	body := `{"provider":"oidc-test"}`
	accepted := 0
	for range api.DefaultInviteBurst {
		if h.do(request{Method: http.MethodPost, Path: authURLPath, Body: body}).Status == http.StatusOK {
			accepted++
		}
	}
	require.Positive(t, accepted, "the bucket refused everything, so nothing below is a limit test")

	// Well past the burst, so the bucket cannot refill into a pass on the injected clock.
	refused := 0
	for range api.DefaultInviteBurst {
		got := h.do(request{Method: http.MethodPost, Path: authURLPath, Body: body})
		require.Equal(t, http.StatusTooManyRequests, got.Status, got.Body)
		refused++
	}
	require.Positive(t, refused)

	require.Equal(t, accepted, h.sweepAuthFlows(),
		"one row per ACCEPTED request and none per refused one; %d requests were rate-limited and "+
			"an unauthenticated flood must not be able to grow the table", refused)
}
