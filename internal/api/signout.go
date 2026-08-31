package api

import (
	"context"
	"net/http"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// SignOutResponse is what ending a session tells the caller.
//
// `tokens_kept` is the point of it. Sign-out ends a browser session and touches no personal access
// token, and this is that promise stated to the person who just clicked the button rather than only
// to the test that asserts it: somebody signing out of the console on a shared machine can see that
// the plugin still logging their raid's ToDs is untouched. A raider's nParse+ destination going
// silent because they signed out of a website is the surprise this field exists to pre-empt.
type SignOutResponse struct {
	// TokensKept is how many live personal access tokens this membership still holds — neither
	// revoked nor expired at `as_of`. Sign-out never changes it.
	TokensKept int `json:"tokens_kept" doc:"Live personal access tokens this membership still holds. Signing out never revokes one"`
	// AsOf is the instant this answer was computed.
	AsOf core.Micros `json:"as_of"`
}

// signOutOutput clears the cookie in the same response that records the revocation.
//
// Both halves are needed and neither is sufficient. Clearing the cookie without the row would make
// sign-out a request the browser can decline to honour, and a copy of the cookie taken beforehand
// would still work; writing the row without clearing the cookie would leave the browser presenting
// a credential the server now refuses, so every subsequent request 401s instead of the console
// simply knowing it is signed out.
type signOutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      SignOutResponse
}

type signOutInput struct{}

// registerSignOut attaches the operation that ends the caller's own browser session.
//
// # This session, not every session for the identity
//
// Signing out ends THE SESSION THAT ASKED and no other. The id comes off the verified cookie, so
// there is no session id in the request at all: a caller cannot name a session, which is what stops
// this being a way to end somebody else's.
//
// The alternative — end every session this identity holds, anywhere — was rejected as the DEFAULT
// rather than as an idea. Sign-out is overwhelmingly "I am finished on this machine", and the
// identity here may hold sessions in several circles at once; signing somebody out of their phone
// because they closed a browser at work is a surprise, and a surprise attached to the one control
// that exists to make a shared machine safe is the worst place to put one. So the narrow act is the
// one the button performs.
//
// Sign-out-everywhere is therefore NOT offered here rather than offered behind a flag, and that is
// deliberate: a `?all=true` on the control everybody clicks is exactly the destructive thing that
// gets passed by accident. When it is wanted it wants to be its own explicit operation with its own
// confirmation, and the instance-wide remedy — rotating `TOD_SESSION_KEY` — still exists in the
// meantime. Sessions expire in [auth.DefaultSessionTTL], which is what keeps that gap bounded.
func (s *Server) registerSignOut() error {
	return registerFailure(OpSignOut, Register(s.api, OpSignOut,
		func(ctx context.Context, _ *signOutInput) (*signOutOutput, error) {
			p, ok := PrincipalFrom(ctx)
			if !ok {
				return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
			}
			if p.Kind != auth.KindSession || p.SessionID == "" {
				// Unreachable through the route: the registry declares it session-only, so the
				// middleware refuses a token before the handler runs. Checked anyway, because a
				// handler that quietly answered 200 while ending nothing is the failure a sign-out
				// button must never have.
				return nil, apierr.New(apierr.CodeSessionRequired,
					"only a browser session can be signed out")
			}

			now := s.cfg.Clock.Now()
			err := s.cfg.Store.Queries().RevokeSession(ctx, sqlitegen.RevokeSessionParams{
				SessionID: p.SessionID,
				// The revoked session's own expiry, so the sweep can take the row once the cookie
				// would have stopped working anyway.
				ExpiresAt: int64(p.SessionExpiryAt),
				CreatedAt: int64(now),
				UpdatedAt: int64(now),
			})
			if err != nil {
				// The cookie is NOT cleared on this path. A browser told it is signed out while
				// the server still accepts its cookie is the one outcome worse than an error.
				return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
			}

			kept, err := s.liveTokenCount(ctx, p.MembershipID.String(), now)
			if err != nil {
				return nil, err
			}

			s.cfg.Log.InfoContext(ctx, "session signed out",
				"membership_id", p.MembershipID.String(), "tokens_kept", kept)
			return &signOutOutput{
				SetCookie: *s.cfg.Sessions.ClearCookie(),
				Body:      SignOutResponse{TokensKept: kept, AsOf: now},
			}, nil
		}))
}

// liveTokenCount counts the membership's tokens that still authenticate at now.
//
// It reads the same two columns `authenticateToken` refuses on — `revoked_at` and `expires_at` —
// rather than a `COUNT(*)`, so "live" means here what it means at the edge.
func (s *Server) liveTokenCount(
	ctx context.Context, membershipID string, now core.Micros,
) (int, error) {
	rows, err := s.cfg.Store.Queries().ListAPITokensForMembership(ctx, membershipID)
	if err != nil {
		return 0, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	live := 0
	for _, row := range rows {
		if row.RevokedAt != nil {
			continue
		}
		if row.ExpiresAt != nil && !now.Before(core.Micros(*row.ExpiresAt)) {
			continue
		}
		live++
	}
	return live, nil
}
