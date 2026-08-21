package api

import (
	"context"
	"errors"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
)

// AdminIdentityProvider is one row of the instance's provider registry, as an operator holding
// `instance.security.manage` reads it.
//
// **The client secret is not here and never will be.** It is a [core.Secret] in the domain, which
// renders as `***` everywhere; this type omits the field entirely, so there is no renderer to get
// wrong. `client_secret_set` is the fact an operator actually needs — whether the OAuth
// application is configured — and it is derivable without disclosing anything.
//
// The name is `AdminIdentityProvider` rather than `Provider` because the OpenAPI schema namer
// strips the package: a second type called `Provider` anywhere in this repository would collide
// with this one and panic at startup.
type AdminIdentityProvider struct {
	// ID is the provider row's id, and what the PATCH and DELETE paths take.
	ID string `json:"id"`
	// Key is the wire key `listIdentityProviders` publishes and `/join` dispatches on.
	Key string `json:"key"`
	// Kind is `discord`, `oidc` or `local`. Immutable: it decides `verifiable_subject`.
	Kind string `json:"kind"`
	// DisplayName is what a join page calls it.
	DisplayName string `json:"display_name"`
	// Enabled says whether this instance will accept it at all.
	Enabled bool `json:"enabled"`
	// VerifiableSubject is a CHECK against `kind`, never a toggle. A circle accepting a provider
	// with this false is weakly revocable, and says so.
	VerifiableSubject bool `json:"verifiable_subject"`

	// Issuer and the three beside it are the OIDC discovery configuration, empty for other kinds.
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	SubjectClaim          string `json:"subject_claim"`

	// ClientID is the operator's own OAuth application (ADR-0011). It is not a secret: it travels
	// in every authorization URL a browser follows.
	ClientID string `json:"client_id"`
	// ClientSecretSet says whether a secret is stored, which is all an operator needs to know and
	// all this API will ever say about it.
	ClientSecretSet bool   `json:"client_secret_set"`
	RedirectURI     string `json:"redirect_uri"`
	TokenEndpoint   string `json:"token_endpoint"`
}

// adminProvider renders a domain provider for the wire, with the secret dropped rather than
// blanked. See [AdminIdentityProvider].
func adminProvider(p identity.Provider) AdminIdentityProvider {
	return AdminIdentityProvider{
		ID:                    p.ID,
		Key:                   p.Key,
		Kind:                  string(p.Kind),
		DisplayName:           p.DisplayName,
		Enabled:               p.Enabled,
		VerifiableSubject:     p.VerifiableSubject,
		Issuer:                p.Issuer,
		AuthorizationEndpoint: p.AuthorizationEndpoint,
		JWKSURI:               p.JWKSURI,
		SubjectClaim:          p.SubjectClaim,
		ClientID:              p.ClientID,
		ClientSecretSet:       p.ClientSecret != "",
		RedirectURI:           p.RedirectURI,
		TokenEndpoint:         p.TokenEndpoint,
	}
}

// AdminIdentityProviderResponse is one provider and the instant it was read. `as_of` sits on the
// response rather than the view so the ETag can be computed over the view alone.
type AdminIdentityProviderResponse struct {
	AdminIdentityProvider
	AsOf core.Micros `json:"as_of"`
}

type listAdminIdentityProvidersInput struct{}

type listAdminIdentityProvidersOutput struct {
	Body Page[AdminIdentityProvider]
}

type createIdentityProviderInput struct {
	Body struct {
		Key         string `json:"key" doc:"The wire key /join dispatches on" maxLength:"40"`
		Kind        string `json:"kind" doc:"discord, oidc or local. Immutable: it decides verifiable_subject" enum:"discord,oidc,local"`
		DisplayName string `json:"display_name,omitempty" doc:"What a join page calls it. Defaults to the key" maxLength:"80"`
		Enabled     bool   `json:"enabled,omitempty" doc:"Defaults to false, so a half-configured application is never briefly live"`

		Issuer                string `json:"issuer,omitempty" doc:"OIDC only: the issuer, an absolute https URL"`
		AuthorizationEndpoint string `json:"authorization_endpoint,omitempty" doc:"OIDC only"`
		JWKSURI               string `json:"jwks_uri,omitempty" doc:"OIDC only: where the signing keys live"`
		SubjectClaim          string `json:"subject_claim,omitempty" doc:"OIDC only: the claim that carries the subject. Defaults to sub"`

		ClientID      string `json:"client_id,omitempty" doc:"The operator's own OAuth application. Required for discord and oidc, forbidden for local"`
		ClientSecret  string `json:"client_secret,omitempty" doc:"Write-only: it is never returned by any operation"`
		RedirectURI   string `json:"redirect_uri,omitempty" doc:"Where the provider sends the browser back"`
		TokenEndpoint string `json:"token_endpoint,omitempty" doc:"Where the authorization code is exchanged"`

		AcknowledgeWeakRevocation bool `json:"acknowledge_weak_revocation,omitempty" doc:"Required to ENABLE a provider with no verifiable subject: revocation through one is advisory"`
	}
}

type createIdentityProviderOutput struct {
	Body AdminIdentityProviderResponse
}

type updateIdentityProviderInput struct {
	ProviderID string `path:"provider_id" doc:"The provider"`
	IfMatch    string `header:"If-Match" doc:"The ETag a previous read returned"`
	Body       struct {
		DisplayName *string `json:"display_name,omitempty" maxLength:"80"`
		Enabled     *bool   `json:"enabled,omitempty"`

		Issuer                *string `json:"issuer,omitempty"`
		AuthorizationEndpoint *string `json:"authorization_endpoint,omitempty"`
		JWKSURI               *string `json:"jwks_uri,omitempty"`
		SubjectClaim          *string `json:"subject_claim,omitempty"`

		ClientID      *string `json:"client_id,omitempty"`
		ClientSecret  *string `json:"client_secret,omitempty" doc:"Write-only. Send it to rotate; omit it to leave the stored one alone"`
		RedirectURI   *string `json:"redirect_uri,omitempty"`
		TokenEndpoint *string `json:"token_endpoint,omitempty"`

		AcknowledgeWeakRevocation bool `json:"acknowledge_weak_revocation,omitempty" doc:"Required when this change ENABLES a provider with no verifiable subject"`

		// Key and Kind are accepted only so that sending either is REFUSED with the code that says
		// why. Ignoring them would let a client believe a provider had been renamed or retyped,
		// and `kind` decides `verifiable_subject`, which is what every circle's revocation
		// strength is derived from.
		Key  *string `json:"key,omitempty" doc:"Rejected with 422 field_immutable"`
		Kind *string `json:"kind,omitempty" doc:"Rejected with 422 field_immutable: kind decides verifiable_subject"`
	}
}

type updateIdentityProviderOutput struct {
	ETag string `header:"ETag"`
	Body AdminIdentityProviderResponse
}

type deleteIdentityProviderInput struct {
	ProviderID string `path:"provider_id" doc:"The provider"`
}

type deleteIdentityProviderOutput struct {
	Body AdminIdentityProviderResponse
}

// registerAdmin attaches the instance administration operations.
//
// All four carry `instance.security.manage`, which is instance-realm: no circle role reaches them,
// no PAT reaches them at any scope, and what does reach them is an `instance_grant` on the caller's
// identity (ADR-0012). Until that ledger existed these routes were in the registry and served by
// nothing, which is why `configure the Discord provider` was a command-line-only operation.
func (s *Server) registerAdmin() error {
	return errors.Join(
		registerFailure(OpListAdminIdentityProviders, Register(s.api, OpListAdminIdentityProviders,
			func(ctx context.Context, _ *listAdminIdentityProvidersInput) (
				*listAdminIdentityProvidersOutput, error,
			) {
				providers, err := s.cfg.Identities.Providers(ctx)
				if err != nil {
					return nil, fromIdentityError(err)
				}
				items := make([]AdminIdentityProvider, 0, len(providers))
				for _, p := range providers {
					items = append(items, adminProvider(p))
				}
				// No cursor: an instance has a handful of providers and there is no second page to
				// have. Saying `has_more: false` is the honest version of that.
				return &listAdminIdentityProvidersOutput{
					Body: NewPage(items, "", false, s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpCreateIdentityProvider, Register(s.api, OpCreateIdentityProvider,
			func(ctx context.Context, in *createIdentityProviderInput) (
				*createIdentityProviderOutput, error,
			) {
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				key, _ := IdempotencyKeyFrom(ctx)
				// The client secret is deliberately absent from the request hash. A retry carrying
				// the same secret hashes the same way without the secret ever reaching the
				// idempotency record, which is a table read by more code than this handler.
				hash := hashBody("createIdentityProvider", in.Body.Key, in.Body.Kind,
					in.Body.DisplayName, in.Body.ClientID, in.Body.Issuer, in.Body.RedirectURI)

				out, _, err := runIdempotentHandler(ctx, s.api, p, key, hash,
					func(ctx context.Context) (AdminIdentityProviderResponse, error) {
						created, addErr := s.cfg.Identities.AddProvider(ctx,
							identity.AddProviderRequest{
								Key:                       in.Body.Key,
								Kind:                      identity.Kind(in.Body.Kind),
								DisplayName:               in.Body.DisplayName,
								Enabled:                   in.Body.Enabled,
								Issuer:                    in.Body.Issuer,
								AuthorizationEndpoint:     in.Body.AuthorizationEndpoint,
								JWKSURI:                   in.Body.JWKSURI,
								SubjectClaim:              in.Body.SubjectClaim,
								ClientID:                  in.Body.ClientID,
								ClientSecret:              core.Secret(in.Body.ClientSecret),
								RedirectURI:               in.Body.RedirectURI,
								TokenEndpoint:             in.Body.TokenEndpoint,
								AcknowledgeWeakRevocation: in.Body.AcknowledgeWeakRevocation,
							})
						if addErr != nil {
							return AdminIdentityProviderResponse{}, fromIdentityError(addErr)
						}
						return AdminIdentityProviderResponse{
							AdminIdentityProvider: adminProvider(created),
							AsOf:                  s.cfg.Clock.Now(),
						}, nil
					})
				if err != nil {
					return nil, err
				}
				return &createIdentityProviderOutput{Body: out}, nil
			})),

		registerFailure(OpUpdateIdentityProvider, Register(s.api, OpUpdateIdentityProvider,
			func(ctx context.Context, in *updateIdentityProviderInput) (
				*updateIdentityProviderOutput, error,
			) {
				if in.Body.Key != nil || in.Body.Kind != nil {
					// Refused before the read, so the answer does not depend on what the current
					// row says. A kind change would restate what revocation means for every circle
					// already accepting this provider, and it would do it silently.
					return nil, apierr.New(apierr.CodeFieldImmutable,
						"a provider's key and kind are immutable: kind decides "+
							"verifiable_subject, which every circle's revocation strength is "+
							"derived from. Delete it and add it again, which is refused once "+
							"anybody has joined through it").
						WithField("body.kind", "immutable")
				}

				current, err := s.cfg.Identities.ProviderByID(ctx, in.ProviderID)
				if err != nil {
					return nil, fromIdentityError(err)
				}
				if err := RequireIfMatch(in.IfMatch, adminProvider(current)); err != nil {
					return nil, err
				}

				var secret *core.Secret
				if in.Body.ClientSecret != nil {
					s := core.Secret(*in.Body.ClientSecret)
					secret = &s
				}
				updated, err := s.cfg.Identities.ChangeProvider(ctx, in.ProviderID,
					identity.ChangeProviderRequest{
						DisplayName:               in.Body.DisplayName,
						Enabled:                   in.Body.Enabled,
						Issuer:                    in.Body.Issuer,
						AuthorizationEndpoint:     in.Body.AuthorizationEndpoint,
						JWKSURI:                   in.Body.JWKSURI,
						SubjectClaim:              in.Body.SubjectClaim,
						ClientID:                  in.Body.ClientID,
						ClientSecret:              secret,
						RedirectURI:               in.Body.RedirectURI,
						TokenEndpoint:             in.Body.TokenEndpoint,
						AcknowledgeWeakRevocation: in.Body.AcknowledgeWeakRevocation,
					})
				if err != nil {
					return nil, fromIdentityError(err)
				}
				view := adminProvider(updated)
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &updateIdentityProviderOutput{
					ETag: etag,
					Body: AdminIdentityProviderResponse{
						AdminIdentityProvider: view, AsOf: s.cfg.Clock.Now(),
					},
				}, nil
			})),

		registerFailure(OpDeleteIdentityProvider, Register(s.api, OpDeleteIdentityProvider,
			func(ctx context.Context, in *deleteIdentityProviderInput) (
				*deleteIdentityProviderOutput, error,
			) {
				removed, err := s.cfg.Identities.RemoveProvider(ctx, in.ProviderID)
				if err != nil {
					return nil, fromIdentityError(err)
				}
				return &deleteIdentityProviderOutput{
					Body: AdminIdentityProviderResponse{
						AdminIdentityProvider: adminProvider(removed),
						AsOf:                  s.cfg.Clock.Now(),
					},
				}, nil
			})),
	)
}

// fromIdentityError renders internal/identity's coded error as the problem the edge sends.
//
// The two vocabularies are one vocabulary — both are docs/design/02-api-design.md's closed enum —
// so this is a rename rather than a translation, and an error with no code becomes an internal
// error rather than a guess. It mirrors `membership.fromIdentity`, which does the same job on the
// join path; the duplication is one function and the alternative is internal/api importing a
// helper out of internal/membership to talk about internal/identity.
func fromIdentityError(err error) error {
	if err == nil {
		return nil
	}
	var coded *identity.Error
	if !errors.As(err, &coded) {
		return apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	problem := apierr.Wrap(apierr.Code(coded.Code), err, coded.Message)
	if coded.Location != "" {
		problem = problem.WithField(coded.Location, coded.Message)
	}
	return problem
}
