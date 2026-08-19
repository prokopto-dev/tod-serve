package identity_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity/oidc"
	"github.com/prokopto-dev/tod-serve/internal/identity/outbound"
)

const (
	discordProviderID = "01J000000000000000PROVID"
	discordAppID      = "111111111111111111"
	discordSubject    = "333333333333333333"
	guildID           = "222222222222222222"
	circleID          = "01J00000000000000CIRCLE"
	identityID        = "01J000000000000000IDENT"
	inviteCode        = "TODI-4KQ7M-9XPB2"
	spaJoinURL        = "https://tod.example.com/join"

	// theAccessToken is the string the whole no-persistence invariant is about. It is a constant
	// so one assertion can search every recorded store call for it.
	theAccessToken = "discord-access-token-that-must-never-be-written-down"
)

var at = core.MicrosFromTime(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))

// discordDoer answers Discord's API from a table keyed on a URL suffix, and records what it saw.
type discordDoer struct {
	answers map[string]*outbound.Response
	seen    []string
}

func (d *discordDoer) Do(_ context.Context, _, rawURL string, _ http.Header, _ []byte) (*outbound.Response, error) {
	d.seen = append(d.seen, rawURL)
	for suffix, resp := range d.answers {
		if strings.HasSuffix(rawURL, suffix) {
			return resp, nil
		}
	}
	return &outbound.Response{Status: http.StatusNotFound, Header: http.Header{}, Body: []byte("{}")}, nil
}

func jsonResponse(t *testing.T, status int, v map[string]any) *outbound.Response {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return &outbound.Response{Status: status, Header: http.Header{}, Body: b}
}

// stubClients hands out a real discord.Client wired to a stub transport, so everything under test
// is the real dispatch, the real audience check and the real fact handling.
//
// It counts the calls, because "the flow never got as far as building a redirect" is the
// assertion a validation test actually wants: an error returned after the URL was constructed
// would pass a check on the error alone.
type stubClients struct {
	discord    *discord.Client
	oidc       *oidc.Verifier
	oidcCalls  int
	discordHit int
}

func (s *stubClients) Discord(identity.Provider) (*discord.Client, error) {
	s.discordHit++
	return s.discord, nil
}

func (s *stubClients) OIDC(identity.Provider) (*oidc.Verifier, error) {
	s.oidcCalls++
	return s.oidc, nil
}

type harness struct {
	service *identity.Service
	store   *fakeStore
	doer    *discordDoer
	clients *stubClients
	clock   *clock.Test
}

func discordProvider() identity.Provider {
	return identity.Provider{
		ID:                discordProviderID,
		Key:               "discord",
		Kind:              identity.KindDiscord,
		DisplayName:       "Sign in with Discord",
		Enabled:           true,
		VerifiableSubject: true,
		ClientID:          discordAppID,
		ClientSecret:      core.Secret("operator-client-secret"),
		RedirectURI:       "https://tod.example.com/api/v1/auth/callback/discord",
	}
}

// newHarness wires the real service to a fake store and a stub Discord transport whose happy path
// answers every call the flow makes.
func newHarness(t *testing.T) *harness {
	t.Helper()

	doer := &discordDoer{answers: map[string]*outbound.Response{
		"/oauth2/token": jsonResponse(t, http.StatusOK, map[string]any{"access_token": theAccessToken}),
		"/oauth2/@me": jsonResponse(t, http.StatusOK, map[string]any{
			"application": map[string]any{"id": discordAppID},
			"scopes":      []string{discord.ScopeIdentify, discord.ScopeGuildsMembersRead},
		}),
		"/users/@me": jsonResponse(t, http.StatusOK, map[string]any{"id": discordSubject, "username": "tankguy"}),
		"/member":    jsonResponse(t, http.StatusOK, map[string]any{"roles": []string{"raider"}}),
	}}
	client, err := discord.New(doer, discord.Config{
		ClientID:     discordAppID,
		ClientSecret: core.Secret("operator-client-secret"),
		RedirectURI:  "https://tod.example.com/api/v1/auth/callback/discord",
	})
	require.NoError(t, err)

	store := newFakeStore()
	store.addProvider(discordProvider())

	clients := &stubClients{discord: client}
	testClock := clock.NewTest(at)
	service, err := identity.New(identity.Config{
		Store:      store,
		Clients:    clients,
		Clock:      testClock,
		IDs:        core.NewGenerator(&countingEntropy{}),
		Entropy:    &countingEntropy{},
		SPAJoinURL: spaJoinURL,
		Logger:     slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	return &harness{service: service, store: store, doer: doer, clients: clients, clock: testClock}
}

// oidcProvider is a well-formed `oidc` row. Tests that need a broken one start here and break one
// field, so the difference between the passing and failing case is visible in the test.
func oidcProvider() identity.Provider {
	return identity.Provider{
		ID:                    "01J000000000000000OIDCP",
		Key:                   "authentik",
		Kind:                  identity.KindOIDC,
		DisplayName:           "Sign in with Authentik",
		Enabled:               true,
		VerifiableSubject:     true,
		Issuer:                "https://idp.example.com",
		JWKSURI:               "https://idp.example.com/jwks",
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		TokenEndpoint:         "https://idp.example.com/token",
		ClientID:              "tod-serve-instance",
		ClientSecret:          core.Secret("oidc-client-secret"),
		RedirectURI:           "https://tod.example.com/api/v1/auth/callback/authentik",
	}
}

// withOIDC adds an OIDC provider and a verifier for it, and returns the provider so a test can
// mutate the row before the call under test reads it.
func (h *harness) withOIDC(t *testing.T, p identity.Provider) {
	t.Helper()
	h.store.addProvider(p)

	// A row too broken to build a verifier from still gets stored: the tests that use one are
	// asserting the flow refuses it BEFORE reaching the client factory, so refusing to set the
	// fixture up would hide the thing under test.
	verifier, err := oidc.NewVerifier(h.doer, h.clock, oidc.Config{
		Issuer: p.Issuer, ClientID: p.ClientID, ClientSecret: p.ClientSecret,
		JWKSURI: p.JWKSURI, AuthorizationEndpoint: p.AuthorizationEndpoint,
		TokenEndpoint: p.TokenEndpoint, RedirectURI: p.RedirectURI,
	})
	if err == nil {
		h.clients.oidc = verifier
	}
}

// withLiveInvite adds an invite whose circle gates on a guild, and returns its code.
func (h *harness) withLiveInvite(gate identity.GuildGate) string {
	h.store.invites[inviteCode] = identity.Invite{
		ID: "01J0000000000000INVITE", CircleID: circleID, CodeHash: []byte("invite-hash"), Live: true,
	}
	h.store.gates[circleID+"/"+discordProviderID] = gate
	return inviteCode
}

// authorizeThenCallback runs the whole browser flow and returns the callback's answer.
func (h *harness) authorizeThenCallback(t *testing.T, req identity.AuthorizationRequest) (identity.Callback, error) {
	t.Helper()
	authorization, err := h.service.CreateAuthorizationURL(t.Context(), req)
	require.NoError(t, err)

	return h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
		ProviderKey: req.ProviderKey,
		State:       authorization.State,
		Code:        "authorization-code",
	})
}

// The invariant, at the level it can actually be asserted. The token exists inside the callback;
// nothing the callback writes may contain it.
func TestDiscord_AccessToken_NeverPersisted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{GuildID: guildID, RequiredRoleIDs: []string{"raider"}})

	callback, err := h.authorizeThenCallback(t, identity.AuthorizationRequest{
		ProviderKey: "discord", InviteCode: inviteCode,
	})
	require.NoError(t, err)
	require.Contains(t, callback.Location, "#ticket=")

	calls := h.store.recorded()
	require.NotEmpty(t, calls, "a flow that stored nothing would pass this test vacuously")
	for _, call := range calls {
		require.NotContains(t, call, theAccessToken,
			"the Discord access token reached a store call: %s", call)
	}

	// And it is not in what the browser is sent to either, which is the other place a token that
	// was meant to be discarded turns up.
	require.NotContains(t, callback.Location, theAccessToken)
}

// The ticket is a bearer credential that mints a PAT, so it obeys the same rule as an invite code:
// a query string lands in access logs, Referer headers and proxy logs.
func TestNoTokenInURL_CallbackRedirectUsesFragment(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{})

	callback, err := h.authorizeThenCallback(t, identity.AuthorizationRequest{
		ProviderKey: "discord", InviteCode: inviteCode,
	})
	require.NoError(t, err)

	parsed, err := url.Parse(callback.Location)
	require.NoError(t, err)
	require.Empty(t, parsed.RawQuery, "the ticket must not reach a query string")
	require.True(t, strings.HasPrefix(parsed.Fragment, "ticket="))
	require.NotEmpty(t, strings.TrimPrefix(parsed.Fragment, "ticket="))
}

// One rule for the redirect rather than one per outcome: the failure the fragment rule is
// forgotten for is the failure that matters.
func TestCompleteAuthorization_Failure_AlsoUsesTheFragment(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	callback, err := h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
		ProviderKey: "discord", State: "a-state-nobody-issued", Code: "code",
	})

	require.Error(t, err)
	require.Equal(t, identity.CodeAuthFlowExpired, callback.Code)

	parsed, parseErr := url.Parse(callback.Location)
	require.NoError(t, parseErr)
	require.Empty(t, parsed.RawQuery)
	require.Equal(t, "error=auth_flow_expired", parsed.Fragment)
}

func TestCompleteAuthorization_DeclinedAtTheConsentScreen_ReportsScopeDeclined(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{})
	authorization, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
	require.NoError(t, err)

	callback, err := h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
		ProviderKey: "discord", State: authorization.State, ProviderError: "access_denied",
	})

	require.Error(t, err)
	require.Equal(t, identity.CodeProviderScopeDeclined, callback.Code)
	require.Equal(t, "error=provider_scope_declined", mustFragment(t, callback.Location))
}

// A state is single-use. A replayed callback must not get a second exchange, because a second
// exchange is a second ticket and therefore a second PAT for one authorization.
func TestCompleteAuthorization_ReplayedState_IsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{})
	authorization, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
	require.NoError(t, err)

	req := identity.CallbackRequest{ProviderKey: "discord", State: authorization.State, Code: "code-1"}
	first, err := h.service.CompleteAuthorization(t.Context(), req)
	require.NoError(t, err)
	require.Contains(t, first.Location, "#ticket=")

	second, err := h.service.CompleteAuthorization(t.Context(), req)
	require.Error(t, err)
	require.Equal(t, identity.CodeAuthFlowExpired, second.Code)
}

func TestCreateAuthorizationURL_ExpiredFlow_IsRefusedAtTheCallback(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{})
	authorization, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
	require.NoError(t, err)

	h.clock.Advance(identity.AuthFlowTTL + time.Second)

	callback, err := h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
		ProviderKey: "discord", State: authorization.State, Code: "code-1",
	})

	require.Error(t, err)
	require.Equal(t, identity.CodeAuthFlowExpired, callback.Code)
}

// The authorization request asks for every scope the callback then uses, and no more.
func TestAuthorizationURL_GuildGatedCircle_RequestsGuildsMembersRead(t *testing.T) {
	t.Parallel()

	t.Run("gated circle", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.withLiveInvite(identity.GuildGate{GuildID: guildID})

		got, err := h.service.CreateAuthorizationURL(t.Context(),
			identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})

		require.NoError(t, err)
		require.Contains(t, got.URL, "guilds.members.read")
	})

	t.Run("ungated circle", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.withLiveInvite(identity.GuildGate{})

		got, err := h.service.CreateAuthorizationURL(t.Context(),
			identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})

		require.NoError(t, err)
		require.NotContains(t, got.URL, "guilds.members.read")
		require.Contains(t, got.URL, "scope=identify")
	})

	// With no invite there is no circle to resolve — resolving one from a caller-supplied id is
	// the existence oracle canonical §7 closes — so the scope decision falls back to an
	// instance-level fact that names no circle.
	t.Run("no invite, some circle on the instance gates", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.store.anyGuildGate = true

		got, err := h.service.CreateAuthorizationURL(t.Context(),
			identity.AuthorizationRequest{ProviderKey: "discord"})

		require.NoError(t, err)
		require.Contains(t, got.URL, "guilds.members.read")
		require.Contains(t, h.store.recorded(), "AnyCircleGatesOnAGuild[]")
	})
}

// The PKCE verifier is held server-side. That is what makes an intercepted `code` useless to
// whoever intercepted it, and it only holds if the verifier is not in the URL.
func TestCreateAuthorizationURL_PKCEVerifier_NeverLeavesTheServer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{})

	got, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
	require.NoError(t, err)

	var flow identity.AuthFlow
	for _, f := range h.store.flows {
		flow = f
	}
	require.NotEmpty(t, flow.PKCEVerifier)
	require.NotContains(t, got.URL, flow.PKCEVerifier)
	require.Contains(t, got.URL, "code_challenge_method=S256")
	require.Contains(t, got.URL, "code_challenge="+url.QueryEscape(discord.PKCEChallenge(flow.PKCEVerifier)))
}

// The circle on an auth_flow is ADVISORY: it is recorded so the scope set and the guild to check
// can be decided before the browser leaves. Redemption re-derives the circle from the invite and
// is the authority — a 120-second-old snapshot must never outrank the live row.
func TestAuthFlow_CircleIsAdvisory_RedemptionReDerives(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{})

	authorization, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
	require.NoError(t, err)

	var flow identity.AuthFlow
	for _, f := range h.store.flows {
		flow = f
	}
	require.Equal(t, circleID, flow.CircleID, "the circle is recorded, because the scopes depend on it")

	_, err = h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
		ProviderKey: "discord", State: authorization.State, Code: "code-1",
	})
	require.NoError(t, err)

	// The ticket carries the verified subject and the guild facts, and no circle at all: which
	// circle it lands in is settled at redemption, from the invite.
	for _, ticket := range h.store.tickets {
		rendered, marshalErr := json.Marshal(ticket)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(rendered), circleID)
	}
}

// A user can sit on a consent screen for minutes. If the invite died in between, the callback
// mints NOTHING — the early-out that stops a credential being issued for a dead invite.
func TestCompleteAuthorization_InviteDiedMidFlow_MintsNoTicket(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dead identity.Code
	}{
		{"revoked", identity.CodeInviteRevoked},
		{"expired", identity.CodeInviteExpired},
		{"exhausted", identity.CodeInviteExhausted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.withLiveInvite(identity.GuildGate{})
			authorization, err := h.service.CreateAuthorizationURL(t.Context(),
				identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
			require.NoError(t, err)

			invite := h.store.invites[inviteCode]
			invite.Live, invite.DeadCode = false, tt.dead
			h.store.invites[inviteCode] = invite

			callback, err := h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
				ProviderKey: "discord", State: authorization.State, Code: "code-1",
			})

			require.Error(t, err)
			require.Equal(t, tt.dead, callback.Code)
			require.Empty(t, h.store.tickets, "no ticket is minted for an invite that is already dead")
		})
	}
}

// The instance block is the operator's tool, and it is checked at the callback as well as at
// redemption, so a blocked identity does not even receive a ticket.
func TestJoin_BlockedIdentity_Refused(t *testing.T) {
	t.Parallel()

	t.Run("at the callback", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.withLiveInvite(identity.GuildGate{})
		h.store.identities[discordProviderID+"/"+discordSubject] = identity.StoredIdentity{
			ID: identityID, ProviderID: discordProviderID, Subject: discordSubject, Blocked: true,
		}
		authorization, err := h.service.CreateAuthorizationURL(t.Context(),
			identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
		require.NoError(t, err)

		callback, err := h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
			ProviderKey: "discord", State: authorization.State, Code: "code-1",
		})

		require.Error(t, err)
		require.Equal(t, identity.CodeIdentityBlocked, callback.Code)
		require.Empty(t, h.store.tickets)
	})

	// And again at redemption, so a ticket minted before the block was applied is not a second
	// door into a circle whose officers have never heard of them.
	t.Run("at ticket redemption", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.withLiveInvite(identity.GuildGate{})
		ticket := h.mintTicket(t)

		h.store.identities[discordProviderID+"/"+discordSubject] = identity.StoredIdentity{
			ID: identityID, ProviderID: discordProviderID, Subject: discordSubject, Blocked: true,
		}

		_, err := h.service.Verify(t.Context(), identity.VerifyRequest{
			Provider:   discordProvider(),
			Credential: identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: ticket},
		})

		code, ok := identity.CodeOf(err)
		require.True(t, ok)
		require.Equal(t, identity.CodeIdentityBlocked, code)
	})
}

// mintTicket runs the browser flow and returns the ticket out of the redirect fragment, which is
// exactly what the SPA does.
func (h *harness) mintTicket(t *testing.T) string {
	t.Helper()
	callback, err := h.authorizeThenCallback(t, identity.AuthorizationRequest{
		ProviderKey: "discord", InviteCode: inviteCode,
	})
	require.NoError(t, err)
	return strings.TrimPrefix(mustFragment(t, callback.Location), "ticket=")
}

func mustFragment(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	return parsed.Fragment
}

// The browser flow validates the provider row before it uses it, and this is the case that makes
// it load-bearing rather than tidy.
//
// `authorization_endpoint` is the ONE operator-supplied OIDC URL that never goes through
// internal/identity/outbound: the browser goes there, this instance does not. So the guarded
// client's https check, its allowlist and its deny list never see it, and Provider.Validate is the
// only thing standing between a mistyped `http://` issuer and a redirect carrying the OAuth
// `state` over plaintext. Every other URL on the row fails closed without this — which is exactly
// why the gap was survivable everywhere except the one place it was not.
func TestCreateAuthorizationURL_InvalidProviderRow_IsRefusedBeforeARedirectExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		broken func(identity.Provider) identity.Provider
	}{
		{
			// The one the guarded client cannot catch.
			name: "a plaintext authorization endpoint, which nothing else would refuse",
			broken: func(p identity.Provider) identity.Provider {
				p.AuthorizationEndpoint = "http://idp.example.com/authorize"
				return p
			},
		},
		{
			name: "a plaintext issuer",
			broken: func(p identity.Provider) identity.Provider {
				p.Issuer = "http://idp.example.com"
				return p
			},
		},
		{
			name: "a plaintext jwks uri",
			broken: func(p identity.Provider) identity.Provider {
				p.JWKSURI = "http://idp.example.com/jwks"
				return p
			},
		},
		{
			// verifiable_subject is a CHECK against kind in the schema. A row that says otherwise
			// reached Go without going through the database, and it would report a weak circle as
			// durable.
			name: "an oidc row claiming an unverifiable subject",
			broken: func(p identity.Provider) identity.Provider {
				p.VerifiableSubject = false
				return p
			},
		},
		{
			name: "an oidc row carrying no jwks uri at all",
			broken: func(p identity.Provider) identity.Provider {
				p.JWKSURI = ""
				return p
			},
		},
		{
			// With no client id there is no audience to compare `aud` against, so an ID token
			// minted for a different relying party at the same issuer would verify.
			name: "an oidc row carrying no client id, so it has no audience to check",
			broken: func(p identity.Provider) identity.Provider {
				p.ClientID = ""
				return p
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.withOIDC(t, tt.broken(oidcProvider()))

			got, err := h.service.CreateAuthorizationURL(t.Context(),
				identity.AuthorizationRequest{ProviderKey: "authentik"})

			require.ErrorIs(t, err, identity.ErrProviderInconsistent)
			require.Empty(t, got.URL, "no redirect is built from a row that does not validate")
			require.Empty(t, h.store.flows, "and no auth_flow row is written for one")
			require.Zero(t, h.clients.oidcCalls,
				"the row is checked before it reaches anything that would construct a URL from it")
		})
	}
}

// The positive control. Without it the test above would pass just as well if the harness could
// not build an OIDC authorization URL at all, which would make it assert nothing.
func TestCreateAuthorizationURL_WellFormedOIDCRow_BuildsAnHTTPSRedirect(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withOIDC(t, oidcProvider())

	got, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "authentik"})

	require.NoError(t, err)
	require.True(t, strings.HasPrefix(got.URL, "https://idp.example.com/authorize?"))
	require.Contains(t, got.URL, "code_challenge_method=S256")
	require.Len(t, h.store.flows, 1)
}

// The same check on the way back. The callback only FETCHES this row's endpoints, so the guarded
// client would refuse a non-https one anyway — but as a request that failed rather than as a row
// somebody can see is wrong.
func TestCompleteAuthorization_InvalidProviderRow_IsRefusedBeforeAnyExchange(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withOIDC(t, oidcProvider())
	authorization, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "authentik"})
	require.NoError(t, err)

	// The operator edits the row while the browser is on the consent screen.
	broken := oidcProvider()
	broken.TokenEndpoint = "http://idp.example.com/token"
	h.store.addProvider(broken)

	callback, err := h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
		ProviderKey: "authentik", State: authorization.State, Code: "code-1",
	})

	require.ErrorIs(t, err, identity.ErrProviderInconsistent)
	require.Empty(t, h.doer.seen, "no exchange is attempted against a row that does not validate")
	require.Empty(t, h.store.tickets)
	require.Equal(t, "error=credential_invalid", mustFragment(t, callback.Location),
		"an uncoded failure still redirects, so the browser is not left on a blank callback page")
}

// The callback's own resistance to a guessed `state`.
//
// The shared invite-code rate limit sits in front of `createAuthorizationURL`, not in front of the
// callback — reaching the callback needs a state that route already minted. So nothing upstream
// meters a caller who simply guesses states, and the whole defence is that a state is not
// guessable and not reusable. That makes these three properties load-bearing rather than tidy.
func TestCompleteAuthorization_State_IsUnguessableSingleUseAndOpaqueInFailure(t *testing.T) {
	t.Parallel()

	t.Run("256 bits, from the injected entropy source", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.withLiveInvite(identity.GuildGate{})

		first, err := h.service.CreateAuthorizationURL(t.Context(),
			identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
		require.NoError(t, err)
		second, err := h.service.CreateAuthorizationURL(t.Context(),
			identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
		require.NoError(t, err)

		raw, err := base64.RawURLEncoding.DecodeString(first.State)
		require.NoError(t, err)
		require.Len(t, raw, 32, "a state is 256 bits; anything shorter is a brute-force surface")
		require.NotEqual(t, first.State, second.State, "two flows never share a state")

		// The service refuses to exist without an entropy source rather than defaulting to one.
		// A generator that quietly falls back to a weak source is one nobody notices is weak; the
		// binary passes crypto/rand.
		_, err = identity.New(identity.Config{
			Store: h.store, Clients: h.clients, Clock: h.clock,
			IDs: core.NewGenerator(&countingEntropy{}), Entropy: nil,
			SPAJoinURL: spaJoinURL, Logger: slog.New(slog.DiscardHandler),
		})
		require.Error(t, err)
	})

	// Unknown, already-consumed and expired must be ONE answer. Telling them apart would confirm
	// that a guessed state was real, which is the only thing a guesser is trying to learn.
	t.Run("unknown, consumed and expired are indistinguishable", func(t *testing.T) {
		t.Parallel()

		consumed := newHarness(t)
		consumed.withLiveInvite(identity.GuildGate{})
		used, err := consumed.service.CreateAuthorizationURL(t.Context(),
			identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
		require.NoError(t, err)
		_, err = consumed.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
			ProviderKey: "discord", State: used.State, Code: "code-1",
		})
		require.NoError(t, err)

		expired := newHarness(t)
		expired.withLiveInvite(identity.GuildGate{})
		stale, err := expired.service.CreateAuthorizationURL(t.Context(),
			identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
		require.NoError(t, err)
		expired.clock.Advance(identity.AuthFlowTTL + time.Second)

		unknown := newHarness(t)

		answers := []identity.Callback{}
		for _, tc := range []struct {
			h     *harness
			state string
		}{
			{unknown, "a-state-nobody-ever-issued"},
			{consumed, used.State},
			{expired, stale.State},
		} {
			got, err := tc.h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
				ProviderKey: "discord", State: tc.state, Code: "code-1",
			})
			require.Error(t, err)
			answers = append(answers, got)
		}

		for _, got := range answers {
			require.Equal(t, identity.CodeAuthFlowExpired, got.Code)
			require.Equal(t, answers[0].Code, got.Code)
			require.Equal(t, answers[0].Location, got.Location,
				"the browser must not be able to tell a real state from an invented one")
		}
	})

	// The row is keyed on the state, so a second exchange finds nothing — and the database refuses
	// it a second time even if the query forgot the `WHERE consumed_at IS NULL`.
	t.Run("a consumed state mints no second ticket", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.withLiveInvite(identity.GuildGate{})
		authorization, err := h.service.CreateAuthorizationURL(t.Context(),
			identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: inviteCode})
		require.NoError(t, err)

		req := identity.CallbackRequest{ProviderKey: "discord", State: authorization.State, Code: "c"}
		_, err = h.service.CompleteAuthorization(t.Context(), req)
		require.NoError(t, err)
		require.Len(t, h.store.tickets, 1)

		_, err = h.service.CompleteAuthorization(t.Context(), req)
		require.Error(t, err)
		require.Len(t, h.store.tickets, 1, "one authorization, one ticket, one PAT")
	})
}
