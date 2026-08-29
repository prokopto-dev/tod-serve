package identity_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
)

// The failure this whole file is about, stated as a scenario: an instance moves to a new domain,
// `$TOD_PUBLIC_URL` is updated, and the `identity_provider` row still carries the old callback.
// Discord is satisfied — the row agrees with what is registered there — the user signs in, and the
// browser is redirected to a host this instance is not at. No request reaches this server, so no
// log line on this server records it, and the operator's only symptom is "sign-in does nothing".
//
// The check therefore has to run BEFORE the browser leaves, which is what this asserts: no
// `auth_flow` row, no URL, and an error naming both strings.
func TestCreateAuthorizationURL_ARedirectURIForAnotherDeployment_IsRefusedBeforeARedirectExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		uri  string
	}{
		{"the old domain, after a move", "https://old.example.com/api/v1/auth/callback/discord"},
		{"another provider's key", callbackBaseURL + "/authentik"},
		{"the bare origin", "https://tod.example.com"},
		{"a trailing slash, which Discord compares literally", callbackBaseURL + "/discord/"},
		{"plaintext against an https instance", "http://tod.example.com/api/v1/auth/callback/discord"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			elsewhere := discordProvider()
			elsewhere.RedirectURI = tt.uri
			h.store.addProvider(elsewhere)
			h.withLiveInvite(identity.GuildGate{})

			got, err := h.service.CreateAuthorizationURL(t.Context(),
				identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})

			require.ErrorIs(t, err, identity.ErrRedirectURIMismatch)
			require.Empty(t, got.URL, "no redirect is built from a row that points at another deployment")
			require.Empty(t, h.store.flows, "and no auth_flow row is written for one")
			// Both strings, because an operator holding only one of them cannot tell which end is
			// wrong: the row, or what they registered with the provider.
			require.Contains(t, err.Error(), callbackBaseURL+"/discord")
		})
	}
}

// The positive control. Without it every case above would pass just as well if the flow could not
// build a Discord authorization URL at all.
func TestCreateAuthorizationURL_TheInstancesOwnCallback_IsAccepted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{})

	got, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})

	require.NoError(t, err)
	require.Contains(t, got.URL, "redirect_uri=")
	require.Len(t, h.store.flows, 1)
}

// Configuration time is where this SHOULD be caught: it is the only moment at which the person
// reading the error is the person who can fix it, and the error carries the exact string to paste
// into the provider's developer portal rather than a description of how to build one.
func TestAddProvider_ARedirectURIForAnotherDeployment_IsRefusedWithTheOneThatWorks(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.service.AddProvider(t.Context(), identity.AddProviderRequest{
		Key: "authentik", Kind: identity.KindOIDC, DisplayName: "Corp SSO",
		Issuer: "https://sso.example.com", JWKSURI: "https://sso.example.com/jwks",
		ClientID: "tod-serve", RedirectURI: "https://old.example.com/api/v1/auth/callback/authentik",
	})

	require.ErrorIs(t, err, identity.ErrRedirectURIMismatch)
	code, ok := identity.CodeOf(err)
	require.True(t, ok)
	require.Equal(t, identity.CodeValidationFailed, code)

	var coded *identity.Error
	require.ErrorAs(t, err, &coded)
	require.Equal(t, "body.redirect_uri", coded.Location,
		"the error points at the field, so a form can put it beside the input")
	require.Contains(t, coded.Message, callbackBaseURL+"/authentik")
}

// An operator who edits `$TOD_PUBLIC_URL` and then edits the provider to match must be able to.
// This is the repair path the operations guide sends them down, so it is pinned.
func TestChangeProvider_CorrectingTheRedirectURI_IsAccepted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	created, err := h.service.AddProvider(t.Context(), identity.AddProviderRequest{
		Key: "authentik", Kind: identity.KindOIDC, DisplayName: "Corp SSO",
		Issuer: "https://sso.example.com", JWKSURI: "https://sso.example.com/jwks",
		ClientID: "tod-serve", RedirectURI: callbackBaseURL + "/authentik",
	})
	require.NoError(t, err)

	stale := "https://old.example.com/api/v1/auth/callback/authentik"
	_, err = h.service.ChangeProvider(t.Context(), created.ID,
		identity.ChangeProviderRequest{RedirectURI: &stale})
	require.ErrorIs(t, err, identity.ErrRedirectURIMismatch, "an edit cannot introduce one either")

	fixed := callbackBaseURL + "/authentik"
	updated, err := h.service.ChangeProvider(t.Context(), created.ID,
		identity.ChangeProviderRequest{RedirectURI: &fixed})
	require.NoError(t, err)
	require.Equal(t, fixed, updated.RedirectURI)
}

// `local` redirects nowhere because it goes nowhere. A check that demanded a callback URL of it
// would make the one provider that needs no OAuth application unconfigurable.
func TestAddProvider_Local_NeedsNoRedirectURI(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	created, err := h.service.AddProvider(t.Context(), identity.AddProviderRequest{
		Key: "lan", Kind: identity.KindLocal, DisplayName: "On this LAN",
		Enabled: true, AcknowledgeWeakRevocation: true,
	})

	require.NoError(t, err)
	require.Empty(t, created.RedirectURI)
}

// The service refuses to exist without a callback base, rather than falling back to the join URL's
// origin. `$TOD_SPA_JOIN_URL` legitimately moves the console to another origin; the redirect URI
// belongs to the API's origin, always, and a service that guessed one would produce exactly the
// silent failure this check exists to catch.
func TestNew_WithNoCallbackBase_RefusesToStart(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	base := identity.Config{
		Store: h.store, Clients: h.clients, Clock: h.clock,
		IDs: core.NewGenerator(&countingEntropy{}), Entropy: &countingEntropy{},
		SPAJoinURL: spaJoinURL, CallbackBaseURL: callbackBaseURL,
		Logger: slog.New(slog.DiscardHandler),
	}
	_, err := identity.New(base)
	require.NoError(t, err, "the positive control: everything but the field under test is sound")

	for name, mutate := range map[string]func(identity.Config) identity.Config{
		"absent":   func(c identity.Config) identity.Config { c.CallbackBaseURL = ""; return c },
		"relative": func(c identity.Config) identity.Config { c.CallbackBaseURL = "/api/v1/auth/callback"; return c },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := identity.New(mutate(base))
			require.Error(t, err)
		})
	}
}

// A trailing slash on the configured base must not become a double slash in the value an operator
// is told to paste: `…/callback//discord` is a different URI to every party that compares one.
func TestExpectedRedirectURI_ATrailingSlashOnTheBase_IsNotADoubleSlash(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withSlash, err := identity.New(identity.Config{
		Store: h.store, Clients: h.clients, Clock: h.clock,
		IDs: core.NewGenerator(&countingEntropy{}), Entropy: &countingEntropy{},
		SPAJoinURL: spaJoinURL, CallbackBaseURL: callbackBaseURL + "/",
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	require.Equal(t, callbackBaseURL+"/discord", withSlash.ExpectedRedirectURI("discord"))
	require.Equal(t, callbackBaseURL+"/discord", h.service.ExpectedRedirectURI("discord"))
}
