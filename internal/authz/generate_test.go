package authz_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// updateDoc rewrites the generated documentation page. A flag variable is package-level mutable
// state, which this repository bans; the exemption is that the testing package's flags have to be
// registered somewhere and there is no other way to spell this.
var updateDoc = flag.Bool("update", false, "rewrite docs/reference/permissions.md")

// permissionsDocPath is the generated page, relative to the repository root.
const permissionsDocPath = "docs/reference/permissions.md"

func TestPermissionsDoc_Generated_MatchesTheCheckedInPage(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	path := filepath.Join(root, permissionsDocPath)
	want := authz.PermissionsDoc()

	if *updateDoc {
		// Refused under CI for the same reason the golden corpus refuses it: the fastest route to
		// a green build must never be rewriting what the build checks.
		require.Empty(t, os.Getenv("CI"), "-update is refused in CI")
		require.NoError(t, os.WriteFile(path, []byte(want), 0o644))
		return
	}

	got, err := os.ReadFile(path)
	require.NoError(t, err, "run `go test ./internal/authz -run TestPermissionsDoc -update`")
	if diff := cmp.Diff(want, string(got)); diff != "" {
		t.Errorf("%s is stale (-generated +checked-in):\n%s\nregenerate it with `%s`",
			permissionsDocPath, diff, "go test ./internal/authz -run TestPermissionsDoc -update")
	}
}

func TestPermissionsDoc_Page_DescribesEveryPermissionScopeAndRole(t *testing.T) {
	t.Parallel()
	page := authz.PermissionsDoc()

	for _, def := range authz.Permissions() {
		require.Contains(t, page, "`"+string(def.Key)+"`")
		require.Contains(t, page, def.Summary)
	}
	for _, def := range authz.Scopes() {
		require.Contains(t, page, "`"+string(def.Key)+"`")
	}
	for _, r := range authz.Roles() {
		require.Contains(t, page, string(r))
	}
	require.Contains(t, page, "role permissions ∩ token scopes")
}

func TestSeedSQL_Rows_CoverTheCatalogueAndTheMatrix(t *testing.T) {
	t.Parallel()
	seed := authz.SeedSQL()

	require.Contains(t, seed, "INSERT INTO permission (key, realm, requires_step_up, summary) VALUES")
	require.Contains(t, seed, "INSERT INTO role_permission (role, permission_key) VALUES")

	for _, def := range authz.Permissions() {
		require.Contains(t, seed, "('"+string(def.Key)+"', '"+string(def.Realm)+"'")
	}
	for _, role := range authz.Roles() {
		for _, p := range authz.RolePermissions(role).Slice() {
			require.Contains(t, seed, "('"+string(role)+"', '"+string(p)+"')")
		}
	}

	// The summaries contain apostrophes. An unescaped one produces a seed that will not parse,
	// which is a boot failure a long way from the sentence that caused it.
	require.Contains(t, seed, "the circle''s audit log")
	for _, line := range strings.Split(seed, "\n") {
		require.Equal(t, 0, strings.Count(line, "'")%2,
			"unbalanced quotes in seed line: %s", line)
	}
}

func TestSeedSQL_Output_IsStableAcrossRuns(t *testing.T) {
	t.Parallel()
	// A generated artefact that reorders itself produces a diff on every regeneration, and a diff
	// nobody can read is a diff nobody reviews.
	require.Equal(t, authz.SeedSQL(), authz.SeedSQL())
	require.Equal(t, authz.PermissionsDoc(), authz.PermissionsDoc())
}

func TestScopeEnum_Values_MatchTheScopeCatalogue(t *testing.T) {
	t.Parallel()
	enum := authz.ScopeEnum()
	require.Equal(t, authz.ScopeEnumName, enum.Name)

	var want []string
	for _, def := range authz.Scopes() {
		want = append(want, string(def.Key))
	}
	if diff := cmp.Diff(want, enum.Values); diff != "" {
		t.Errorf("scope enum (-catalogue +enum):\n%s", diff)
	}
	require.Contains(t, enum.CheckSQL("scope"), "'tod:read'")
}

func TestOpenAPIPermissions_EveryPermission_CarriesItsMetadata(t *testing.T) {
	t.Parallel()
	got := authz.OpenAPIPermissions()
	require.Len(t, got, len(authz.Permissions()))

	byKey := map[string]authz.OpenAPIPermission{}
	for _, ext := range got {
		byKey[ext.Key] = ext
	}

	for _, def := range authz.Permissions() {
		ext, ok := byKey[string(def.Key)]
		require.True(t, ok)
		require.Equal(t, string(def.Realm), ext.Realm)
		require.Equal(t, def.StepUp != authz.StepUpNone, ext.RequiresStepUp)
		require.Equal(t, string(def.StepUp), ext.StepUpTier)
		require.Equal(t, def.Floor, ext.Floor)
		require.Equal(t, def.Summary, ext.Summary)
		if def.Floor {
			require.Empty(t, ext.Scopes, "%q is in the floor and lists a scope", def.Key)
		}
	}

	// It is a struct rather than a map[string]any so that the generated spec can be typechecked;
	// this is the shape that ends up under `x-tod-permission`.
	encoded, err := json.Marshal(byKey["tod.read"])
	require.NoError(t, err)
	require.JSONEq(t, `{
		"key": "tod.read",
		"realm": "circle",
		"requires_step_up": false,
		"step_up_tier": "none",
		"floor": false,
		"roles": ["observer", "member", "officer", "owner"],
		"scopes": ["tod:read", "events:subscribe"],
		"summary": "Read the board, the reports behind it, and the quake log"
	}`, string(encoded))
}

func TestRolesFor_Permission_ListsGrantingRolesWeakestFirst(t *testing.T) {
	t.Parallel()
	require.Equal(t,
		[]authz.Role{authz.RoleObserver, authz.RoleMember, authz.RoleOfficer, authz.RoleOwner},
		authz.RolesFor(authz.PermissionTodRead))
	require.Equal(t, []authz.Role{authz.RoleOwner}, authz.RolesFor(authz.PermissionCircleDelete))
	require.Empty(t, authz.RolesFor(authz.PermissionInstanceOwner))
}

func TestScopesFor_Permission_ListsReachingScopes(t *testing.T) {
	t.Parallel()
	require.Equal(t, []authz.Scope{authz.ScopeTodRead, authz.ScopeEventsSubscribe},
		authz.ScopesFor(authz.PermissionTodRead))
	require.Empty(t, authz.ScopesFor(authz.PermissionTokenMint))

	// Every scope a permission lists must list it back.
	for _, def := range authz.Scopes() {
		for _, p := range def.Grants {
			require.True(t, slices.Contains(authz.ScopesFor(p), def.Key))
		}
	}
}
