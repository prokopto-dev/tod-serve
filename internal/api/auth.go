package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
)

// PublicIdentityProvider is a provider as a stranger reads it, before any credential exists.
//
// It carries nothing that is not already public: the client id travels in every authorization URL
// a browser follows, and the authorization endpoint is where that browser is sent. The secret is
// absent from the struct rather than blanked in the renderer, which is the same discipline
// [AdminIdentityProvider] uses and for the same reason — there is no field to get wrong.
//
// `verifiable_subject` is here because it is the whole of a circle's revocation guarantee. A join
// page that lists providers without it is a join page that cannot tell somebody the truth about
// what revocation will mean for them.
type PublicIdentityProvider struct {
	// Key is what `/join` and `createAuthorizationURL` dispatch on. It is the id a client uses;
	// the provider row's own id is deliberately absent.
	Key string `json:"key"`
	// Kind is `discord`, `oidc` or `local`.
	Kind string `json:"kind" enum:"discord,oidc,local"`
	// DisplayName is what a sign-in button says.
	DisplayName string `json:"display_name"`
	// VerifiableSubject is a CHECK against `kind`, never a toggle.
	VerifiableSubject bool `json:"verifiable_subject"`
	// BrowserFlow says whether this provider has an OAuth redirect to start. A `local` provider
	// does not, and a client that cannot tell them apart offers a button that goes nowhere.
	BrowserFlow bool `json:"browser_flow"`
	// Issuer, AuthorizationEndpoint and ClientID are the OIDC discovery facts, empty for other
	// kinds. They are public by construction: the browser is sent to the endpoint carrying the
	// client id.
	Issuer                string `json:"issuer,omitempty"`
	AuthorizationEndpoint string `json:"authorization_endpoint,omitempty"`
	ClientID              string `json:"client_id,omitempty"`
}

// publicProvider renders an enabled provider for the discovery endpoint.
func publicProvider(p identity.Provider) PublicIdentityProvider {
	out := PublicIdentityProvider{
		Key:               p.Key,
		Kind:              string(p.Kind),
		DisplayName:       p.DisplayName,
		VerifiableSubject: p.VerifiableSubject,
		BrowserFlow:       p.SupportsBrowserFlow(),
	}
	if p.Kind == identity.KindOIDC {
		out.Issuer = p.Issuer
		out.AuthorizationEndpoint = p.AuthorizationEndpoint
	}
	if p.Kind != identity.KindLocal {
		out.ClientID = p.ClientID
	}
	return out
}

type listIdentityProvidersInput struct{}

type listIdentityProvidersOutput struct {
	Body Page[PublicIdentityProvider]
}

type createAuthorizationURLInput struct {
	Body struct {
		Provider string `json:"provider" doc:"A provider key from listIdentityProviders" maxLength:"40"`
		// InviteCode is optional and is the ONLY way a circle reaches this route. There is no
		// circle_id parameter, by design: a public route that answered differently for a real
		// circle than an unknown one would confirm a circle's existence to anybody who guessed
		// an id — including through which OAuth scopes the returned URL asks for.
		InviteCode string `json:"invite_code,omitempty" doc:"The invite code, if this is a join rather than a re-auth. A circle is resolved from this and from nothing else" maxLength:"120"`
	}
}

// AuthorizationStart is where to send the browser, and when the flow stops working.
//
// The `state` is deliberately absent. It is not a credential — it is a CSRF nonce whose only
// meaning is a row in `auth_flow` — but it is unguessable and the client has no use for it: the
// provider hands it back to `completeAuthorization` directly.
type AuthorizationStart struct {
	// AuthorizationURL is the provider's own authorization endpoint, fully parameterised.
	AuthorizationURL string `json:"authorization_url"`
	// ExpiresAt is when the `auth_flow` row stops being redeemable.
	ExpiresAt core.Micros `json:"expires_at"`
	// AsOf is the instant this answer was computed. Every countdown a client renders from it is a
	// signed offset from this, never from the browser's own clock.
	AsOf core.Micros `json:"as_of"`
}

type createAuthorizationURLOutput struct {
	Body AuthorizationStart
}

type completeAuthorizationInput struct {
	ProviderKey string `path:"provider_key" doc:"The provider whose redirect this is"`
	// Code and State are the provider's own query parameters. Neither is a credential for this
	// API: `code` is single-use, PKCE-bound and exchanged server-side inside this call, and
	// `state` is a CSRF nonce whose only meaning is a row in `auth_flow`.
	Code  string `query:"code" doc:"The authorization code, exchanged server-side"`
	State string `query:"state" doc:"The CSRF nonce that names the auth_flow row"`
	// Error is what a provider sends instead of a code when the user clicked Cancel.
	Error string `query:"error" doc:"The provider's refusal, e.g. access_denied"`
}

// completeAuthorizationOutput is a redirect and nothing else.
//
// There is no body on purpose: the browser is being sent somewhere, and the one thing it must not
// be shown is a page containing the ticket. The ticket rides in the redirect FRAGMENT, which no
// browser transmits to any server.
type completeAuthorizationOutput struct {
	Status   int
	Location string `header:"Location"`
	// CacheControl keeps a single-use ticket out of the browser's back-button cache and out of
	// any intermediary. A cached 302 is a replayable one.
	CacheControl string `header:"Cache-Control"`
}

// registerAuth attaches the three public operations a browser needs before it holds anything.
//
// They are the discovery half of ADR-0011: the instance is a confidential OAuth client, so the
// token exchange happens server-side and the browser only ever sees a `credential_ticket`. Because
// both browser providers land on that one ticket, the console has a single code path for `discord`
// and `oidc` alike.
func (s *Server) registerAuth() error {
	return errors.Join(
		registerFailure(OpListIdentityProviders, Register(s.api, OpListIdentityProviders,
			func(ctx context.Context, _ *listIdentityProvidersInput) (
				*listIdentityProvidersOutput, error,
			) {
				providers, err := s.cfg.Identities.EnabledProviders(ctx)
				if err != nil {
					return nil, fromIdentityError(err)
				}
				items := make([]PublicIdentityProvider, 0, len(providers))
				for _, p := range providers {
					items = append(items, publicProvider(p))
				}
				// No cursor: an instance holds at most one provider per kind, so there is no
				// second page to have and saying `has_more: false` is the honest version of that.
				return &listIdentityProvidersOutput{
					Body: NewPage(items, "", false, s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpCreateAuthorizationURL, Register(s.api, OpCreateAuthorizationURL,
			func(ctx context.Context, in *createAuthorizationURLInput) (
				*createAuthorizationURLOutput, error,
			) {
				// The shared invite-code bucket has already been drawn on by the route
				// middleware, before this handler runs, so a rejected probe writes no `auth_flow`
				// row. That ordering is the whole reason the limiter is on the route rather than
				// in here.
				started, err := s.cfg.Identities.CreateAuthorizationURL(ctx,
					identity.AuthorizationRequest{
						ProviderKey: in.Body.Provider,
						InviteCode:  in.Body.InviteCode,
					})
				if err != nil {
					return nil, fromIdentityError(err)
				}
				return &createAuthorizationURLOutput{Body: AuthorizationStart{
					AuthorizationURL: started.URL,
					ExpiresAt:        started.ExpiresAt,
					AsOf:             s.cfg.Clock.Now(),
				}}, nil
			})),

		registerFailure(OpCompleteAuthorization, Register(s.api, OpCompleteAuthorization,
			func(ctx context.Context, in *completeAuthorizationInput) (
				*completeAuthorizationOutput, error,
			) {
				callback, err := s.cfg.Identities.CompleteAuthorization(ctx,
					identity.CallbackRequest{
						ProviderKey:   in.ProviderKey,
						State:         in.State,
						Code:          in.Code,
						ProviderError: in.Error,
					})
				if err != nil {
					// The browser is redirected anyway: `callback.Location` carries
					// `#error=<code>` and the SPA renders it. Answering a problem body here would
					// leave somebody on a blank callback page with a JSON document, and the one
					// thing they can act on — "try again" — is on the join screen.
					//
					// The reason stays in the log, where an operator can find it. It is not
					// audited: `audit_log.circle_id` is NOT NULL and no circle is known here.
					s.cfg.Log.WarnContext(ctx, "authorization callback failed",
						slog.String("provider", in.ProviderKey),
						slog.String("code", string(callback.Code)))
				}
				if callback.Location == "" {
					// Defensive: the service populates Location on success AND on failure, so an
					// empty one is a bug rather than an outcome. A 302 to nowhere is worse than a
					// problem body.
					return nil, apierr.New(apierr.CodeInternalError,
						"the authorization callback produced no redirect")
				}
				return &completeAuthorizationOutput{
					Status:       http.StatusFound,
					Location:     callback.Location,
					CacheControl: "no-store",
				}, nil
			})),
	)
}

// CallbackPathParam is the one path parameter `completeAuthorization` takes. It is named here so
// that [CallbackBaseURL] can strip it from the registry's path rather than re-spelling that path,
// and so a rename is a compile error in one place.
const CallbackPathParam = "/{provider_key}"

// ErrCallbackPathChanged reports that the callback route no longer ends in the provider-key
// parameter, so its base can no longer be derived by removing one.
//
// It is an error rather than a panic because the caller is `serve` starting up, and a startup
// that refuses with a sentence beats one that dumps a stack — but it is not recoverable either:
// TestCallbackBaseURL_IsDerivedFromTheRouteRegistry fails first, in CI.
var ErrCallbackPathChanged = errors.New("the completeAuthorization route no longer ends in " + CallbackPathParam)

// ErrPublicURLNotAnOrigin reports a public URL carrying something an origin cannot carry: a query
// string, a fragment, or userinfo.
//
// Refused rather than stripped, and the difference matters. A provider key is appended to the
// callback base, so a preserved query puts it INSIDE the query — `…/callback?tenant=one/discord`
// — and the redirect URI then addresses `/api/v1/auth/callback` with no provider key, which is
// not a route this server has. A fragment is worse: no browser transmits anything after `#`, so
// the callback would arrive carrying no provider key at all and the flow could never complete.
//
// Stripping would produce a working URL that is not the one the operator configured, which is the
// same class of quiet surprise this whole check exists to remove. `$TOD_PUBLIC_URL` is an ORIGIN;
// a value that is not one is a mistake worth reading at startup.
var ErrPublicURLNotAnOrigin = errors.New("a public URL is an origin: it carries no query, fragment or userinfo")

// CallbackBaseURL renders the absolute URL a provider redirects back to, minus the provider key.
//
// It exists because the redirect URI an operator pastes into Discord's developer portal has to be
// the string this server is actually reachable at, character for character — Discord compares it
// literally — and the only way to be sure of that is to DERIVE it from the route registry and the
// instance's own public URL rather than to write it down a second time. A second copy is a way for
// the registered URI, the stored `identity_provider.redirect_uri` and the path this binary serves
// to differ silently, and the symptom of that is a sign-in that lands nowhere.
//
// internal/identity takes the result as a string rather than calling this itself, because this
// package imports that one.
func CallbackBaseURL(publicURL string) (string, error) {
	route, ok := Lookup(OpCompleteAuthorization)
	if !ok {
		return "", fmt.Errorf("no %s route in the registry", OpCompleteAuthorization)
	}
	path, found := strings.CutSuffix(route.FullPath(), CallbackPathParam)
	if !found {
		return "", fmt.Errorf("%s is %q: %w", OpCompleteAuthorization, route.FullPath(), ErrCallbackPathChanged)
	}

	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(publicURL), "/"))
	if err != nil {
		return "", fmt.Errorf("parse public url %q: %w", publicURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("public url %q is not absolute", publicURL)
	}
	// Checked on the PARSED url rather than by looking for `?` or `#` in the string, so an encoded
	// one is caught too. RawQuery is empty for both "no query" and a bare trailing `?`; ForceQuery
	// distinguishes them, and `https://host/?` appending a key yields `…/callback/?discord`, which
	// is as broken as the rest.
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.User != nil {
		return "", fmt.Errorf("public url %q: %w", publicURL, ErrPublicURLNotAnOrigin)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}
