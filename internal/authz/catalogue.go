package authz

import (
	"errors"
	"fmt"
)

// Permission is a `<resource>.<action>` key. It narrows a role.
type Permission string

// Scope is a `<family>:<verb>` PAT scope. It narrows a token, and is deliberately coarser than a
// permission: a scope says what a device is for, a permission says what a person may do.
type Scope string

// The permission keys, exactly as canonical conventions §6 lists them.
// TestPermissions_Catalogue_MatchesCanonicalConventions parses that document and compares in both
// directions.
const (
	PermissionCircleRead           Permission = "circle.read"
	PermissionCircleManage         Permission = "circle.manage"
	PermissionCircleSecurityManage Permission = "circle.security.manage"
	PermissionCircleDelete         Permission = "circle.delete"

	PermissionMemberRead   Permission = "member.read"
	PermissionMemberManage Permission = "member.manage"
	PermissionMemberRevoke Permission = "member.revoke"

	PermissionInviteRead   Permission = "invite.read"
	PermissionInviteCreate Permission = "invite.create"
	PermissionInviteRevoke Permission = "invite.revoke"

	PermissionTodRead            Permission = "tod.read"
	PermissionTodReadAttribution Permission = "tod.read.attribution"
	PermissionTodReport          Permission = "tod.report"
	PermissionTodRetract         Permission = "tod.retract"
	PermissionTodRetractAny      Permission = "tod.retract.any"
	PermissionTodQuakeReport     Permission = "tod.quake.report"

	PermissionCatalogueRead   Permission = "catalogue.read"
	PermissionCatalogueManage Permission = "catalogue.manage"

	PermissionAuditRead Permission = "audit.read"
	PermissionOpsRead   Permission = "ops.read"

	PermissionTokenMint   Permission = "token.mint"
	PermissionTokenRevoke Permission = "token.revoke"

	PermissionInstanceCircleCreate   Permission = "instance.circle.create"
	PermissionInstanceSecurityManage Permission = "instance.security.manage"
	PermissionInstanceOwner          Permission = "instance.owner"
)

// The PAT scopes, exactly as canonical conventions §6 lists them.
const (
	ScopeTodRead    Scope = "tod:read"
	ScopeTodReport  Scope = "tod:report"
	ScopeTodRetract Scope = "tod:retract"

	ScopeCircleRead Scope = "circle:read"
	ScopeMemberRead Scope = "member:read"

	ScopeInviteRead   Scope = "invite:read"
	ScopeInviteCreate Scope = "invite:create"

	ScopeCatalogueRead Scope = "catalogue:read"

	ScopeEventsSubscribe Scope = "events:subscribe"
)

// Realm says what grants a permission.
type Realm string

const (
	// RealmCircle is granted by a membership's role within one circle.
	RealmCircle Realm = "circle"
	// RealmInstance is granted at the instance level and is not reachable from any circle role.
	RealmInstance Realm = "instance"
)

var (
	// ErrUnknownPermission is returned for a key outside the catalogue.
	ErrUnknownPermission = errors.New("unknown permission")
	// ErrUnknownScope is returned for a scope outside the catalogue.
	ErrUnknownScope = errors.New("unknown scope")
	// ErrUnknownRole is returned for a role outside the membership role enum.
	ErrUnknownRole = errors.New("unknown role")
)

// PermissionDef is one permission and everything generated from it.
type PermissionDef struct {
	// Key is the permission itself.
	Key Permission
	// Realm says whether a circle role can grant it.
	Realm Realm
	// StepUp marks membership of the capability floor: session-only, re-authenticated, and
	// reachable by no PAT scope at all.
	StepUp bool
	// Summary is one line, and appears in the generated seed and documentation page.
	Summary string
}

// ScopeDef is one PAT scope and the permissions it can reach.
type ScopeDef struct {
	// Key is the scope itself.
	Key Scope
	// Grants are the permissions this scope makes reachable. The role still has to hold them.
	Grants []Permission
	// Summary is one line, and appears in the generated documentation page.
	Summary string
}

// Permissions returns the catalogue, in the order canonical conventions §6 lists it.
//
// It is a function, not a package-level slice, because a slice is mutable and the authorization
// catalogue is the last thing in this codebase that should be modifiable from a distance.
func Permissions() []PermissionDef {
	return []PermissionDef{
		{
			PermissionCircleRead, RealmCircle, false,
			"Read the circle's name, server, settings and revocation strength",
		},
		{
			PermissionCircleManage, RealmCircle, true,
			"Rename the circle, change its settings, and set its timer overrides",
		},
		{
			PermissionCircleSecurityManage, RealmCircle, true,
			"Change which identity providers the circle accepts, which changes its revocation strength",
		},
		{
			PermissionCircleDelete, RealmCircle, true,
			"Delete the circle and every report in it",
		},

		{PermissionMemberRead, RealmCircle, false, "List the circle's members and read one"},
		{PermissionMemberManage, RealmCircle, true, "Change a member's role or display name"},
		{
			PermissionMemberRevoke, RealmCircle, true,
			"Revoke a membership, and reinstate a revoked one",
		},

		{PermissionInviteRead, RealmCircle, false, "List the circle's invites"},
		{
			PermissionInviteCreate, RealmCircle, false,
			"Mint an invite code; one minted by a PAT is hard-narrowed to one use, 24 hours and a role below owner",
		},
		{PermissionInviteRevoke, RealmCircle, true, "Revoke an invite before it expires"},

		{
			PermissionTodRead, RealmCircle, false,
			"Read the board, the reports behind it, and the quake log",
		},
		{
			PermissionTodReadAttribution, RealmCircle, false,
			"See which member reported a time of death; withholding this is what the observer role is",
		},
		{PermissionTodReport, RealmCircle, false, "Append a time-of-death report"},
		{PermissionTodRetract, RealmCircle, false, "Retract one's own report"},
		{PermissionTodRetractAny, RealmCircle, false, "Retract another member's report"},
		{
			PermissionTodQuakeReport, RealmCircle, false,
			"Record a server-wide earthquake; a false one wipes the whole board",
		},

		{
			PermissionCatalogueRead, RealmCircle, false,
			"Read the raid-target catalogue and resolve a target name",
		},
		{
			PermissionCatalogueManage, RealmInstance, true,
			"Add or change raid targets and their per-server timers, for every circle on the instance",
		},

		{PermissionAuditRead, RealmCircle, true, "Read the circle's audit log"},
		{PermissionOpsRead, RealmInstance, false, "Read instance diagnostics and job status"},

		{
			PermissionTokenMint, RealmCircle, true,
			"Create a service membership and mint its token",
		},
		{PermissionTokenRevoke, RealmCircle, true, "Revoke another principal's token"},

		{PermissionInstanceCircleCreate, RealmInstance, true, "Create a circle on this instance"},
		{
			PermissionInstanceSecurityManage, RealmInstance, true,
			"Add, change or remove the instance's identity providers",
		},
		{
			PermissionInstanceOwner, RealmInstance, true,
			"Instance ownership: whatever an instance administrator can do that has no narrower key",
		},
	}
}

// Scopes returns the PAT scope catalogue, in the order canonical conventions §6 lists it.
func Scopes() []ScopeDef {
	return []ScopeDef{
		{
			Key:     ScopeTodRead,
			Grants:  []Permission{PermissionTodRead, PermissionTodReadAttribution},
			Summary: "Read the board and the reports behind it",
		},
		{
			Key: ScopeTodReport, Grants: []Permission{PermissionTodReport},
			Summary: "Append time-of-death reports",
		},
		{
			Key:     ScopeTodRetract,
			Grants:  []Permission{PermissionTodRetract, PermissionTodRetractAny},
			Summary: "Retract reports",
		},
		{
			Key: ScopeCircleRead, Grants: []Permission{PermissionCircleRead},
			Summary: "Read the circle",
		},
		{
			Key: ScopeMemberRead, Grants: []Permission{PermissionMemberRead},
			Summary: "Read the member list",
		},
		{
			Key: ScopeInviteRead, Grants: []Permission{PermissionInviteRead},
			Summary: "Read the invite list",
		},
		{
			Key: ScopeInviteCreate, Grants: []Permission{PermissionInviteCreate},
			Summary: "Mint an invite, hard-narrowed to one use, 24 hours and a role below owner",
		},
		{
			Key: ScopeCatalogueRead, Grants: []Permission{PermissionCatalogueRead},
			Summary: "Read the raid-target catalogue",
		},
		{
			Key: ScopeEventsSubscribe,
			// The SSE stream carries the same rows a read returns, so a token that may subscribe
			// can already see them. Granting anything narrower here would be a fiction: the route
			// declares `tod.read`, and the difference between the two scopes is which transport a
			// device is trusted with, not which rows it sees.
			Grants:  []Permission{PermissionTodRead},
			Summary: "Subscribe to the circle's event stream and replay it",
		},
	}
}

// InstancePermissions returns every permission granted at the instance level, in catalogue order.
//
// It is the value set of `instance_grant.permission`: a grant naming a circle-realm key would be a
// grant nothing could ever consult, because a circle permission comes from a membership's role.
// [InstancePermissionEnum] is how that becomes a CHECK constraint rather than a convention.
func InstancePermissions() []Permission {
	var out []Permission
	for _, def := range Permissions() {
		if def.Realm == RealmInstance {
			out = append(out, def.Key)
		}
	}
	return out
}

// Implies returns what holding p grants BEYOND p itself, and is empty for every key but one.
//
// `instance.owner` expands to the whole instance realm. Its catalogue entry has always described
// it as "whatever an instance administrator can do that has no narrower key", and until this
// function existed that description was a fiction: [EffectiveForSession] was a plain union, no
// route requires `instance.owner`, and so the grant the deployment runbook told an operator to
// make handed them nothing. The expansion is what makes the description true.
//
// It is EXPANSION rather than a second matrix on purpose. ADR-0012 rejected an instance role enum
// because it would grant by implication across unrelated keys; this implies in one direction from
// one key, and every narrower key stays separately grantable, so granting `ops.read` for a
// dashboard still hands over nothing else.
//
// The expansion is derived from [Realm] rather than listed, so an instance-realm permission added
// to the catalogue is one an instance owner holds without anybody remembering to append to
// anything. TestPermissions_EveryPermission_IsRequiredByARouteOrExpandsToOnesThatAre is what stops
// this becoming a key that expands into nothing again.
//
// It says nothing about tokens, and must not: [EffectiveForToken] takes no instance set, so a
// leaked PAT reaches nothing here however this reads. That is the ADR-0012 capability floor, and
// TestEffectiveForSession_TheExpansion_NeverReachesAToken asserts it from this side.
func Implies(p Permission) Set {
	if p != PermissionInstanceOwner {
		return Set{}
	}
	var out []Permission
	for _, key := range InstancePermissions() {
		if key != PermissionInstanceOwner {
			out = append(out, key)
		}
	}
	return NewSet(out...)
}

// ExpandInstance returns the instance-realm permissions an identity effectively holds, given the
// ones `instance_grant` records for it.
//
// One pass, not a fixed point: [Implies] is non-empty for exactly one key and expands to keys that
// imply nothing, and TestImplies_TheExpansion_IsOnePassDeep is what keeps that true. A transitive
// closure here would be machinery for a graph this catalogue does not have.
func ExpandInstance(granted Set) Set {
	out := granted
	for _, p := range granted.Slice() {
		out = out.Union(Implies(p))
	}
	return out
}

// IsInstanceRealm reports whether the permission is granted at the instance level rather than by a
// circle membership's role. An unknown key is not: a permission that fell out of the catalogue must
// fail closed in both directions.
func IsInstanceRealm(p Permission) bool {
	def, ok := LookupPermission(p)
	return ok && def.Realm == RealmInstance
}

// LookupPermission returns the definition of key.
func LookupPermission(key Permission) (PermissionDef, bool) {
	for _, def := range Permissions() {
		if def.Key == key {
			return def, true
		}
	}
	return PermissionDef{}, false
}

// LookupScope returns the definition of key.
func LookupScope(key Scope) (ScopeDef, bool) {
	for _, def := range Scopes() {
		if def.Key == key {
			return def, true
		}
	}
	return ScopeDef{}, false
}

// ParsePermission validates a permission key read from the database or a request.
func ParsePermission(s string) (Permission, error) {
	if _, ok := LookupPermission(Permission(s)); !ok {
		return "", fmt.Errorf("parse permission %q: %w", s, ErrUnknownPermission)
	}
	return Permission(s), nil
}

// ParseScope validates a scope read from a token record or a mint request.
func ParseScope(s string) (Scope, error) {
	if _, ok := LookupScope(Scope(s)); !ok {
		return "", fmt.Errorf("parse scope %q: %w", s, ErrUnknownScope)
	}
	return Scope(s), nil
}

// String returns the permission key.
func (p Permission) String() string { return string(p) }

// String returns the scope key.
func (s Scope) String() string { return string(s) }

// CapabilityFloor returns the permissions that are session-and-step-up only: operations that alter
// authentication, authorization or bulk-export state, which no token may reach at any scope.
//
// It is derived from [PermissionDef.StepUp] rather than being a second list, and
// TestCapabilityFloor_MatchesCanonicalConventions parses the fenced block in canonical
// conventions §6 and compares in both directions, so neither this catalogue nor that document can
// drift away from the other.
func CapabilityFloor() []Permission {
	var floor []Permission
	for _, def := range Permissions() {
		if def.StepUp {
			floor = append(floor, def.Key)
		}
	}
	return floor
}

// RequiresStepUp reports whether the permission is in the capability floor.
func RequiresStepUp(p Permission) bool {
	def, ok := LookupPermission(p)
	return ok && def.StepUp
}

// GrantedByScopes returns every permission the given scopes make reachable. A permission is only
// effective if the role holds it too — see [EffectiveForToken].
func GrantedByScopes(scopes []Scope) Set {
	var granted []Permission
	for _, s := range scopes {
		if def, ok := LookupScope(s); ok {
			granted = append(granted, def.Grants...)
		}
	}
	return NewSet(granted...)
}
