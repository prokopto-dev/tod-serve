package identity_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
)

// The union is validated in the service rather than purely in the schema — ADR-0007 names that as
// a cost — so the errors it produces have to stay SPECIFIC, pointing into the body rather than
// saying "body invalid".
func TestCredentialValidate_EveryShape(t *testing.T) {
	t.Parallel()

	discordP := identity.Provider{Key: "discord", Kind: identity.KindDiscord, ClientID: "app", VerifiableSubject: true}
	oidcP := identity.Provider{Key: "authentik", Kind: identity.KindOIDC, VerifiableSubject: true}
	localP := identity.Provider{Key: "local", Kind: identity.KindLocal}

	tests := []struct {
		name     string
		provider identity.Provider
		cred     identity.Credential
		location string // empty means the credential is valid
	}{
		{
			"a ticket for discord", discordP,
			identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: "t"},
			"",
		},
		{
			"a ticket for oidc — both browser providers land on one ticket", oidcP,
			identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: "t"},
			"",
		},
		{
			"a ticket for local, which has no browser flow", localP,
			identity.Credential{Kind: identity.CredentialProviderTicket, Ticket: "t"},
			identity.LocationCredentialKind,
		},
		{
			"a ticket with no ticket", discordP,
			identity.Credential{Kind: identity.CredentialProviderTicket},
			identity.LocationCredentialTicket,
		},

		{
			"a bearer token for discord", discordP,
			identity.Credential{Kind: identity.CredentialBearerToken, Token: core.Secret("t")},
			"",
		},
		{
			"a bearer token for oidc", oidcP,
			identity.Credential{Kind: identity.CredentialBearerToken, Token: core.Secret("t")},
			identity.LocationCredentialKind,
		},
		{
			"a bearer token with no token", discordP,
			identity.Credential{Kind: identity.CredentialBearerToken},
			identity.LocationCredentialToken,
		},

		{
			"an id token for oidc", oidcP,
			identity.Credential{Kind: identity.CredentialIDToken, IDToken: "t", Nonce: "n"},
			"",
		},
		{
			"an id token for discord", discordP,
			identity.Credential{Kind: identity.CredentialIDToken, IDToken: "t", Nonce: "n"},
			identity.LocationCredentialKind,
		},
		{
			"an id token with no token", oidcP,
			identity.Credential{Kind: identity.CredentialIDToken, Nonce: "n"},
			identity.LocationCredentialIDToken,
		},
		{
			"an id token with no nonce", oidcP,
			identity.Credential{Kind: identity.CredentialIDToken, IDToken: "t"},
			identity.LocationCredentialNonce,
		},

		{"none, for local", localP, identity.Credential{Kind: identity.CredentialNone}, ""},
		{
			"none, for discord", discordP,
			identity.Credential{Kind: identity.CredentialNone},
			identity.LocationCredentialKind,
		},

		{
			"a kind nobody defined", discordP,
			identity.Credential{Kind: "magic_word"},
			identity.LocationCredentialKind,
		},
		{"no kind at all", discordP, identity.Credential{}, identity.LocationCredentialKind},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cred.Validate(tt.provider)
			if tt.location == "" {
				require.NoError(t, err)
				return
			}
			var coded *identity.Error
			require.ErrorAs(t, err, &coded)
			require.Equal(t, identity.CodeValidationFailed, coded.Code)
			require.Equal(t, tt.location, coded.Location,
				"a validation error has to point at the field, not at the body")
		})
	}
}

// A credential logged whole is a credential in a log file. Only the kind is loggable.
func TestCredential_LogValue_CarriesOnlyTheKind(t *testing.T) {
	t.Parallel()

	cred := identity.Credential{
		Kind: identity.CredentialBearerToken, Token: core.Secret("super-secret"),
		Ticket: "ticket-secret", IDToken: "id-token-secret", Nonce: "nonce",
	}

	rendered := cred.LogValue().String()

	require.Contains(t, rendered, "bearer_token")
	for _, secret := range []string{"super-secret", "ticket-secret", "id-token-secret", "nonce"} {
		require.NotContains(t, rendered, secret)
	}
}

func TestProvider_LogValue_CarriesNoSecret(t *testing.T) {
	t.Parallel()

	p := identity.Provider{
		Key: "discord", Kind: identity.KindDiscord, Enabled: true, VerifiableSubject: true,
		ClientID: "app-1", ClientSecret: core.Secret("operator-client-secret"),
	}

	rendered := p.LogValue().String()

	require.Contains(t, rendered, "discord")
	require.NotContains(t, rendered, "operator-client-secret")
}
