package authz

import (
	"fmt"

	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// Role is a membership role. It is ordered — `observer < member < officer < owner` — and the
// ordering is a rule, not a convention: "at least an officer" is a question the codebase asks.
type Role string

// The roles, weakest first. The values come from the enum catalogue so the string that reaches the
// wire, the CHECK constraint and this matrix is one string.
const (
	RoleObserver Role = schemaenum.MembershipRoleObserver
	RoleMember   Role = schemaenum.MembershipRoleMember
	RoleOfficer  Role = schemaenum.MembershipRoleOfficer
	RoleOwner    Role = schemaenum.MembershipRoleOwner
)

// Roles returns every role, weakest first.
//
// TestRoles_Order_MatchesSchemaEnum asserts this agrees with internal/schemaenum, which owns the
// ordering, so this can be a literal instead of a lookup with a not-found case it cannot reach.
func Roles() []Role { return []Role{RoleObserver, RoleMember, RoleOfficer, RoleOwner} }

// String returns the wire and database value.
func (r Role) String() string { return string(r) }

// Rank returns the role's position, weakest first, and whether it is a known role. The ranking
// itself lives in internal/schemaenum with every other ordered enum.
func (r Role) Rank() (int, bool) {
	e, ok := schemaenum.Lookup(schemaenum.NameMembershipRole)
	if !ok {
		return 0, false
	}
	return e.Rank(string(r))
}

// AtLeast reports whether r is at least as strong as other. An unknown role is never at least
// anything: a role that fell out of the enum must fail closed.
func (r Role) AtLeast(other Role) bool {
	mine, ok := r.Rank()
	if !ok {
		return false
	}
	theirs, ok := other.Rank()
	if !ok {
		return false
	}
	return mine >= theirs
}

// ParseRole validates a role read from the database or a request.
func ParseRole(s string) (Role, error) {
	role := Role(s)
	if _, ok := role.Rank(); !ok {
		return "", fmt.Errorf("parse role %q: %w", s, ErrUnknownRole)
	}
	return role, nil
}

// RolePermissions returns what a role grants within its circle.
//
// The matrix is cumulative: every role holds everything the role below it holds, and
// TestRolePermissions_EachRole_ContainsTheRoleBelow enforces that. A non-cumulative row would mean
// promoting someone could take a capability away, which is not a thing anybody would predict.
//
// Instance-realm permissions appear nowhere here — see the package comment and ADR-0012.
func RolePermissions(r Role) Set {
	observer := NewSet(
		PermissionCircleRead,
		PermissionTodRead,
		PermissionCatalogueRead,
	)
	// An observer is an allied guild reading your board. It sees the state, the window and the
	// evidence counts, and never who reported — that omission IS the role.
	member := observer.Union(NewSet(
		PermissionMemberRead,
		PermissionTodReadAttribution,
		PermissionTodReport,
		PermissionTodRetract,
	))
	officer := member.Union(NewSet(
		PermissionCircleManage,
		PermissionMemberManage,
		PermissionMemberRevoke,
		PermissionInviteRead,
		PermissionInviteCreate,
		PermissionInviteRevoke,
		PermissionTodRetractAny,
		// A false quake wipes the whole board, so it stops at the officers.
		PermissionTodQuakeReport,
		PermissionAuditRead,
	))
	// What an owner adds is exactly the set whose compromise costs more than a wrong ToD:
	// the circle's identity providers, its existence, and its tokens.
	owner := officer.Union(NewSet(
		PermissionCircleSecurityManage,
		PermissionCircleDelete,
		PermissionTokenMint,
		PermissionTokenRevoke,
	))

	switch r {
	case RoleObserver:
		return observer
	case RoleMember:
		return member
	case RoleOfficer:
		return officer
	case RoleOwner:
		return owner
	default:
		// An unrecognised role grants nothing. Failing open here would make a typo in a migration
		// into a privilege escalation.
		return Set{}
	}
}

// EffectiveForSession returns what a browser session may do: its role's permissions in its circle,
// plus the instance-realm permissions its IDENTITY has been granted (ADR-0012). A session is not
// narrowed by scopes; it is narrowed by step-up, which is a separate question asked per operation
// — see [RequiresStepUp].
//
// The instance set is a parameter rather than something this package reads, because it comes from
// a table and this package holds no store. It is a union rather than a second lookup at the call
// site so that "what may this principal do" has one answer computed in one place — the same reason
// [EffectiveForToken] owns the intersection.
func EffectiveForSession(r Role, instance Set) Set { return RolePermissions(r).Union(instance) }

// EffectiveForToken returns role permissions ∩ token scopes: the capability floor, stated as code.
//
// A token can only ever narrow what its membership's role already grants, so a leaked token is
// bounded by the person it belongs to. A token with no scopes may do nothing at all.
//
// It takes no instance set, and that is the point: an instance grant belongs to an identity and a
// token is bound to a membership, so a leaked token cannot reach one however the ledger reads.
// TestScopes_NoScopeGrants_AnInstanceRealmPermission keeps the intersection empty from the other
// side, so this stays true if a scope is ever widened.
func EffectiveForToken(r Role, scopes []Scope) Set {
	return RolePermissions(r).Intersect(GrantedByScopes(scopes))
}
