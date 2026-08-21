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
		// role: holding one instance permission implies none of the rest.
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
