package authz

import (
	"errors"
	"fmt"
	"slices"
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

// StepUpTier says how recently a session must have proved its identity before an operation
// carrying this permission is allowed.
//
// It exists because one boolean was answering two different questions. "No personal access token
// reaches this at any scope" is [PermissionDef.Floor]; "prove again that you are the person now
// typing" is this. They travelled together because every floor permission happened to be a write —
// and then `audit.read` joined the floor, which is a READ, and inherited a five-minute expiry
// designed for granting a role. An operator reported the result as being "half-authenticated":
// the console showed the Audit section, the page refused to load it, and the only exit anybody
// found was signing out. ADR-0024.
//
// The tier is a property of the PERMISSION rather than of the route, for the same reason the floor
// is: a second route reaching the same permission must not be able to answer a weaker question.
type StepUpTier string

const (
	// StepUpNone asks for no recency proof at all. The session still has to exist, and for a floor
	// permission no token reaches it — a tier of `none` narrows nothing about who may call.
	StepUpNone StepUpTier = "none"
	// StepUpRoutine is the window for an operation that changes circle state without changing who
	// holds a capability: renaming a circle, revoking an invite, editing a timer. A hijacked tab
	// reaching one of these is a nuisance and an audit row, not a takeover.
	StepUpRoutine StepUpTier = "routine"
	// StepUpSensitive is the window for an operation that changes WHO CAN DO WHAT, or that
	// destroys data nothing can rebuild: a role, a revocation, a minted credential, the identity
	// providers a circle accepts, the instance realm. This is the tier five minutes was chosen
	// for, and it keeps it.
	StepUpSensitive StepUpTier = "sensitive"
)

// String returns the tier name as the documents and the OpenAPI extension spell it.
func (t StepUpTier) String() string { return string(t) }

// StepUpTiers returns every tier, weakest first. The order is the ordering: a session satisfying
// a later tier satisfies every earlier one, which is what [StepUpTier.AtLeast] rests on.
func StepUpTiers() []StepUpTier { return []StepUpTier{StepUpNone, StepUpRoutine, StepUpSensitive} }

// ErrUnknownStepUpTier is returned for a tier name outside the catalogue.
var ErrUnknownStepUpTier = errors.New("unknown step-up tier")

// ParseStepUpTier validates a tier name. It is what keeps the canonical document's fenced block
// from naming a tier nothing implements.
func ParseStepUpTier(s string) (StepUpTier, error) {
	for _, t := range StepUpTiers() {
		if string(t) == s {
			return t, nil
		}
	}
	return "", fmt.Errorf("%q: %w", s, ErrUnknownStepUpTier)
}

// AtLeast reports whether t is at least as strict as other.
func (t StepUpTier) AtLeast(other StepUpTier) bool {
	return slices.Index(StepUpTiers(), t) >= slices.Index(StepUpTiers(), other)
}

// PermissionDef is one permission and everything generated from it.
type PermissionDef struct {
	// Key is the permission itself.
	Key Permission
	// Realm says whether a circle role can grant it.
	Realm Realm
	// Floor marks membership of the capability floor: no PAT reaches this permission at any
	// scope, whatever its role holds.
	Floor bool
	// StepUp is how recently the session must have proved its identity. It is meaningful only
	// on a floor permission — a permission a token can reach cannot ask a token to step up, and
	// TestStepUp_OutsideTheFloor_IsAlwaysNone is what stops one claiming to.
	StepUp StepUpTier
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
			PermissionCircleRead, RealmCircle, false, StepUpNone,
			"Read the circle's name, server, settings and revocation strength",
		},
		{
			PermissionCircleManage, RealmCircle, true, StepUpRoutine,
			"Rename the circle, change its settings, and set its timer overrides",
		},
		{
			PermissionCircleSecurityManage, RealmCircle, true, StepUpSensitive,
			"Change which identity providers the circle accepts, which changes its revocation strength",
		},
		{
			PermissionCircleDelete, RealmCircle, true, StepUpSensitive,
			"Delete the circle and every report in it",
		},

		{
			PermissionMemberRead, RealmCircle, false, StepUpNone,
			"List the circle's members and read one",
		},
		{
			PermissionMemberManage, RealmCircle, true, StepUpSensitive,
			"Change a member's role or display name",
		},
		{
			PermissionMemberRevoke, RealmCircle, true, StepUpSensitive,
			"Revoke a membership, and reinstate a revoked one",
		},

		{PermissionInviteRead, RealmCircle, false, StepUpNone, "List the circle's invites"},
		{
			PermissionInviteCreate, RealmCircle, false, StepUpNone,
			"Mint an invite code; one minted by a PAT is hard-narrowed to one use, 24 hours and a role below owner",
		},
		{
			PermissionInviteRevoke, RealmCircle, true, StepUpRoutine,
			"Revoke an invite before it expires",
		},

		{
			PermissionTodRead, RealmCircle, false, StepUpNone,
			"Read the board, the reports behind it, and the quake log",
		},
		{
			PermissionTodReadAttribution, RealmCircle, false, StepUpNone,
			"See which member reported a time of death; withholding this is what the observer role is",
		},
		{
			PermissionTodReport, RealmCircle, false, StepUpNone,
			"Append a time-of-death report",
		},
		{PermissionTodRetract, RealmCircle, false, StepUpNone, "Retract one's own report"},
		{
			PermissionTodRetractAny, RealmCircle, false, StepUpNone,
			"Retract another member's report",
		},
		{
			PermissionTodQuakeReport, RealmCircle, false, StepUpNone,
			"Record a server-wide earthquake; a false one wipes the whole board",
		},

		{
			PermissionCatalogueRead, RealmCircle, false, StepUpNone,
			"Read the raid-target catalogue and resolve a target name",
		},
		{
			PermissionCatalogueManage, RealmInstance, true, StepUpRoutine,
			"Add or change raid targets and their per-server timers, for every circle on the instance",
		},

		{
			// Floored and NOT stepped up, and the gap between those two is the whole of
			// ADR-0024. A bulk export of who did what is exactly what a leaked bot token must
			// not reach, so the floor keeps it; reading a circle's own audit log is not a
			// privilege escalation, so demanding a fresh identity proof for it bought nothing
			// and cost an operator the ability to look at all.
			PermissionAuditRead, RealmCircle, true, StepUpNone,
			"Read the circle's audit log",
		},
		{
			PermissionOpsRead, RealmInstance, false, StepUpNone,
			"Read instance diagnostics and job status",
		},

		{
			PermissionTokenMint, RealmCircle, true, StepUpSensitive,
			"Create a service membership and mint its token",
		},
		{
			PermissionTokenRevoke, RealmCircle, true, StepUpSensitive,
			"Revoke another principal's token",
		},

		{
			PermissionInstanceCircleCreate, RealmInstance, true, StepUpSensitive,
			"Create a circle on this instance",
		},
		{
			// The summary widened when `/admin/instance` landed, because this key had already
			// reached further than it said: whoever may add a `local` identity provider can
			// already let anybody in, so gating a policy switch behind it takes nothing new.
			// The alternative — `instance.owner` — would have been the wider grant, not the
			// narrower one: it expands to the whole instance realm (ADR-0015), so delegating one
			// switch would mean handing over the providers, the catalogue and the ops dashboard.
			PermissionInstanceSecurityManage, RealmInstance, true, StepUpSensitive,
			"Configure how this instance admits and authorises people: its identity providers, " +
				"and the instance-wide policy switches such as self-service circle creation",
		},
		{
			PermissionInstanceOwner, RealmInstance, true, StepUpSensitive,
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
// It is EXPANSION rather than a second matrix on purpose, and ADR-0015 is where that is argued:
// ADR-0012 rejected implication as the GRANTING MODEL — a role enum, or a boolean with the rest
// derived in storage — and this implies in one direction from one key, at the authorization
// boundary, with every narrower key still separately grantable. So granting `ops.read` for a
// dashboard still hands over nothing else, which is ADR-0012's consequence and is unchanged.
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

// CapabilityFloor returns the permissions that are session-only: operations that alter
// authentication, authorization or bulk-export state, which no token may reach at any scope.
//
// It is derived from [PermissionDef.Floor] rather than being a second list, and
// TestCapabilityFloor_MatchesCanonicalConventions parses the fenced block in canonical
// conventions §6 and compares in both directions, so neither this catalogue nor that document can
// drift away from the other.
//
// It used to be derived from the step-up flag, when those were one field. They are two now, and
// the floor is the wider of the two: `audit.read` is floored and asks for no step-up. See
// [StepUpTier].
func CapabilityFloor() []Permission {
	var floor []Permission
	for _, def := range Permissions() {
		if def.Floor {
			floor = append(floor, def.Key)
		}
	}
	return floor
}

// InFloor reports whether no personal access token reaches the permission at any scope.
func InFloor(p Permission) bool {
	def, ok := LookupPermission(p)
	return ok && def.Floor
}

// StepUpFor returns how recently a session must have proved its identity for the permission. An
// unknown key answers [StepUpSensitive] rather than [StepUpNone]: a permission this catalogue has
// never heard of is not one to wave through, and the caller is already going to be refused by
// [LookupPermission] elsewhere.
func StepUpFor(p Permission) StepUpTier {
	def, ok := LookupPermission(p)
	if !ok {
		return StepUpSensitive
	}
	return def.StepUp
}

// RequiresStepUp reports whether the permission asks for any recency proof at all.
//
// It is `StepUpFor(p) != StepUpNone` and not "is in the floor", which is what it meant when one
// boolean answered both questions. Callers that want the floor want [InFloor].
func RequiresStepUp(p Permission) bool { return StepUpFor(p) != StepUpNone }

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
