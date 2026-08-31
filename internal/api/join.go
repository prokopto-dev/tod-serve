package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/membership"
)

// CredentialBody is the discriminated union [ADR-0007] chose over a route per provider.
//
// One shape in the system rather than one per provider — and `/join` and `/sessions` take the same
// one, so there is a single credential to secure, audit and rate-limit. The cost is stated in the
// ADR: a `oneOf` with a discriminator is uglier in a generated SDK than three clean bodies, and
// part of the validation lives in the service rather than purely in the schema.
//
// [ADR-0007]: docs/adr/0007-one-join-endpoint.md
type CredentialBody struct {
	Kind string `json:"kind" doc:"provider_ticket, bearer_token, id_token or none" enum:"provider_ticket,bearer_token,id_token,none"`
	// Ticket is the single-use, 120-second `credential_ticket` the OAuth callback delivered in the
	// redirect FRAGMENT. Any browser flow — discord and oidc alike.
	Ticket string `json:"ticket,omitempty"`
	// Token is a Discord access token, for a client with no browser to redirect. Its safety rests
	// entirely on the audience check inside internal/identity/discord.
	Token string `json:"token,omitempty"`
	// IDToken and Nonce are the non-browser OIDC path. The nonce is not optional: it is what binds
	// an ID token to the authorization request that asked for it.
	IDToken string `json:"id_token,omitempty"`
	Nonce   string `json:"nonce,omitempty"`
}

func (c CredentialBody) toCredential() identity.Credential {
	return identity.Credential{
		Kind:    identity.CredentialKind(c.Kind),
		Ticket:  c.Ticket,
		Token:   core.Secret(c.Token),
		IDToken: c.IDToken,
		Nonce:   c.Nonce,
	}
}

// ClientBody names the device a token is being minted for, so a person can recognise it in their
// own token list and cut off the one they no longer have.
type ClientBody struct {
	Name    string `json:"name,omitempty" doc:"e.g. nparse-plus-tod" maxLength:"64"`
	Version string `json:"version,omitempty" maxLength:"32"`
}

type redeemInviteInput struct {
	Body struct {
		// InviteCode travels in the body, never the path. The link an officer pastes carries it in
		// the URL FRAGMENT, which no browser transmits at all.
		InviteCode  string         `json:"invite_code" doc:"The invite code, in any case, with or without the TODI- prefix"`
		Provider    string         `json:"provider" doc:"A provider key from previewInvite's providers[]"`
		Credential  CredentialBody `json:"credential"`
		DisplayName string         `json:"display_name,omitempty" doc:"Required for local, optional elsewhere, where it overrides what the provider reported" maxLength:"64"`
		Client      ClientBody     `json:"client,omitempty"`
		Scopes      []string       `json:"scopes,omitempty" doc:"Narrow the minted token. Empty means every scope, still bounded by the role"`
	}
}

// redeemInviteOutput carries the minted PAT in the body and a browser session in a `Set-Cookie`.
//
// Both, from one operation, because both kinds of client come through this door. A plugin reads
// the token and never looks at the cookie; a browser is handed the one credential that reaches
// the capability floor, which no token reaches at any scope. Neither client has to know the other
// exists — and there is no second, browser-only route to redeem an invite, which is exactly the
// back door the API-parity gate exists to refuse.
type redeemInviteOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      membership.Joined
}

type authenticateIdentityInput struct {
	Body struct {
		// CircleID is accepted here and nowhere else on a public route, and only WITH a
		// credential: the circle is resolved after the credential verifies, and every failure
		// answers 404 so the route confirms nothing about which circles exist.
		CircleID    string         `json:"circle_id" doc:"The circle to re-authenticate into"`
		Provider    string         `json:"provider" doc:"A provider key"`
		Credential  CredentialBody `json:"credential"`
		DisplayName string         `json:"display_name,omitempty" maxLength:"64"`
		Client      ClientBody     `json:"client,omitempty"`
		Scopes      []string       `json:"scopes,omitempty"`
	}
}

type authenticateIdentityOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      membership.Joined
}

// registerJoin attaches the two public operations that mint a token.
//
// They are one credential union and one code path on purpose. The guild gate is evaluated in BOTH,
// through the single evaluator in internal/identity: a gate checked only at join is a gate
// somebody walks around by re-authing on a new device.
func (s *Server) registerJoin() error {
	return errors.Join(
		registerFailure(OpRedeemInvite, Register(s.api, OpRedeemInvite,
			func(ctx context.Context, in *redeemInviteInput) (*redeemInviteOutput, error) {
				key, _ := IdempotencyKeyFrom(ctx)
				joined, err := s.cfg.Members.Join(ctx, membership.JoinRequest{
					Code:           in.Body.InviteCode,
					ProviderKey:    in.Body.Provider,
					Credential:     in.Body.Credential.toCredential(),
					DisplayName:    in.Body.DisplayName,
					ClientName:     clientName(in.Body.Client),
					Scopes:         in.Body.Scopes,
					IdempotencyKey: key,
				})
				if err != nil {
					return nil, err
				}
				cookie, err := s.sessionCookie(joined)
				if err != nil {
					return nil, err
				}
				return &redeemInviteOutput{SetCookie: cookie, Body: joined}, nil
			})),

		registerFailure(OpAuthenticateIdentity, Register(s.api, OpAuthenticateIdentity,
			func(ctx context.Context, in *authenticateIdentityInput) (*authenticateIdentityOutput, error) {
				circleID, err := core.ParseID[core.Circle](in.Body.CircleID)
				if err != nil {
					// A malformed circle id answers exactly what an unknown one does. Anything
					// narrower would tell a caller their guess was at least well-formed.
					return nil, apierr.Wrap(apierr.CodeNotFound, err,
						"no membership for this identity in that circle")
				}
				key, _ := IdempotencyKeyFrom(ctx)
				joined, err := s.cfg.Members.Authenticate(ctx, membership.AuthenticateRequest{
					CircleID:       circleID,
					ProviderKey:    in.Body.Provider,
					Credential:     in.Body.Credential.toCredential(),
					DisplayName:    in.Body.DisplayName,
					ClientName:     clientName(in.Body.Client),
					Scopes:         in.Body.Scopes,
					IdempotencyKey: key,
				})
				if err != nil {
					return nil, err
				}
				cookie, err := s.sessionCookie(joined)
				if err != nil {
					return nil, err
				}
				return &authenticateIdentityOutput{SetCookie: cookie, Body: joined}, nil
			})),
	)
}

// clientName renders the device name a token is filed under. The version is folded in because
// "nparse-plus-tod 1.2.0" is what somebody scanning their own device list can actually act on.
func clientName(c ClientBody) string {
	switch {
	case c.Name == "":
		return ""
	case c.Version == "":
		return c.Name
	default:
		return c.Name + " " + c.Version
	}
}

// sessionCookie mints the browser session for a caller who has just proved their identity.
//
// `SteppedUpAt` is now, and that is the definition rather than a convenience: step-up asks whether
// the identity was proved recently, and a credential verified inside this request is the strongest
// answer that question can have. It decays on the same five-minute window every other session
// does, so an operator who leaves the console open still re-authenticates before revoking anybody.
//
// The cookie is set for every caller, including one that will never send it back. A plugin
// ignoring a `Set-Cookie` costs it nothing, and branching on a guess about what the client is —
// a `User-Agent` sniff, an `Accept` header — would make the two clients travel different code
// paths through the operation that mints credentials, which is the last place that should happen.
func (s *Server) sessionCookie(joined membership.Joined) (http.Cookie, error) {
	now := s.cfg.Clock.Now()
	expires := now.Add(auth.DefaultSessionTTL)
	// The session's own id, and the only place one is minted. It is what `signOut` writes into
	// `session_revocation`, so a session that could not be given an id is a session nobody could
	// end — which is why the failure is returned rather than swallowed into an id-less cookie.
	id, err := s.cfg.IDs.New(now)
	if err != nil {
		return http.Cookie{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	value, err := s.cfg.Sessions.Encode(auth.Session{
		ID:           id.String(),
		MembershipID: joined.Membership.ID.String(),
		IssuedAt:     now,
		ExpiresAt:    expires,
		SteppedUpAt:  now,
	})
	if err != nil {
		return http.Cookie{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return *s.cfg.Sessions.Cookie(value, expires), nil
}
