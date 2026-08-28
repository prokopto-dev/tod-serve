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
	// IdentityID is the person behind a human membership, and is zero for a service membership —
	// a bot has no identity, it has an owner. It is the key an instance grant hangs off.
	IdentityID core.IdentityID
	// InstanceGrants are the instance-realm permissions this principal's IDENTITY currently holds,
	// read live from `instance_grant` on every request for the same reason the membership is: a
	// revoked grant takes effect on the next request rather than when a credential expires.
	//
	// It is empty for a token at every scope. See ADR-0012 and [authz.EffectiveForToken].
	InstanceGrants authz.Set
}

// Effective returns what this principal may actually do: `role permissions ∩ token scopes` for a
// token, and the role's permissions plus the identity's instance grants for a session. Both are
// computed in internal/authz and neither is reimplemented here.
func (p Principal) Effective() authz.Set {
	if p.Kind == KindPAT {
		return authz.EffectiveForToken(p.Role, p.Scopes)
	}
	return authz.EffectiveForSession(p.Role, p.InstanceGrants)
}

// Can reports whether the principal holds the permission, after narrowing.
func (p Principal) Can(perm authz.Permission) bool { return p.Effective().Has(perm) }

// Holds reports whether the principal was GRANTED the permission, before any token narrowing: by
// the membership's role for a circle-realm key, and by the identity's instance grants for an
// instance-realm one.
//
// The difference between this and [Principal.Can] is the difference between `forbidden` and
// `insufficient_scope`, which point at completely different fixes: ask an officer for a role,
// versus mint a token with the scope.
//
// The realm branch is here rather than in the caller because asking a role about an instance-realm
// key would answer false for a principal who holds the grant, and a 403 saying "your role does not
// hold it" is exactly the confidently wrong answer this project is built against.
//
// The instance side asks [authz.ExpandInstance] rather than the raw ledger set for the same
// reason. An identity granted `instance.owner` HAS been granted `instance.security.manage`, so a
// bare `Has` would answer no here while [Principal.Can] answered yes — and the two disagreeing is
// how `forbidden` and `insufficient_scope` end up pointing somebody at the wrong fix.
//
// A TOKEN holds no instance-realm permission, whatever this struct's `InstanceGrants` says. That
// is the same floor [authz.EffectiveForToken] states as a signature, and it is checked here on the
// credential KIND rather than left to the field being empty: the authenticator populates it only
// on the session path, so today the two agree — and a `Holds` resting on that would answer
// `insufficient_scope` the day they stopped, sending somebody to mint a scope no token can carry.
func (p Principal) Holds(perm authz.Permission) bool {
	if authz.IsInstanceRealm(perm) {
		return p.Kind != KindPAT && authz.ExpandInstance(p.InstanceGrants).Has(perm)
	}
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
