package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
)

// AddProviderRequest is one new row of the instance's provider registry.
//
// There is no `verifiable_subject` field, and its absence is the design: verifiability is a CHECK
// against `kind`, so it is DERIVED here and a caller cannot assert that a `local` provider is
// verifiable. Everything downstream about revocation strength hangs off that one column.
type AddProviderRequest struct {
	// Key is the wire key `listIdentityProviders` publishes and `/join` dispatches on.
	Key string
	// Kind is `discord`, `oidc` or `local`.
	Kind Kind
	// DisplayName is what a join page calls it.
	DisplayName string
	// Enabled says whether it can be used at all. A provider is added disabled unless the request
	// says otherwise, so a half-configured OAuth application is not briefly live.
	Enabled bool

	// The OIDC discovery fields.
	Issuer                string
	AuthorizationEndpoint string
	JWKSURI               string
	SubjectClaim          string

	// The operator's own OAuth application (ADR-0011). `local` has none and must not be given one.
	ClientID      string
	ClientSecret  core.Secret
	RedirectURI   string
	TokenEndpoint string

	// AcknowledgeWeakRevocation is docs/design/04-identity §6 at the API. Enabling a provider with
	// no verifiable subject means a revoked member returns under a new name, and the damage is the
	// officers' belief that revocation worked — so it takes a word the operator had to type.
	AcknowledgeWeakRevocation bool
}

// ChangeProviderRequest rewrites a provider's mutable configuration.
//
// `Key` and `Kind` are absent because they are immutable: a kind change would restate what
// revocation means for every circle already accepting the provider, silently. The edge answers
// `422 field_immutable` for a request that names either.
type ChangeProviderRequest struct {
	DisplayName *string
	Enabled     *bool

	Issuer                *string
	AuthorizationEndpoint *string
	JWKSURI               *string
	SubjectClaim          *string

	ClientID      *string
	ClientSecret  *core.Secret
	RedirectURI   *string
	TokenEndpoint *string

	AcknowledgeWeakRevocation bool
}

// Providers returns every provider on the instance, disabled ones included.
//
// This is the ADMINISTRATIVE read behind `instance.security.manage`, not the public one: an
// operator configuring the instance has to see the row they turned off, and a caller with no
// credential must not. `EnabledProviders` is the public question and stays separate for exactly
// that reason.
func (s *Service) Providers(ctx context.Context) ([]Provider, error) {
	providers, err := s.store.AllProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the instance's providers: %w", err)
	}
	return providers, nil
}

// ProviderByID returns one provider by its id.
//
// It is separate from [Service.Provider], which takes a wire key: the administrative surface
// addresses a row by the id in its path and a caller must be able to rename nothing by finding it.
func (s *Service) ProviderByID(ctx context.Context, id string) (Provider, error) {
	p, err := s.store.ProviderByID(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Provider{}, NewError(CodeNotFound, "no such identity provider", err)
	}
	if err != nil {
		return Provider{}, err
	}
	return p, nil
}

// AddProvider writes a new provider row.
//
// The order of the checks is the rule: shape first, then the acknowledgement, then the write. A
// request that is both malformed and unacknowledged should hear about the malformed half, because
// acknowledging weak revocation for a provider that was never going to work is a word typed for
// nothing.
func (s *Service) AddProvider(ctx context.Context, req AddProviderRequest) (Provider, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return Provider{}, NewValidationError("body.key", "a provider needs a key")
	}
	candidate := Provider{
		ID:          "",
		Key:         key,
		Kind:        req.Kind,
		DisplayName: strings.TrimSpace(req.DisplayName),
		Enabled:     req.Enabled,
		// Derived, never taken: `CHECK ((kind = 'local') = (verifiable_subject = 0))`.
		VerifiableSubject:     req.Kind != KindLocal,
		Issuer:                strings.TrimSpace(req.Issuer),
		AuthorizationEndpoint: strings.TrimSpace(req.AuthorizationEndpoint),
		JWKSURI:               strings.TrimSpace(req.JWKSURI),
		SubjectClaim:          strings.TrimSpace(req.SubjectClaim),
		ClientID:              strings.TrimSpace(req.ClientID),
		ClientSecret:          req.ClientSecret,
		RedirectURI:           strings.TrimSpace(req.RedirectURI),
		TokenEndpoint:         strings.TrimSpace(req.TokenEndpoint),
	}
	if candidate.DisplayName == "" {
		candidate.DisplayName = key
	}
	if err := validateProvider(candidate); err != nil {
		return Provider{}, err
	}
	// After the shape check and before the acknowledgement, for the same reason the
	// acknowledgement is last: a redirect URI that cannot work is a row that was never going to
	// work, and hearing about the weak-revocation flag first would be a word typed for nothing.
	if err := s.checkRedirectURI(candidate); err != nil {
		return Provider{}, redirectURIValidationError(err, s.ExpectedRedirectURI(candidate.Key))
	}
	if err := checkWeakAcknowledgement(candidate, req.AcknowledgeWeakRevocation); err != nil {
		return Provider{}, err
	}

	if _, err := s.store.ProviderByKey(ctx, key); err == nil {
		return Provider{}, duplicateKeyError(nil)
	} else if !errors.Is(err, ErrNotFound) {
		return Provider{}, err
	}
	if err := s.checkKindIsFree(ctx, candidate.Kind); err != nil {
		return Provider{}, err
	}

	now := s.clock.Now()
	id, err := core.NewID[core.IdentityProvider](s.ids, now)
	if err != nil {
		return Provider{}, fmt.Errorf("mint an identity provider id: %w", err)
	}
	candidate.ID = id.String()

	created, err := s.store.CreateProvider(ctx, candidate, now)
	if err != nil {
		// The two preflight reads above are outside this write, so a second request that passed
		// them a moment earlier can commit first and leave this one losing on
		// `ux_identity_provider_key` or one of the two partial kind indexes. The database is the
		// authority either way; what must not happen is the documented `409` becoming a `500`
		// because the loser's answer depended on which of two identical requests was slower.
		//
		// Coded HERE rather than in the adapter because the CONFLICT is this function's rule —
		// the adapter knows a constraint was violated and not which of them means "already
		// exists".
		if store.IsUniqueViolation(err) {
			return Provider{}, duplicateProviderError(candidate, err)
		}
		return Provider{}, err
	}
	// The key and the kind, never the secret. An operator reading a log to find out what changed
	// needs the row; nobody needs the client secret in a log line, ever.
	s.log.InfoContext(ctx, "identity provider added",
		"provider_key", created.Key, "kind", string(created.Kind),
		"enabled", created.Enabled, "verifiable_subject", created.VerifiableSubject)
	return created, nil
}

// ChangeProvider rewrites a provider's mutable configuration.
//
// Every field is a pointer, so "not sent" and "sent empty" are different requests: clearing an
// issuer and leaving it alone are different intentions, and a struct of values could not tell them
// apart.
func (s *Service) ChangeProvider(
	ctx context.Context, id string, req ChangeProviderRequest,
) (Provider, error) {
	current, err := s.ProviderByID(ctx, id)
	if err != nil {
		return Provider{}, err
	}

	next := current
	setString(&next.DisplayName, req.DisplayName)
	setString(&next.Issuer, req.Issuer)
	setString(&next.AuthorizationEndpoint, req.AuthorizationEndpoint)
	setString(&next.JWKSURI, req.JWKSURI)
	setString(&next.SubjectClaim, req.SubjectClaim)
	setString(&next.ClientID, req.ClientID)
	setString(&next.RedirectURI, req.RedirectURI)
	setString(&next.TokenEndpoint, req.TokenEndpoint)
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
	}
	if req.ClientSecret != nil {
		next.ClientSecret = *req.ClientSecret
	}
	if next.DisplayName == "" {
		next.DisplayName = next.Key
	}

	if err := validateProvider(next); err != nil {
		return Provider{}, err
	}
	if err := s.checkRedirectURI(next); err != nil {
		return Provider{}, redirectURIValidationError(err, s.ExpectedRedirectURI(next.Key))
	}
	// Only when the change turns it ON. Re-acknowledging a `local` provider that was already
	// enabled on every unrelated edit would train an operator to pass the flag by reflex, which is
	// the opposite of what it is for.
	if !current.Enabled && next.Enabled {
		if err := checkWeakAcknowledgement(next, req.AcknowledgeWeakRevocation); err != nil {
			return Provider{}, err
		}
	}

	updated, err := s.store.UpdateProvider(ctx, next, s.clock.Now())
	if err != nil {
		return Provider{}, err
	}
	s.log.InfoContext(ctx, "identity provider changed",
		"provider_key", updated.Key, "kind", string(updated.Kind), "enabled", updated.Enabled)
	return updated, nil
}

// RemoveProvider deletes a provider row.
//
// Foreign keys are NO ACTION everywhere, so this is REFUSED by the database once any identity,
// auth flow, credential ticket or circle references the row — and that refusal is the right
// answer rather than an inconvenience. Removing a provider people joined through would orphan
// their identities; DISABLING it is the operation that stops new joins, and it is what the 409
// points at.
func (s *Service) RemoveProvider(ctx context.Context, id string) (Provider, error) {
	current, err := s.ProviderByID(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if err := s.store.DeleteProvider(ctx, id); err != nil {
		return Provider{}, NewError(CodeConflict,
			"this provider is still referenced by an identity, a circle or an in-flight "+
				"authorization; disable it instead, which stops new joins and revokes nobody",
			err)
	}
	s.log.InfoContext(ctx, "identity provider removed",
		"provider_key", current.Key, "kind", string(current.Kind))
	return current, nil
}

// duplicateProviderError is the answer to "something with these identifying values is already
// there", whichever of the three unique indexes said so.
//
// It cannot tell which one from the driver's error without matching on a message, so it names both
// possibilities. A caller who has just been told their key is taken and whose key is unique reads
// the second half; the alternative is a 500.
func duplicateProviderError(candidate Provider, cause error) *Error {
	if candidate.Kind == KindDiscord || candidate.Kind == KindLocal {
		return NewError(CodeConflict, fmt.Sprintf(
			"a provider with that key, or another %q provider, already exists on this instance: "+
				"there is at most one of each of those", candidate.Kind), cause)
	}
	return duplicateKeyError(cause)
}

func duplicateKeyError(cause error) *Error {
	return NewError(CodeConflict,
		"a provider with that key already exists on this instance", cause)
}

// checkKindIsFree refuses a second `discord` or a second `local` row.
//
// The schema refuses it too — `ux_identity_provider_discord` and `ux_identity_provider_local` are
// partial unique indexes — and this exists so the caller reads a sentence naming the row already
// there rather than a constraint name behind a 500. Any number of `oidc` providers is fine: an
// instance can federate with several issuers, and each is a different organisation.
func (s *Service) checkKindIsFree(ctx context.Context, kind Kind) error {
	if kind != KindDiscord && kind != KindLocal {
		return nil
	}
	existing, err := s.store.AllProviders(ctx)
	if err != nil {
		return fmt.Errorf("list the instance's providers: %w", err)
	}
	for _, p := range existing {
		if p.Kind == kind {
			return NewError(CodeConflict, fmt.Sprintf(
				"this instance already has a %q provider, %q: there is at most one, because a "+
					"second would be a second OAuth application for the same third party",
				kind, p.Key), nil)
		}
	}
	return nil
}

// validateProvider turns [Provider.Validate]'s sentinel into the wire code the edge sends.
func validateProvider(p Provider) error {
	if err := p.Validate(); err != nil {
		coded := NewError(CodeValidationFailed, providerMessage(p), err)
		coded.Location = "body"
		return coded
	}
	return nil
}

// providerMessage says what is wrong in terms of the row's kind, because "inconsistent with its
// kind" is true and useless to somebody pasting a Discord client id into an OIDC provider.
func providerMessage(p Provider) string {
	switch p.Kind {
	case KindDiscord:
		return "a discord provider needs a client id AND a client secret: the instance is a " +
			"confidential OAuth client of the operator's own Discord application (ADR-0011), so " +
			"it performs the token exchange itself. Both are on the application's OAuth2 page; " +
			"the secret is shown once, and resetting it invalidates the previous one"
	case KindOIDC:
		return "an oidc provider needs an issuer, a jwks uri and a client id, each an absolute " +
			"https url where it is a url: with no audience to check, an id token minted for a " +
			"different relying party at the same issuer would verify here"
	case KindLocal:
		return "a local provider has no client id, no issuer and no verifiable subject; it talks " +
			"to nobody"
	default:
		return "kind must be discord, oidc or local"
	}
}

// checkWeakAcknowledgement is docs/design/04-identity §6 at the API, and the mirror of what
// `tod-serve init --local` demands at the command line.
func checkWeakAcknowledgement(p Provider, acknowledged bool) error {
	if !p.Enabled || p.VerifiableSubject || acknowledged {
		return nil
	}
	return &Error{
		Code: CodeAcknowledgementRequired,
		Message: "enabling a provider with no verifiable subject makes revocation ADVISORY: a " +
			"revoked member holding any live invite returns under a new name, and the damage is " +
			"the officers' belief that revocation worked. Send acknowledge_weak_revocation to " +
			"accept that",
		Location: "body.acknowledge_weak_revocation",
	}
}

func setString(dst *string, v *string) {
	if v != nil {
		*dst = strings.TrimSpace(*v)
	}
}
