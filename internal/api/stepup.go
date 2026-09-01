package api

import (
	"context"
	"net/http"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/membership"
)

// StepUpResponse is what re-proving an identity tells the caller.
//
// `token_minted: false` is a constant, and it is here rather than left implicit. It is the whole
// point of the operation existing beside `/sessions`, and a field that says so is the difference
// between a promise and a thing somebody has to infer from an absent key. A client watching its
// own device list grow had no way to tell which of its own calls was doing it.
type StepUpResponse struct {
	// SteppedUpAt is the instant the identity was proved.
	SteppedUpAt core.Micros `json:"stepped_up_at"`
	// StepUp is every graded tier and when this session stops satisfying it, so a console can
	// redraw its "you can do this for another N minutes" without a second request.
	StepUp []StepUpTierView `json:"step_up"`
	// TokenMinted is always false. Re-proving a session is not a sign-in and mints no device.
	TokenMinted bool `json:"token_minted" doc:"Always false: re-proving a session mints no personal access token"`
	// AsOf is the instant this answer was computed.
	AsOf core.Micros `json:"as_of"`
}

type stepUpSessionInput struct {
	Body struct {
		// Provider is a provider key. There is deliberately no `circle_id`: the circle is the one
		// the caller's session is already bound to, and a route that let a caller name one would
		// be a second place tenancy is decided.
		Provider   string         `json:"provider" doc:"A provider key this circle accepts"`
		Credential CredentialBody `json:"credential"`
		// DisplayName is what a `local` provider asserts. It is used to verify and never to
		// rename: renaming is `member.manage`, which is a permission this operation does not ask
		// for and must not quietly exercise.
		DisplayName string `json:"display_name,omitempty" doc:"Required for local; used to verify, never to rename" maxLength:"64"`
	}
}

// stepUpSessionOutput re-issues the session cookie with a fresh proof on it.
//
// The cookie carries the SAME session id. That is what makes this a step-up rather than a second
// sign-in: `signOut` still names one session, a cookie copied before this call is the same session
// and is revoked with it, and nothing in `session_revocation` has to learn that one browser now
// holds two.
type stepUpSessionOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      StepUpResponse
}

// registerStepUp attaches the operation that re-proves an existing session.
//
// # Why this is not "sign in again"
//
// Signing in again worked, which is exactly why it was the remedy people found. It also minted a
// personal access token every time — `/sessions` hands one out, because a plugin with no browser
// needs one from somewhere — so an operator who re-authenticated every five minutes ended up with
// a device list nobody could read and a pile of live credentials nobody had asked for. ADR-0024.
//
// # The expiry is not extended, only the proof
//
// The re-issued cookie keeps the original `ExpiresAt`. A session lasts [auth.DefaultSessionTTL]
// from when it was created and stepping up does not renew that: if it did, a console left open and
// re-proved once an hour would never expire at all, and the bounded lifetime that makes a stateless
// session acceptable (see [auth.Session]) would be gone. Twelve hours after signing in you sign in
// again — that is the design, and this route does not quietly change it.
func (s *Server) registerStepUp() error {
	return registerFailure(OpStepUpSession, Register(s.api, OpStepUpSession,
		func(ctx context.Context, in *stepUpSessionInput) (*stepUpSessionOutput, error) {
			p, ok := PrincipalFrom(ctx)
			if !ok {
				return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
			}
			if p.Kind != auth.KindSession || p.SessionID == "" {
				// Unreachable through the route: it is session-only, so the middleware refuses a
				// token first. Checked anyway — a handler that answered 200 while stepping
				// nothing up would hand a caller a proof they do not have.
				return nil, apierr.New(apierr.CodeSessionRequired,
					"only a browser session can be stepped up")
			}

			stepped, err := s.cfg.Members.StepUp(ctx, membership.StepUpRequest{
				MembershipID: p.MembershipID,
				ProviderKey:  in.Body.Provider,
				Credential:   in.Body.Credential.toCredential(),
				DisplayName:  in.Body.DisplayName,
			})
			if err != nil {
				return nil, err
			}

			value, err := s.cfg.Sessions.Encode(auth.Session{
				ID:           p.SessionID,
				MembershipID: p.MembershipID.String(),
				IssuedAt:     p.SessionIssuedAt,
				ExpiresAt:    p.SessionExpiryAt,
				SteppedUpAt:  stepped.SteppedUpAt,
			})
			if err != nil {
				return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
			}

			s.cfg.Log.InfoContext(ctx, "session stepped up",
				"membership_id", p.MembershipID.String(), "provider", in.Body.Provider)

			// The principal is rebuilt with the new proof so the tiers reported here are the ones
			// the very next request will be judged by. Reading them off `p` would report the
			// state this call just replaced, which is the shape of "your action did not take".
			proved := p
			proved.SteppedUpAt = stepped.SteppedUpAt
			return &stepUpSessionOutput{
				SetCookie: *s.cfg.Sessions.Cookie(value, p.SessionExpiryAt),
				Body: StepUpResponse{
					SteppedUpAt: stepped.SteppedUpAt,
					StepUp:      stepUpTiers(proved, s.cfg.Auth.StepUpWindows(), stepped.AsOf),
					TokenMinted: false,
					AsOf:        stepped.AsOf,
				},
			}, nil
		}))
}
