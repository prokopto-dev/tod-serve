package identity_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
)

func requireCode(t *testing.T, err error, want identity.Code) {
	t.Helper()
	require.Error(t, err)
	got, ok := identity.CodeOf(err)
	require.True(t, ok, "error carries no wire code: %v", err)
	require.Equal(t, want, got)
}

// A ticket is redeemable ONCE, at either /join or /sessions. A second redemption would be a
// second PAT for one authorization, which is the whole thing the ticket exists to prevent.
func TestCredentialTicket_SecondRedemption_Refused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{GuildID: guildID})
	ticket := h.mintTicket(t)

	credential := identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: ticket}

	first, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider: discordProvider(), Credential: credential,
	})
	require.NoError(t, err)
	require.Equal(t, discordSubject, first.Subject)

	_, err = h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider: discordProvider(), Credential: credential,
	})
	requireCode(t, err, identity.CodeAuthTicketInvalid)
}

// The TTL is a CHECK on the row, so a longer-lived ticket cannot be written at all. This is the
// other half: the injected clock catching up with a ticket that was written correctly.
func TestCredentialTicket_After120s_Refused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{})
	ticket := h.mintTicket(t)

	// One second inside the window still works, so the test is about the boundary rather than
	// about the clock moving at all.
	h.clock.Advance(identity.TicketTTL - time.Second)
	_, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:   discordProvider(),
		Credential: identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: ticket},
	})
	require.NoError(t, err)

	h2 := newHarness(t)
	h2.withLiveInvite(identity.GuildGate{})
	ticket2 := h2.mintTicket(t)

	h2.clock.Advance(identity.TicketTTL)
	_, err = h2.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:   discordProvider(),
		Credential: identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: ticket2},
	})
	requireCode(t, err, identity.CodeAuthTicketExpired)
}

func TestRedeemProviderTicket_UnknownTicket_IsIndistinguishableFromAConsumedOne(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.service.RedeemProviderTicket(t.Context(), "a-ticket-nobody-issued")

	requireCode(t, err, identity.CodeAuthTicketInvalid)
}

// A ticket presented for the wrong provider is consumed and refused. Letting it be retried
// against the right one would make it multi-use in exactly the case somebody was probing.
func TestVerify_TicketForAnotherProvider_IsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{})
	ticket := h.mintTicket(t)

	other := oidcProvider()

	_, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:   other,
		Credential: identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: ticket},
	})

	requireCode(t, err, identity.CodeCredentialInvalid)
}

// The gate is evaluated against the facts the ticket carries, at BOTH /join and /sessions. This
// is the reusable half the circle worker calls from each; a gate on join alone would let
// /sessions mint a fresh PAT for somebody who has left the guild.
func TestGuildGate_EvaluatedAgainstTicketFacts_AtEitherEndpoint(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	gate := identity.GuildGate{GuildID: guildID, RequiredRoleIDs: []string{"raider"}}
	h.withLiveInvite(gate)
	ticket := h.mintTicket(t)

	verified, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:   discordProvider(),
		Credential: identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: ticket},
	})
	require.NoError(t, err)

	// /join and /sessions are two call sites of one function, and they are handed the same facts.
	require.NoError(t, identity.EvaluateGuildGate(gate, verified.GuildFacts))
	require.NoError(t, identity.EvaluateGuildGate(gate, verified.GuildFacts))

	// The same facts against a gate the subject does not satisfy.
	requireCode(t,
		identity.EvaluateGuildGate(identity.GuildGate{GuildID: guildID, RequiredRoleIDs: []string{"officer"}}, verified.GuildFacts),
		identity.CodeGuildRoleRequired)
}

// The circle adds a guild gate mid-flow: the authorization never requested guilds.members.read,
// so the ticket carries no member object — and an absent fact is a rejection, so the join fails
// closed rather than sliding past an ungated check.
func TestGuildGate_MissingRoleFacts_Refused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.withLiveInvite(identity.GuildGate{}) // ungated when the flow started
	ticket := h.mintTicket(t)

	verified, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:   discordProvider(),
		Credential: identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: ticket},
	})
	require.NoError(t, err)
	require.Empty(t, verified.GuildFacts)

	requireCode(t,
		identity.EvaluateGuildGate(identity.GuildGate{GuildID: guildID}, verified.GuildFacts),
		identity.CodeGuildRoleRequired)
}

func TestEvaluateGuildGate_NotInTheGuild_ReportsMembershipRatherThanRole(t *testing.T) {
	t.Parallel()

	facts := identity.GuildFacts{guildID: discord.GuildFact{Member: false, RoleIDs: []string{}}}

	requireCode(t,
		identity.EvaluateGuildGate(identity.GuildGate{GuildID: guildID}, facts),
		identity.CodeGuildMembershipRequired)
}

// The audience check is what closes cross-instance replay, and the bearer_token path is where it
// is load-bearing rather than redundant.
// The `bearer_token` half on its own. ADR-0011's named mechanism,
// TestDiscord_ForeignApplicationToken_Refused in flow_test.go, covers this path AND the callback
// in one test, because the rule the ADR states is that there is no carve-out between them. This
// stays as the narrower statement of the same fact.
func TestVerify_BearerTokenFromAnotherApplication_IsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.doer.answers["/oauth2/@me"] = jsonResponse(t, 200, map[string]any{
		"application": map[string]any{"id": "999999999999999999"},
		"scopes":      []string{discord.ScopeIdentify},
	})

	_, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:   discordProvider(),
		Credential: identity.Credential{Kind: identity.CredentialBearerToken, Token: core.Secret("stolen")},
	})

	requireCode(t, err, identity.CodeCredentialAudienceMismatch)
}

func TestVerify_BearerToken_ReadsGuildFactsForTheCallersCircle(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	verified, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:   discordProvider(),
		Credential: identity.Credential{Kind: identity.CredentialBearerToken, Token: core.Secret("a-token")},
		GuildIDs:   []string{guildID},
	})

	require.NoError(t, err)
	require.Equal(t, discord.GuildFact{Member: true, RoleIDs: []string{"raider"}}, verified.GuildFacts[guildID])
}

func TestVerify_DisabledProvider_IsRefusedBeforeAnythingElse(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	provider := discordProvider()
	provider.Enabled = false

	_, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:   provider,
		Credential: identity.Credential{Kind: identity.CredentialBearerToken, Token: core.Secret("a-token")},
	})

	requireCode(t, err, identity.CodeProviderDisabled)
	require.Empty(t, h.doer.seen, "a disabled provider is not contacted")
}

// `local` needs no network, no operator registration and no third party at all. That is the whole
// reason it exists, so it is asserted rather than assumed.
func TestVerify_Local_WorksWithNoProviderAndNoNetwork(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	localProvider := identity.Provider{
		ID: "01J0000000000000LOCALID", Key: "local", Kind: identity.KindLocal,
		DisplayName: "This server", Enabled: true, VerifiableSubject: false,
	}

	verified, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:    localProvider,
		Credential:  identity.Credential{Kind: identity.CredentialNone},
		DisplayName: "Tankguy",
	})

	require.NoError(t, err)
	require.Equal(t, "Tankguy", verified.DisplayName)
	require.Len(t, verified.Subject, core.ULIDLen)
	require.Empty(t, h.doer.seen, "local reaches no third party")
}

func TestVerify_LocalWithoutADisplayName_IsAValidationFailure(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	localProvider := identity.Provider{
		ID: "01J0000000000000LOCALID", Key: "local", Kind: identity.KindLocal,
		Enabled: true, VerifiableSubject: false,
	}

	_, err := h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider:   localProvider,
		Credential: identity.Credential{Kind: identity.CredentialNone},
	})

	requireCode(t, err, identity.CodeValidationFailed)
	var coded *identity.Error
	require.ErrorAs(t, err, &coded)
	require.Equal(t, identity.LocationDisplayName, coded.Location)
}

func TestNew_IncompleteConfiguration_IsRefused(t *testing.T) {
	t.Parallel()

	full := func() identity.Config {
		h := newHarness(t)
		return identity.Config{
			Store: h.store, Clients: &stubClients{}, Clock: h.clock,
			IDs: core.NewGenerator(&countingEntropy{}), Entropy: &countingEntropy{},
			SPAJoinURL: spaJoinURL, CallbackBaseURL: callbackBaseURL,
			Logger: slog.New(slog.DiscardHandler),
		}
	}

	for name, mutate := range map[string]func(identity.Config) identity.Config{
		"no store":     func(c identity.Config) identity.Config { c.Store = nil; return c },
		"no clients":   func(c identity.Config) identity.Config { c.Clients = nil; return c },
		"no clock":     func(c identity.Config) identity.Config { c.Clock = nil; return c },
		"no ids":       func(c identity.Config) identity.Config { c.IDs = nil; return c },
		"no entropy":   func(c identity.Config) identity.Config { c.Entropy = nil; return c },
		"no logger":    func(c identity.Config) identity.Config { c.Logger = nil; return c },
		"no spa url":   func(c identity.Config) identity.Config { c.SPAJoinURL = ""; return c },
		"relative spa": func(c identity.Config) identity.Config { c.SPAJoinURL = "/join"; return c },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := identity.New(mutate(full()))
			require.Error(t, err)
		})
	}
}
