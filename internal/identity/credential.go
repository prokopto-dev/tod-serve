package identity

import (
	"fmt"
	"log/slog"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// CredentialKind discriminates the union [ADR-0007] chose over a route per provider.
//
// [ADR-0007]: docs/adr/0007-one-join-endpoint.md
type CredentialKind string

const (
	// CredentialProviderTicket is any browser flow — `discord` and `oidc` alike. It exists
	// because [ADR-0011] makes the instance a confidential OAuth client, so the code exchange
	// happens server-side and the browser never touches a provider token. Both browser providers
	// land on one ticket, which is what gives the SPA a single code path.
	//
	// [ADR-0011]: docs/adr/0011-operator-registered-discord-application.md
	CredentialProviderTicket CredentialKind = "provider_ticket"

	// CredentialBearerToken is a Discord access token supplied by a client with no browser to
	// redirect. Its safety rests ENTIRELY on the audience check — see the package docs of
	// internal/identity/discord — rather than on the shape of the flow, which is why it exists on
	// sufferance rather than by preference.
	CredentialBearerToken CredentialKind = "bearer_token"

	// CredentialIDToken is an OIDC ID token plus the nonce it was minted for, for a non-browser
	// client.
	CredentialIDToken CredentialKind = "id_token"

	// CredentialNone is `local`, which has nothing to present.
	CredentialNone CredentialKind = "none"
)

// The field paths validation errors point at. Named, because they appear in the API design, in
// the error a client renders, and in this package's tests, and three spellings of one string is
// the drift this repository gates against everywhere else.
const (
	LocationCredentialKind    = "body.credential.kind"
	LocationCredentialToken   = "body.credential.token"
	LocationCredentialTicket  = "body.credential.ticket"
	LocationCredentialIDToken = "body.credential.id_token"
	LocationCredentialNonce   = "body.credential.nonce"
	LocationDisplayName       = "body.display_name"
)

// Credential is the union. One shape in the system rather than one per provider, and one per
// endpoint would be two.
type Credential struct {
	Kind CredentialKind

	// Ticket is the `provider_ticket`, delivered to the SPA in the redirect FRAGMENT.
	Ticket string

	// Token is a Discord access token on the `bearer_token` path. A [core.Secret] because it is
	// one: it renders as `***` everywhere, including in the error somebody pastes into an issue.
	Token core.Secret

	// IDToken and Nonce are the `id_token` path.
	IDToken string
	Nonce   string
}

// Validate checks the union against the provider it is offered for.
//
// Two questions, and they fail differently on purpose: a credential whose shape is wrong is the
// client's bug and points at a field; a credential whose KIND this provider does not accept is a
// configuration mismatch and points at the kind.
func (c Credential) Validate(p Provider) error {
	switch c.Kind {
	case CredentialProviderTicket:
		if !p.SupportsBrowserFlow() {
			return NewValidationError(LocationCredentialKind,
				fmt.Sprintf("provider %q has no browser flow, so it issues no ticket", p.Key))
		}
		if c.Ticket == "" {
			return NewValidationError(LocationCredentialTicket, "a provider_ticket credential carries a ticket")
		}

	case CredentialBearerToken:
		if p.Kind != KindDiscord {
			return NewValidationError(LocationCredentialKind,
				fmt.Sprintf("bearer_token is the non-browser discord credential; provider %q is %s", p.Key, p.Kind))
		}
		if c.Token.IsZero() {
			return NewValidationError(LocationCredentialToken, "a bearer_token credential carries a token")
		}

	case CredentialIDToken:
		if p.Kind != KindOIDC {
			return NewValidationError(LocationCredentialKind,
				fmt.Sprintf("id_token is the non-browser oidc credential; provider %q is %s", p.Key, p.Kind))
		}
		if c.IDToken == "" {
			return NewValidationError(LocationCredentialIDToken, "an id_token credential carries an id_token")
		}
		if c.Nonce == "" {
			// Not optional. A nonce is what binds an ID token to the authorization request that
			// asked for it; accepting one without it takes any token the subject has ever been
			// issued for this client.
			return NewValidationError(LocationCredentialNonce, "an id_token credential carries the nonce it was minted for")
		}

	case CredentialNone:
		if p.Kind != KindLocal {
			return NewValidationError(LocationCredentialKind,
				fmt.Sprintf("provider %q requires a credential", p.Key))
		}

	default:
		return NewValidationError(LocationCredentialKind,
			fmt.Sprintf("credential kind %q is not one of provider_ticket, bearer_token, id_token, none", c.Kind))
	}
	return nil
}

// LogValue keeps a credential out of a log line whole. The kind is loggable and is what an
// operator debugging a join needs; every other field in here is a bearer credential.
func (c Credential) LogValue() slog.Value {
	return slog.GroupValue(slog.String("kind", string(c.Kind)))
}

var _ slog.LogValuer = Credential{}
