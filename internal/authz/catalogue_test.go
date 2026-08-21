package authz_test

import (
	"regexp"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// canonicalTokens reads one fenced block out of the canonical conventions. Every list below is
// parsed out of the document rather than copied into this file: a copy makes the test agree with
// itself, and the pair that drifts is the catalogue and the document.
func canonicalTokens(t *testing.T, heading string, block int) []string {
	t.Helper()
	doc, err := canondoc.LoadCanonical()
	require.NoError(t, err)
	b, err := doc.BlockUnder(heading, block)
	require.NoError(t, err)
	tokens := b.Fields()
	require.NotEmpty(t, tokens, "block %d under %q parsed to nothing", block, heading)
	return tokens
}

const permissionsHeading = "6. Permissions and scopes"

func TestPermissions_Catalogue_MatchesCanonicalConventions(t *testing.T) {
	t.Parallel()

	var got []string
	for _, def := range authz.Permissions() {
		got = append(got, string(def.Key))
	}
	if diff := cmp.Diff(canonicalTokens(t, permissionsHeading, 0), got); diff != "" {
		t.Errorf("permission keys differ from canonical conventions §6 (-document +code):\n%s", diff)
	}
}

func TestScopes_Catalogue_MatchesCanonicalConventions(t *testing.T) {
	t.Parallel()

	var got []string
	for _, def := range authz.Scopes() {
		got = append(got, string(def.Key))
	}
	if diff := cmp.Diff(canonicalTokens(t, permissionsHeading, 1), got); diff != "" {
		t.Errorf("PAT scopes differ from canonical conventions §6 (-document +code):\n%s", diff)
	}
}

// The load-bearing test for this package. The capability floor is what stops a leaked token
// seizing a circle, and it is stated in two places — a Go function and a normative document. This
// compares them element by element, in both directions, so neither can move without the other.
func TestCapabilityFloor_MatchesCanonicalConventions(t *testing.T) {
	t.Parallel()

	document := canonicalTokens(t, "The capability floor", 0)
	var code []string
	for _, p := range authz.CapabilityFloor() {
		code = append(code, string(p))
	}

	// Every permission the document floors, the code floors.
	for _, want := range document {
		require.Contains(t, code, want,
			"canonical conventions floor %q and authz.CapabilityFloor does not", want)
		_, known := authz.LookupPermission(authz.Permission(want))
		require.True(t, known, "the document floors %q, which is not a permission", want)
	}
	// And nothing else. A permission floored only in code would be an undocumented denial;
	// the failure mode this catches is the reverse of the one above and just as real.
	for _, got := range code {
		require.Contains(t, document, got,
			"authz.CapabilityFloor floors %q and canonical conventions do not", got)
	}

	sortedDoc, sortedCode := slices.Clone(document), slices.Clone(code)
	slices.Sort(sortedDoc)
	slices.Sort(sortedCode)
	if diff := cmp.Diff(sortedDoc, sortedCode); diff != "" {
		t.Errorf("capability floor (-document +code):\n%s", diff)
	}
}

// "There is no `admin:*` scope and no all-powerful token" is a sentence in the canonical
// conventions. This is the mechanism under it: if no scope grants a floor permission, then no
// token — at any scope, held by any role — can reach one.
func TestScopes_NoScope_ReachesACapabilityFloorPermission(t *testing.T) {
	t.Parallel()

	every := make([]authz.Scope, 0, len(authz.Scopes()))
	for _, def := range authz.Scopes() {
		every = append(every, def.Key)
	}
	granted := authz.GrantedByScopes(every)

	for _, p := range authz.CapabilityFloor() {
		require.False(t, granted.Has(p),
			"%q is in the capability floor and a token can reach it", p)
		require.Empty(t, authz.ScopesFor(p))
	}

	// And the strongest role, holding every scope there is, still cannot step over the floor.
	effective := authz.EffectiveForToken(authz.RoleOwner, every)
	for _, p := range authz.CapabilityFloor() {
		require.False(t, effective.Has(p), "an owner's token reaches %q", p)
	}
}

func TestScopes_NoWildcard_IsDeclared(t *testing.T) {
	t.Parallel()
	for _, def := range authz.Scopes() {
		require.NotContains(t, string(def.Key), "*",
			"a wildcard scope is an all-powerful token by another name")
	}
}

func TestPermissions_EveryDefinition_IsWellFormed(t *testing.T) {
	t.Parallel()
	// `<resource>.<action>`, dot-separated, lowercase.
	shape := regexp.MustCompile(`^[a-z]+(\.[a-z]+)+$`)

	seen := map[authz.Permission]bool{}
	for _, def := range authz.Permissions() {
		require.Regexp(t, shape, string(def.Key))
		require.False(t, seen[def.Key], "%q is declared twice", def.Key)
		seen[def.Key] = true
		require.Contains(t, []authz.Realm{authz.RealmCircle, authz.RealmInstance}, def.Realm,
			"%q has no realm", def.Key)
		require.NotEmpty(t, def.Summary, "%q has no summary; it would generate a blank doc row",
			def.Key)
	}
}

func TestScopes_EveryDefinition_IsWellFormed(t *testing.T) {
	t.Parallel()
	// `<family>:<verb>`, colon-separated, lowercase.
	shape := regexp.MustCompile(`^[a-z]+:[a-z]+$`)

	seen := map[authz.Scope]bool{}
	for _, def := range authz.Scopes() {
		require.Regexp(t, shape, string(def.Key))
		require.False(t, seen[def.Key], "%q is declared twice", def.Key)
		seen[def.Key] = true
		require.NotEmpty(t, def.Summary)
		require.NotEmpty(t, def.Grants,
			"%q grants nothing, so a token holding it can do nothing and nobody would know why",
			def.Key)
		for _, p := range def.Grants {
			_, known := authz.LookupPermission(p)
			require.True(t, known, "scope %q grants %q, which is not a permission", def.Key, p)
		}
	}
}

// A permission a circle role can hold but no role holds is a permission nothing can ever exercise.
func TestPermissions_CircleRealm_IsGrantedBySomeRole(t *testing.T) {
	t.Parallel()
	for _, def := range authz.Permissions() {
		if def.Realm != authz.RealmCircle {
			continue
		}
		require.NotEmpty(t, authz.RolesFor(def.Key), "no role grants %q", def.Key)
	}
}

// The instance realm is granted by the ledger and by nothing else. ADR-0012.
//
// This replaces TestPermissions_InstanceRealm_IsNotGrantedByAnyRole, which asserted only the first
// half below and existed to keep a hole visible while it was open. Both halves matter now and the
// second is the one that closed it: a permission no role grants and no ledger can hold is a
// permission nobody can ever exercise, which is the state this catalogue was in until ADR-0012.
func TestPermissions_InstanceRealm_IsGrantedOnlyByTheLedger(t *testing.T) {
	t.Parallel()
	grantable := authz.InstancePermissionEnum()
	require.NotEmpty(t, grantable.Values)

	for _, def := range authz.Permissions() {
		if def.Realm != authz.RealmInstance {
			// And nothing circle-realm leaks into the ledger's value set. A circle permission
			// there would be a grant nothing consults, because a circle permission comes from a
			// membership's role.
			require.False(t, grantable.Contains(string(def.Key)),
				"%q is circle-realm and instance_grant would accept it", def.Key)
			continue
		}
		require.Empty(t, authz.RolesFor(def.Key),
			"%q is instance-realm and a circle role grants it, which would be the second "+
				"authorization model internal/authz exists to keep out", def.Key)
		require.True(t, grantable.Contains(string(def.Key)),
			"%q is instance-realm and instance_grant cannot hold it, so nothing can grant it",
			def.Key)
	}
}

// No scope reaches an instance-realm permission, at any role. That is what makes "a leaked token
// cannot pivot into the instance" arithmetic rather than a promise — and it is checked over the
// whole catalogue rather than over the capability floor, because `ops.read` is instance-realm and
// deliberately NOT floored.
func TestScopes_NoScopeGrants_AnInstanceRealmPermission(t *testing.T) {
	t.Parallel()
	for _, p := range authz.InstancePermissions() {
		require.Empty(t, authz.ScopesFor(p),
			"%q is instance-realm and a PAT scope reaches it", p)
	}
}

func TestRequiresStepUp_FloorMembership_IsWhatItReports(t *testing.T) {
	t.Parallel()
	require.True(t, authz.RequiresStepUp(authz.PermissionTokenMint))
	require.True(t, authz.RequiresStepUp(authz.PermissionCircleSecurityManage))

	// Deliberately not in the floor: an invite is time-boxed, single-use, role-capped below owner
	// and fully audited, so a leaked bot token can add a visible, revocable member — not seize the
	// circle. See canonical conventions §6.
	require.False(t, authz.RequiresStepUp(authz.PermissionInviteCreate))
	require.False(t, authz.RequiresStepUp(authz.Permission("nonsense")))
}

func TestParsePermission_Value_IsAcceptedOrRefused(t *testing.T) {
	t.Parallel()
	got, err := authz.ParsePermission("tod.report")
	require.NoError(t, err)
	require.Equal(t, authz.PermissionTodReport, got)

	_, err = authz.ParsePermission("tod.destroy")
	require.ErrorIs(t, err, authz.ErrUnknownPermission)
	_, err = authz.ParsePermission("")
	require.ErrorIs(t, err, authz.ErrUnknownPermission)
}

func TestParseScope_Value_IsAcceptedOrRefused(t *testing.T) {
	t.Parallel()
	got, err := authz.ParseScope("tod:read")
	require.NoError(t, err)
	require.Equal(t, authz.ScopeTodRead, got)

	_, err = authz.ParseScope("admin:*")
	require.ErrorIs(t, err, authz.ErrUnknownScope)
}

func TestLookup_UnknownKey_ReportsMissing(t *testing.T) {
	t.Parallel()
	_, ok := authz.LookupPermission("circle.destroy")
	require.False(t, ok)
	_, ok = authz.LookupScope("circle:destroy")
	require.False(t, ok)
}
