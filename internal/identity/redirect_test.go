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

// The line CanonicalRedirectURI draws, from both sides. Getting it wrong in either direction has a
// cost, and they are different costs: fold too much and a configuration the provider rejects
// passes; fold too little and a correct one is reported broken, which is how a check gets
// switched off.
func TestCanonicalRedirectURI_FoldsWhatTheSpecificationFolds_AndNothingElse(t *testing.T) {
	t.Parallel()

	const canonical = "https://tod.example.com/api/v1/auth/callback/discord"

	same := []struct {
		name string
		raw  string
	}{
		{"an upper-case host, which DNS does not distinguish", "https://TOD.EXAMPLE.COM/api/v1/auth/callback/discord"},
		{"an upper-case scheme", "HTTPS://tod.example.com/api/v1/auth/callback/discord"},
		{"https's default port, written out", "https://tod.example.com:443/api/v1/auth/callback/discord"},
		{"surrounding space, as a copy-paste produces", "  https://tod.example.com/api/v1/auth/callback/discord  "},
	}
	for _, tt := range same {
		t.Run("same: "+tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, canonical, identity.CanonicalRedirectURI(tt.raw))
		})
	}

	different := []struct {
		name string
		raw  string
	}{
		{"an upper-case path, which Discord compares literally", "https://tod.example.com/API/v1/auth/callback/discord"},
		{"a trailing slash", "https://tod.example.com/api/v1/auth/callback/discord/"},
		{"a non-default port", "https://tod.example.com:8443/api/v1/auth/callback/discord"},
		{"plaintext", "http://tod.example.com/api/v1/auth/callback/discord"},
		{"a query string this callback does not carry", "https://tod.example.com/api/v1/auth/callback/discord?x=1"},
		{"another host entirely", "https://tod.example.com.evil.test/api/v1/auth/callback/discord"},
	}
	for _, tt := range different {
		t.Run("different: "+tt.name, func(t *testing.T) {
			t.Parallel()
			require.NotEqual(t, canonical, identity.CanonicalRedirectURI(tt.raw))
		})
	}

	// An unparseable value comes back as the operator typed it, so the error they read names the
	// string they actually configured rather than a repaired version of it.
	require.Equal(t, "://nonsense", identity.CanonicalRedirectURI("  ://nonsense  "))
}

// And the check itself accepts what the canonicaliser calls equal. Without this, the folding above
// could be correct and unused.
func TestCreateAuthorizationURL_ARedirectURIDifferingOnlyInCase_IsAccepted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	shouty := discordProvider()
	shouty.RedirectURI = "https://TOD.EXAMPLE.COM:443/api/v1/auth/callback/discord"
	h.store.addProvider(shouty)
	h.withLiveInvite(identity.GuildGate{})

	got, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})

	require.NoError(t, err, "scheme and host are case-insensitive and :443 is https's default")
	require.NotEmpty(t, got.URL)
}

// A `discord` row with no client secret is the other configuration that saves cleanly and cannot
// possibly work: the instance is a confidential OAuth client, so the token exchange is ours to
// perform, and `discord.New` refuses to build a client without one. Every sign-in would fail with
// a 500 at the moment somebody clicked the button rather than at the moment somebody configured
// it — and "I have some credentials" usually means a client id, because the secret is the one
// Discord shows once.
func TestAddProvider_DiscordWithNoClientSecret_IsRefusedAtConfigurationTime(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.store.providersByKey = map[string]identity.Provider{}
	h.store.providersByID = map[string]identity.Provider{}

	_, err := h.service.AddProvider(t.Context(), identity.AddProviderRequest{
		Key: "discord", Kind: identity.KindDiscord, DisplayName: "Discord",
		Enabled: true, ClientID: "111111111111111111",
		RedirectURI: callbackBaseURL + "/discord",
	})

	require.ErrorIs(t, err, identity.ErrProviderInconsistent)
	code, ok := identity.CodeOf(err)
	require.True(t, ok)
	require.Equal(t, identity.CodeValidationFailed, code)
	// The message says where to get one, because "needs a client secret" is true and useless to
	// somebody who does not know the portal shows it once.
	require.Contains(t, err.Error(), "shown once")
}

// And an `oidc` provider without one is still accepted, because that is a working configuration:
// the secret is used only on the browser path's token exchange, and a non-browser `id_token`
// client needs none. Refusing it would be a false positive on something that works, which is how
// a check gets switched off.
func TestAddProvider_OIDCWithNoClientSecret_IsStillAccepted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	created, err := h.service.AddProvider(t.Context(), identity.AddProviderRequest{
		Key: "authentik", Kind: identity.KindOIDC, DisplayName: "Corp SSO",
		Issuer: "https://sso.example.com", JWKSURI: "https://sso.example.com/jwks",
		ClientID: "tod-serve", RedirectURI: callbackBaseURL + "/authentik",
	})

	require.NoError(t, err)
	require.True(t, created.ClientSecret.IsZero())
}

// The same rule at the constructor, because this is where the corruption would surface.
//
// [Service.ExpectedRedirectURI] appends "/" + the provider key, so a callback base carrying
// anything after its path swallows the key: `…/callback?t=1` becomes `…/callback?t=1/discord`,
// which addresses the callback route with no provider key at all. The wiring builds this value
// with `api.CallbackBaseURL`, which refuses the same shapes — but this is a public constructor,
// and a check that lives only in the caller is a check the next caller does not get.
func TestNew_ACallbackBaseThatIsNotAnOrigin_RefusesToStart(t *testing.T) {
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

	notOrigins := map[string]string{
		"a query string": callbackBaseURL + "?tenant=one",
		"a fragment":     callbackBaseURL + "#frag",
		"userinfo":       "https://user:pass@tod.example.com/api/v1/auth/callback",
	}
	for name, raw := range notOrigins {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broken := base
			broken.CallbackBaseURL = raw
			_, err := identity.New(broken)
			require.Error(t, err)
			require.ErrorContains(t, err, "swallows the key")
		})
	}
}
