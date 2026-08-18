package api

import (
	"context"
	"errors"
	"slices"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// ServerMeta is what a client reads before it has any credential: what this instance is, what it
// speaks, and what it will let a stranger do.
type ServerMeta struct {
	// Name is the operator's name for the instance, empty on one that has not been set up.
	Name string `json:"name"`
	// Version is the running build.
	Version string `json:"version"`
	// APIVersions are the base paths this binary serves. Within a version the surface is additive
	// only, so a client that finds its version here can rely on the operations it knows.
	APIVersions []string `json:"api_versions"`
	// Configured says whether an instance row exists. A binary pointed at a fresh database answers
	// honestly rather than pretending to be an unnamed instance.
	Configured bool `json:"configured"`
	// SelfServiceCircleCreation says whether a caller may create a circle without an operator.
	SelfServiceCircleCreation bool `json:"self_service_circle_creation"`
	// AsOf is the instant this answer was computed. Every response carries one, and every
	// countdown anywhere in this API is a signed offset from it.
	AsOf core.Micros `json:"as_of"`
}

type serverMetaInput struct{}

type serverMetaOutput struct{ Body ServerMeta }

// registerMeta attaches the public discovery operation.
func (s *Server) registerMeta() error {
	return registerFailure(OpGetServerMeta, Register(s.api, OpGetServerMeta,
		func(ctx context.Context, _ *serverMetaInput) (*serverMetaOutput, error) {
			meta := ServerMeta{
				Version:     s.cfg.Version,
				APIVersions: []string{BasePath},
				AsOf:        s.cfg.Clock.Now(),
			}
			row, err := s.cfg.Store.Queries().GetInstance(ctx)
			switch {
			case errors.Is(err, store.ErrNoRows):
				// Not an error. A binary pointed at a fresh database is a real state an operator
				// meets during setup, and answering 500 would make the first thing they see look
				// like a broken build.
				return &serverMetaOutput{Body: meta}, nil
			case err != nil:
				return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
			}
			meta.Name = row.Name
			meta.Configured = true
			meta.SelfServiceCircleCreation = row.SelfServiceCircleCreation == 1
			return &serverMetaOutput{Body: meta}, nil
		}))
}

// PrincipalView is what `/me` answers: who the caller is, and exactly what this credential may do.
//
// `permissions` is the EFFECTIVE set — `role permissions ∩ token scopes` — rather than the role's,
// because the question a client is asking is "what can I do with what I am holding", and answering
// with the role's set would make a scoped token look more capable than it is.
type PrincipalView struct {
	// Kind is `session` or `pat`.
	Kind string `json:"kind"`
	// MembershipID is the principal. Both credential kinds are bound to one.
	MembershipID core.MembershipID `json:"membership_id"`
	// CircleID is the circle that membership is in.
	CircleID core.CircleID `json:"circle_id"`
	// Role is the membership's role.
	Role string `json:"role"`
	// DisplayName is what the circle calls this member.
	DisplayName string `json:"display_name"`
	// Permissions are what this credential may actually do, after narrowing.
	Permissions []string `json:"permissions"`
	// Scopes are the token's scopes, empty for a session.
	Scopes []string `json:"scopes"`
	// TokenPrefix is the token's eight-character public half, empty for a session. It is safe to
	// show and to quote: it is how a leaked token is traced, and the secret half is not here.
	TokenPrefix string `json:"token_prefix"`
	// TokenExpiresAt is when the token stops working, null when it does not expire.
	TokenExpiresAt *core.Micros `json:"token_expires_at"`
	// SteppedUp says whether this session has re-authenticated recently enough for a
	// capability-floor operation. Always false for a token, which never reaches one.
	SteppedUp bool `json:"stepped_up"`
	// StepUpWindowSeconds is how recently a session must have proved its identity.
	StepUpWindowSeconds int `json:"step_up_window_seconds"`
	// AsOf is the instant this answer was computed.
	AsOf core.Micros `json:"as_of"`
}

type currentPrincipalInput struct{}

type currentPrincipalOutput struct{ Body PrincipalView }

// registerPrincipal attaches `/me`.
func (s *Server) registerPrincipal() error {
	return registerFailure(OpGetCurrentPrincipal, Register(s.api, OpGetCurrentPrincipal,
		func(ctx context.Context, _ *currentPrincipalInput) (*currentPrincipalOutput, error) {
			p, ok := PrincipalFrom(ctx)
			if !ok {
				// Unreachable: the route is authenticated, so the middleware refused the request
				// before this handler ran. It is checked anyway, because "unreachable" is a claim
				// about the middleware that this handler cannot verify.
				return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
			}
			now := s.cfg.Clock.Now()
			view := PrincipalView{
				Kind:                string(p.Kind),
				MembershipID:        p.MembershipID,
				CircleID:            p.CircleID,
				Role:                string(p.Role),
				DisplayName:         p.DisplayName,
				Permissions:         permissionNames(p),
				Scopes:              scopeNames(p),
				TokenPrefix:         p.TokenPrefix,
				SteppedUp:           p.SteppedUpWithin(now, s.cfg.Auth.StepUpWindow()),
				StepUpWindowSeconds: int(s.cfg.Auth.StepUpWindow().Seconds()),
				AsOf:                now,
			}
			if !p.TokenExpiresAt.IsZero() {
				expires := p.TokenExpiresAt
				view.TokenExpiresAt = &expires
			}
			return &currentPrincipalOutput{Body: view}, nil
		}))
}

func permissionNames(p auth.Principal) []string {
	perms := p.Effective().Slice()
	out := make([]string, 0, len(perms))
	for _, perm := range perms {
		out = append(out, string(perm))
	}
	return out
}

func scopeNames(p auth.Principal) []string {
	out := make([]string, 0, len(p.Scopes))
	for _, s := range p.Scopes {
		out = append(out, string(s))
	}
	slices.Sort(out)
	return out
}

// TokenView is one of the caller's own devices. The secret is not here and never was: the database
// holds `HMAC-SHA256(pepper, secret)` and the prefix, and the prefix is the whole identity a person
// needs to recognise a device.
type TokenView struct {
	ID          core.APITokenID `json:"id"`
	Name        string          `json:"name"`
	TokenPrefix string          `json:"token_prefix"`
	Scopes      []string        `json:"scopes"`
	CreatedAt   core.Micros     `json:"created_at"`
	LastUsedAt  *core.Micros    `json:"last_used_at"`
	ExpiresAt   *core.Micros    `json:"expires_at"`
	RevokedAt   *core.Micros    `json:"revoked_at"`
}

type listMyTokensInput struct {
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor"`
	Limit  int    `query:"limit" doc:"Page size, 1-200" minimum:"0" maximum:"200"`
}

type listMyTokensOutput struct{ Body Page[TokenView] }

type revokeTokenInput struct {
	TokenID string `path:"token_id" doc:"The token to revoke. Must be one of your own"`
}

type revokeTokenOutput struct{ Body TokenView }

// registerTokens attaches the two operations a person uses to see and cut off their own devices.
func (s *Server) registerTokens() error {
	return errors.Join(
		registerFailure(OpListMyTokens, Register(s.api, OpListMyTokens,
			func(ctx context.Context, in *listMyTokensInput) (*listMyTokensOutput, error) {
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				limit, err := NormaliseLimit(in.Limit)
				if err != nil {
					return nil, err
				}
				after, err := ParseCursor(in.Cursor)
				if err != nil {
					return nil, err
				}
				rows, err := s.cfg.Store.Queries().
					ListAPITokensForMembership(ctx, p.MembershipID.String())
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}

				// The query returns newest first, so the cursor walks DOWN the ULID order. Paging
				// in Go is honest here: a person has a handful of devices, and a second query
				// shape would be a second thing to keep in step with this one.
				views := make([]TokenView, 0, len(rows))
				for _, row := range rows {
					view, convErr := tokenView(row)
					if convErr != nil {
						return nil, apierr.Wrap(apierr.CodeInternalError, convErr, "")
					}
					if !after.IsZero() && view.ID.ULID().Compare(after) >= 0 {
						continue
					}
					views = append(views, view)
				}
				hasMore := len(views) > limit
				if hasMore {
					views = views[:limit]
				}
				next := ""
				if len(views) > 0 {
					next = views[len(views)-1].ID.String()
				}
				return &listMyTokensOutput{Body: NewPage(views, next, hasMore)}, nil
			})),

		registerFailure(OpRevokeToken, Register(s.api, OpRevokeToken,
			func(ctx context.Context, in *revokeTokenInput) (*revokeTokenOutput, error) {
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				now := s.cfg.Clock.Now()
				row, err := s.cfg.Store.Queries().RevokeAPIToken(ctx, sqlitegen.RevokeAPITokenParams{
					RevokedAt:             ptrInt64(int64(now)),
					RevokedByMembershipID: ptrString(p.MembershipID.String()),
					UpdatedAt:             int64(now),
					ID:                    in.TokenID,
					MembershipID:          p.MembershipID.String(),
				})
				if errors.Is(err, store.ErrNoRows) {
					// Somebody else's token, an id that does not exist, and one already revoked all
					// answer the same way. Anything narrower would let a caller enumerate other
					// people's tokens by watching which id changed the answer.
					return nil, apierr.New(apierr.CodeNotFound, "no such token")
				}
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				view, err := tokenView(row)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				s.cfg.Log.InfoContext(ctx, "token revoked",
					"token_prefix", view.TokenPrefix,
					"membership_id", p.MembershipID.String())
				return &revokeTokenOutput{Body: view}, nil
			})),
	)
}

func tokenView(row sqlitegen.ApiToken) (TokenView, error) {
	id, err := core.ParseID[core.APIToken](row.ID)
	if err != nil {
		return TokenView{}, err
	}
	scopes, err := auth.ParseScopesJSON(row.ScopesJson)
	if err != nil {
		return TokenView{}, err
	}
	names := make([]string, 0, len(scopes))
	for _, s := range scopes {
		names = append(names, string(s))
	}
	return TokenView{
		ID:          id,
		Name:        row.Name,
		TokenPrefix: row.TokenPrefix,
		Scopes:      names,
		CreatedAt:   core.Micros(row.CreatedAt),
		LastUsedAt:  micros(row.LastUsedAt),
		ExpiresAt:   micros(row.ExpiresAt),
		RevokedAt:   micros(row.RevokedAt),
	}, nil
}

func micros(v *int64) *core.Micros {
	if v == nil {
		return nil
	}
	m := core.Micros(*v)
	return &m
}

func ptrString(v string) *string { return &v }
