package identity

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// Kind is `identity_provider.kind`.
type Kind string

// The kinds, initialised from the one enum catalogue so the wire value, the SQL CHECK and this
// constant cannot drift.
const (
	KindDiscord Kind = schemaenum.IdentityProviderKindDiscord
	KindOIDC    Kind = schemaenum.IdentityProviderKindOIDC
	KindLocal   Kind = schemaenum.IdentityProviderKindLocal
)

// Provider is one row of `identity_provider`.
//
// `VerifiableSubject` is a `CHECK ((kind = 'local') = (verifiable_subject = 0))` in the schema,
// not an operator toggle, because everything downstream about revocation strength hangs off it.
// [Provider.Validate] re-asserts that here so a row assembled in Go — a fixture, a seed, a future
// import — cannot claim a `local` provider is verifiable.
type Provider struct {
	ID                string
	Key               string
	Kind              Kind
	DisplayName       string
	Enabled           bool
	VerifiableSubject bool

	// OIDC only.
	Issuer                string
	AuthorizationEndpoint string
	JWKSURI               string
	SubjectClaim          string

	// The operator's own OAuth application. `CHECK ((kind = 'local') = (client_id IS NULL))`:
	// every provider that talks to a third party is an OAuth client of it, and `local` talks to
	// nobody. For `discord` the client id is what `application.id` is compared against; for
	// `oidc` it is what `aud` is compared against, which is the audience check itself.
	ClientID      string
	ClientSecret  core.Secret
	RedirectURI   string
	TokenEndpoint string
}

// ErrProviderInconsistent is returned by [Provider.Validate].
var ErrProviderInconsistent = errors.New("identity provider row is inconsistent with its kind")

// Validate re-asserts in Go what the schema asserts in SQL.
//
// Duplicating a CHECK is usually a smell. It is not one here: the CHECK protects the database and
// this protects everything assembled without going through it, and the failure this catches — a
// `local` provider whose `verifiable_subject` says 1 — silently turns a weak circle into one
// reporting `durable`, which is the false confidence the whole design is built against.
func (p Provider) Validate() error {
	switch p.Kind {
	case KindDiscord, KindOIDC, KindLocal:
	default:
		return fmt.Errorf("kind %q: %w", p.Kind, ErrProviderInconsistent)
	}
	if (p.Kind == KindLocal) != !p.VerifiableSubject {
		return fmt.Errorf("kind %q with verifiable_subject %t: %w", p.Kind, p.VerifiableSubject, ErrProviderInconsistent)
	}
	// Mirrors ck_identity_provider_application_matches_kind. `oidc` needs a client id as much as
	// `discord` does: with no audience to check, an ID token minted for a different relying party
	// at the same issuer verifies here, and §7's claim that `oidc` is structurally immune to the
	// replay hole stops being true.
	if (p.Kind == KindLocal) != (p.ClientID == "") {
		return fmt.Errorf("kind %q with client_id %q: %w", p.Kind, p.ClientID, ErrProviderInconsistent)
	}
	// A `discord` row with no secret is broken in every direction and saves cleanly, which is the
	// worst combination: `discord.New` refuses to build a client without one, so EVERY sign-in
	// fails with a 500 — at the moment somebody clicks the button, not at the moment somebody
	// configured it. Checking here makes it a sentence at configuration time instead.
	//
	// `discord` only, deliberately. An `oidc` provider legitimately has no secret when it serves
	// non-browser `id_token` clients, which need none — the secret is used only on the browser
	// path's token exchange. Requiring one there would refuse a configuration that works, and a
	// check with false positives is one somebody switches off.
	if p.Kind == KindDiscord && p.ClientSecret.IsZero() {
		return fmt.Errorf("discord provider %q has no client_secret: %w", p.Key, ErrProviderInconsistent)
	}
	if p.Kind == KindOIDC {
		if p.Issuer == "" || p.JWKSURI == "" {
			return fmt.Errorf("oidc provider %q names no issuer or no jwks uri: %w", p.Key, ErrProviderInconsistent)
		}
		for _, raw := range []string{p.Issuer, p.JWKSURI, p.AuthorizationEndpoint, p.TokenEndpoint} {
			if raw == "" {
				continue
			}
			u, err := url.Parse(raw)
			if err != nil {
				return fmt.Errorf("oidc provider %q: parse %q: %w", p.Key, raw, errors.Join(ErrProviderInconsistent, err))
			}
			// https only, checked here as well as in the dialer. An operator who pastes an
			// `http://` issuer should be told at configuration time, not at somebody's first
			// failed join.
			if u.Scheme != "https" {
				return fmt.Errorf("oidc provider %q: %q is not https: %w", p.Key, raw, ErrProviderInconsistent)
			}
		}
	}
	return nil
}

// SupportsBrowserFlow reports whether this provider can be reached through
// `createAuthorizationURL`. `local` cannot: there is nothing to redirect to.
func (p Provider) SupportsBrowserFlow() bool {
	return p.Kind == KindDiscord || p.Kind == KindOIDC
}

// LogValue renders a provider for slog with no secret in it.
//
// `client_secret` is a [core.Secret] and redacts itself on every path, so this is not what stops
// the secret leaking. It is what stops the NEXT field leaking: a struct logged whole is a struct
// whose future columns are logged too, and the fields worth reading are chosen here rather than
// by whoever adds the next one.
func (p Provider) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("key", p.Key),
		slog.String("kind", string(p.Kind)),
		slog.Bool("enabled", p.Enabled),
		slog.Bool("verifiable_subject", p.VerifiableSubject),
	)
}

var _ slog.LogValuer = Provider{}
