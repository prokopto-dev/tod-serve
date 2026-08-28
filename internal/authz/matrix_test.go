package authz_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// The roles here and the ordering in internal/schemaenum are two statements of one rule.
func TestRoles_Order_MatchesSchemaEnum(t *testing.T) {
	t.Parallel()
	enum, ok := schemaenum.Lookup(schemaenum.NameMembershipRole)
	require.True(t, ok)
	require.Len(t, authz.Roles(), len(enum.Values))

	for want, role := range authz.Roles() {
		rank, ok := role.Rank()
		require.True(t, ok, "%q is not in the membership role enum", role)
		require.Equal(t, want, rank, "authz.Roles is not weakest-first for %q", role)
	}
	for _, v := range enum.Values {
		require.Contains(t, authz.Roles(), authz.Role(v))
	}
}

func TestRole_AtLeast_FollowsTheOrdering(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		role  authz.Role
		floor authz.Role
		want  bool
	}{
		{"owner is at least an officer", authz.RoleOwner, authz.RoleOfficer, true},
		{"officer is at least an officer", authz.RoleOfficer, authz.RoleOfficer, true},
		{"member is not an officer", authz.RoleMember, authz.RoleOfficer, false},
		{"observer is not a member", authz.RoleObserver, authz.RoleMember, false},
		// A role that fell out of the enum — a bad migration, a typo — must fail closed.
		{"an unknown role is never enough", authz.Role("admin"), authz.RoleObserver, false},
		{"nothing is enough for an unknown role", authz.RoleOwner, authz.Role("admin"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.role.AtLeast(tc.floor))
		})
	}
}

func TestParseRole_Value_IsAcceptedOrRefused(t *testing.T) {
	t.Parallel()
	got, err := authz.ParseRole("officer")
	require.NoError(t, err)
	require.Equal(t, authz.RoleOfficer, got)

	_, err = authz.ParseRole("admin")
	require.ErrorIs(t, err, authz.ErrUnknownRole)
}

// Promoting somebody must never take a capability away, which is what a non-cumulative row would
// do and what nobody would predict.
func TestRolePermissions_EachRole_ContainsTheRoleBelow(t *testing.T) {
	t.Parallel()
	roles := authz.Roles()
	for i := 1; i < len(roles); i++ {
		lower, higher := roles[i-1], roles[i]
		for _, p := range authz.RolePermissions(lower).Slice() {
			require.True(t, authz.RolePermissions(higher).Has(p),
				"%q holds %q and %q does not", lower, p, higher)
		}
		require.Greater(t, authz.RolePermissions(higher).Len(),
			authz.RolePermissions(lower).Len(),
			"%q adds nothing to %q, so the two roles are the same role", higher, lower)
	}
}

// The separation of `tod.read.attribution` from `tod.read` IS the observer role: a board can be
// shared with an allied guild without handing over the identity of your trackers.
func TestRolePermissions_Observer_SeesTheBoardAndNotTheReporters(t *testing.T) {
	t.Parallel()
	observer := authz.RolePermissions(authz.RoleObserver)

	require.True(t, observer.Has(authz.PermissionTodRead))
	require.True(t, observer.Has(authz.PermissionCatalogueRead))
	require.False(t, observer.Has(authz.PermissionTodReadAttribution))
	require.False(t, observer.Has(authz.PermissionMemberRead))
	require.False(t, observer.Has(authz.PermissionTodReport))

	// The member above it is exactly where attribution starts.
	require.True(t, authz.RolePermissions(authz.RoleMember).Has(authz.PermissionTodReadAttribution))
}

func TestRolePermissions_UnknownRole_GrantsNothing(t *testing.T) {
	t.Parallel()
	// Failing open here would turn a typo in a migration into a privilege escalation.
	require.Equal(t, 0, authz.RolePermissions(authz.Role("admin")).Len())
	require.Equal(t, 0, authz.RolePermissions("").Len())
}

func TestEffectiveForToken_RolePermissionsIntersectTokenScopes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		role   authz.Role
		scopes []authz.Scope
		want   authz.Set
	}{
		{
			name:   "a plugin token: report and read",
			role:   authz.RoleMember,
			scopes: []authz.Scope{authz.ScopeTodRead, authz.ScopeTodReport},
			want: authz.NewSet(authz.PermissionTodRead, authz.PermissionTodReadAttribution,
				authz.PermissionTodReport),
		},
		{
			name: "an observer's token cannot see attribution its role lacks",
			role: authz.RoleObserver,
			// The scope grants attribution; the role does not, and the intersection is the rule.
			scopes: []authz.Scope{authz.ScopeTodRead},
			want:   authz.NewSet(authz.PermissionTodRead),
		},
		{
			name:   "a bot token minting invites",
			role:   authz.RoleOfficer,
			scopes: []authz.Scope{authz.ScopeInviteCreate},
			want:   authz.NewSet(authz.PermissionInviteCreate),
		},
		{
			name:   "an officer's token cannot revoke invites at any scope",
			role:   authz.RoleOfficer,
			scopes: []authz.Scope{authz.ScopeInviteRead, authz.ScopeInviteCreate},
			want:   authz.NewSet(authz.PermissionInviteRead, authz.PermissionInviteCreate),
		},
		{
			name:   "an event stream carries what a read carries",
			role:   authz.RoleMember,
			scopes: []authz.Scope{authz.ScopeEventsSubscribe},
			want:   authz.NewSet(authz.PermissionTodRead),
		},
		{
			name:   "a token with no scopes may do nothing",
			role:   authz.RoleOwner,
			scopes: nil,
			want:   authz.NewSet(),
		},
		{
			name:   "an unknown scope grants nothing rather than everything",
			role:   authz.RoleOwner,
			scopes: []authz.Scope{authz.Scope("admin:*")},
			want:   authz.NewSet(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := authz.EffectiveForToken(tc.role, tc.scopes)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("effective capability (-want +got):\n%s", diff)
			}
		})
	}
}

// A token narrows; it never widens. This is the property behind every row of the table above.
func TestEffectiveForToken_EveryRoleAndScope_NeverExceedsEither(t *testing.T) {
	t.Parallel()
	every := make([]authz.Scope, 0, len(authz.Scopes()))
	for _, def := range authz.Scopes() {
		every = append(every, def.Key)
	}

	for _, role := range authz.Roles() {
		for _, p := range authz.EffectiveForToken(role, every).Slice() {
			require.True(t, authz.RolePermissions(role).Has(p),
				"a %q token reaches %q, which the role does not hold", role, p)
			require.NotEmpty(t, authz.ScopesFor(p),
				"a token reaches %q, which no scope grants", p)
		}
	}
}

func TestEffectiveForSession_NoInstanceGrants_IsTheWholeRoleAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, role := range authz.Roles() {
		got := authz.EffectiveForSession(role, authz.Set{})
		if diff := cmp.Diff(authz.RolePermissions(role), got); diff != "" {
			t.Errorf("session capability for %q (-role +session):\n%s", role, diff)
		}
	}
	// A session is not narrowed by scopes; it is narrowed by step-up, asked per operation.
	owner := authz.EffectiveForSession(authz.RoleOwner, authz.Set{})
	require.True(t, owner.Has(authz.PermissionTokenMint))
	require.True(t, authz.RequiresStepUp(authz.PermissionTokenMint))

	// And an owner with no instance grant reaches no instance-realm permission at all. This is the
	// hole ADR-0012 closes, from the side that must STAY closed: the grant is what opens it, and a
	// role never does.
	for _, p := range authz.InstancePermissions() {
		require.False(t, owner.Has(p), "an owner with no instance grant reaches %q", p)
	}
}

// The instance set widens a session and nothing else does. The failure this catches is a union
// that quietly reached into the role matrix, which would make a grant of `ops.read` hand over
// whatever the granted identity's circle role also happened to imply.
func TestEffectiveForSession_AnInstanceGrant_AddsExactlyThatPermission(t *testing.T) {
	t.Parallel()
	grants := authz.NewSet(authz.PermissionCatalogueManage)
	for _, role := range authz.Roles() {
		got := authz.EffectiveForSession(role, grants)
		want := authz.RolePermissions(role).Union(grants)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("session capability for %q with a grant (-want +got):\n%s", role, diff)
		}
		require.True(t, got.Has(authz.PermissionCatalogueManage))
		// The other four instance keys were not granted, so they are not held. A ledger is not a
		// role: every narrower key stays separately grantable, and `instance.owner` is the one
		// key that implies any of the others — see the expansion tests below.
		for _, p := range authz.InstancePermissions() {
			if p == authz.PermissionCatalogueManage {
				continue
			}
			require.False(t, got.Has(p), "granting catalogue.manage also handed over %q", p)
		}
	}
}

// A token is bound to a membership and an instance grant belongs to an identity, so a leaked token
// reaches no instance-realm permission however the ledger reads. This is the arithmetic behind
// that claim, checked over every role and EVERY scope at once rather than over a chosen pair.
func TestEffectiveForToken_EveryScopeAtOnce_ReachesNoInstancePermission(t *testing.T) {
	t.Parallel()
	every := make([]authz.Scope, 0, len(authz.Scopes()))
	for _, def := range authz.Scopes() {
		every = append(every, def.Key)
	}
	for _, role := range authz.Roles() {
		got := authz.EffectiveForToken(role, every)
		for _, p := range authz.InstancePermissions() {
			require.False(t, got.Has(p),
				"a %q token holding every scope reaches the instance-realm %q", role, p)
		}
	}
}

// `instance.owner` expands to the whole instance realm, which is what its catalogue entry has
// always claimed and what nothing did until [authz.Implies] existed.
//
// The want set is built from [authz.InstancePermissions] — the REALM — rather than from
// [authz.Implies], so this compares the expansion against the catalogue instead of against
// itself. An `Implies` that returned an arbitrary subset would pass a test written the other way.
func TestEffectiveForSession_InstanceOwner_ExpandsToTheWholeInstanceRealm(t *testing.T) {
	t.Parallel()
	grant := authz.NewSet(authz.PermissionInstanceOwner)
	for _, role := range authz.Roles() {
		got := authz.EffectiveForSession(role, grant)
		for _, p := range authz.InstancePermissions() {
			require.True(t, got.Has(p),
				"a %q session granted instance.owner does not hold the instance-realm %q", role, p)
		}
		// And it widens the instance realm and nothing else: an expansion that reached into the
		// role matrix would hand an observer with one grant whatever an owner's role implies.
		want := authz.RolePermissions(role).Union(authz.NewSet(authz.InstancePermissions()...))
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("session capability for %q with instance.owner (-want +got):\n%s", role, diff)
		}
	}
}

// Every other key implies nothing, and the expansion is one pass deep.
//
// The second half is what lets [authz.ExpandInstance] be a single loop rather than a fixed point:
// if a key `instance.owner` expands to ever implied something itself, one pass would silently drop
// it and a session would hold a permission the catalogue says it does not.
func TestImplies_TheExpansion_IsOnePassDeep(t *testing.T) {
	t.Parallel()
	expanding := 0
	for _, def := range authz.Permissions() {
		implied := authz.Implies(def.Key)
		if def.Key != authz.PermissionInstanceOwner {
			require.Zero(t, implied.Len(), "%q implies %s and only instance.owner may", def.Key, implied)
			continue
		}
		expanding++
		require.Positive(t, implied.Len())
		for _, into := range implied.Slice() {
			require.True(t, authz.IsInstanceRealm(into),
				"instance.owner expands to the circle-realm %q, which a ledger cannot grant", into)
			require.Zero(t, authz.Implies(into).Len(),
				"instance.owner expands to %q, which implies more; one pass would drop it", into)
		}
	}
	require.Equal(t, 1, expanding, "no key expands; the search space was empty")

	// An unknown key expands to nothing rather than to everything, the same way an unknown role
	// grants nothing: a key that fell out of the catalogue must fail closed.
	require.Zero(t, authz.Implies(authz.Permission("nonsense")).Len())
	require.Zero(t, authz.ExpandInstance(authz.Set{}).Len())
}

// The expansion never reaches a token, at any role, at every scope at once, whatever the ledger
// records — because [authz.EffectiveForToken] takes no instance set to expand.
//
// This is the one way widening `instance.owner` could do harm: a key that reaches everything is
// only safe while a leaked PAT cannot reach the key. ADR-0012's capability floor is what says it
// cannot, and this is that claim stated against the widened catalogue rather than the old one.
// TestPrincipal_APATCarryingEveryInstanceGrant_ReachesNoneOfThem is the same assertion one layer
// up, where a Principal carries a field an expansion could actually read.
func TestEffectiveForSession_TheExpansion_NeverReachesAToken(t *testing.T) {
	t.Parallel()
	every := make([]authz.Scope, 0, len(authz.Scopes()))
	for _, def := range authz.Scopes() {
		every = append(every, def.Key)
	}
	for _, role := range authz.Roles() {
		byToken := authz.EffectiveForToken(role, every)
		// The session with the same role and the grant DOES hold them, which is what stops this
		// passing because the permissions are unreachable for everybody.
		bySession := authz.EffectiveForSession(role, authz.NewSet(authz.PermissionInstanceOwner))
		for _, p := range authz.InstancePermissions() {
			require.True(t, bySession.Has(p), "the session arm of this test is vacuous for %q", p)
			require.False(t, byToken.Has(p),
				"a %q token holding every scope reaches the instance-realm %q", role, p)
		}
	}
}
