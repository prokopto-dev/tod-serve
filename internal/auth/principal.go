package auth

import (
	"time"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// PrincipalKind says which credential authenticated a request. It is the same vocabulary
// `invite.minted_by_kind` uses, because "who minted this" and "who is asking" are the same question
// asked at two moments.
type PrincipalKind string

const (
	// KindSession is a browser session. Narrowed by step-up and by nothing else.
	KindSession PrincipalKind = "session"
	// KindPAT is a personal access token. Narrowed by its scopes, and it reaches no
	// capability-floor operation at any scope.
	KindPAT PrincipalKind = "pat"
)

// Principal is who is asking, resolved once per request before any handler runs.
//
// Every field is derived from live rows rather than from the credential: the role, the revocation
// state and the circle are read on EVERY request, so a revocation takes effect on the next request
// rather than when a token expires.
type Principal struct {
	// Kind says which credential authenticated the request.
	Kind PrincipalKind
	// MembershipID is the principal itself. Both credential kinds are bound to a membership
	// (ADR-0005), so there is exactly one principal kind in the authorization path.
	MembershipID core.MembershipID
	// CircleID is the circle that membership is in. It is the tenancy key every circle-scoped
	// request is checked against, and it comes from the membership row rather than from the URL.
	CircleID core.CircleID
	// Role is the membership's role, read live.
	Role authz.Role
	// DisplayName is the membership's display name, for audit and for `/me`.
	DisplayName string
	// Scopes are the token's scopes. Empty for a session, which is not narrowed by scopes.
	Scopes []authz.Scope
	// TokenID identifies the token, and is zero for a session.
	TokenID core.APITokenID
	// TokenPrefix is the token's eight-character public half — loggable, and how a leaked token is
	// traced. The secret never appears on this struct at all.
	TokenPrefix string
	// TokenExpiresAt is when the token stops working, or zero when it does not expire.
	TokenExpiresAt core.Micros
	// SteppedUpAt is when a session last proved its identity. Zero for a token, which can never
	// step up.
	SteppedUpAt core.Micros
}

// Effective returns what this principal may actually do: `role permissions ∩ token scopes` for a
// token, and the role's permissions for a session. The intersection lives in internal/authz and is
// not reimplemented here.
func (p Principal) Effective() authz.Set {
	if p.Kind == KindPAT {
		return authz.EffectiveForToken(p.Role, p.Scopes)
	}
	return authz.EffectiveForSession(p.Role)
}

// Can reports whether the principal holds the permission, after narrowing.
func (p Principal) Can(perm authz.Permission) bool { return p.Effective().Has(perm) }

// RoleHolds reports whether the principal's ROLE holds the permission, before any token narrowing.
//
// The difference between this and [Principal.Can] is the difference between `forbidden` and
// `insufficient_scope`, which point at completely different fixes: ask an officer for a role,
// versus mint a token with the scope.
func (p Principal) RoleHolds(perm authz.Permission) bool {
	return authz.RolePermissions(p.Role).Has(perm)
}

// SteppedUpWithin reports whether the principal proved its identity within the window ending at
// now. A token never has, at any scope: that is the capability floor.
func (p Principal) SteppedUpWithin(now core.Micros, window time.Duration) bool {
	if p.Kind != KindSession || p.SteppedUpAt.IsZero() {
		return false
	}
	return !p.SteppedUpAt.Add(window).Before(now)
}

// IsZero reports whether the principal is unset — an unauthenticated request.
func (p Principal) IsZero() bool { return p.MembershipID.IsZero() }
