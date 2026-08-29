package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// catalogueFile DECLARES every permission constant, so every key is named in it and finding one
// there would make the whole catalogue look reachable.
var catalogueFile = filepath.Join("internal", "authz", "catalogue.go")

// unreachedPermissions names every catalogue key that no route requires, no handler consults and
// no expansion covers — and says why it is still in the catalogue.
//
// It is a two-sided map, exactly as internal/apierr/reachable_test.go is: a key here that HAS
// become reachable is as red as one missing from it. That is what makes it different from the
// known-gap list this test replaces, which held `instance.owner` while `instance.owner` granted
// nothing, and would have gone on holding it forever.
//
// A reason here is a claim about a milestone, not a shrug. A permission with no checker and no
// milestone should be deleted or wired up.
func unreachedPermissions() map[authz.Permission]string {
	return map[authz.Permission]string{
		// PRE-EXISTING, and found by this gate rather than by this change. `revokeToken` is the
		// only route over `/tokens/{token_id}` and it is `self`: it revokes one of the caller's
		// OWN devices and asks no permission at all. So "revoke another principal's token", which
		// is what this key's summary promises, is an operation with no route and no checker —
		// the same shape of defect as `instance.owner`, in the circle realm.
		authz.PermissionTokenRevoke: "no operation revokes ANOTHER principal's token yet; " +
			"`revokeToken` is `self` and covers only the caller's own devices",
	}
}

// TestPermissions_EveryPermission_IsRequiredByARouteOrExpandsToOnesThatAre is the gate on a
// permission that can be granted and checked by nothing.
//
// It replaces TestPermissions_EveryInstanceRealmKey_ReachesARouteOrIsNamedHere, which asked the
// same question of the instance realm only and answered it with a list `instance.owner` sat on.
// `tod-serve instance grant --permission instance.owner` then wrote a durable, hash-chained,
// audited decision that no line of code consulted, while the deployment runbook told operators to
// make exactly that grant and the next section sent them somewhere it did not reach. This is the
// class, gated over the WHOLE catalogue.
//
// A permission is reachable three ways, and the three come from three different places on purpose
// — a comparison whose sides share a derivation passes for any input:
//
//  1. THE ROUTE REGISTRY, as data. `Route.Permissions` is what the middleware checks before a
//     handler runs.
//  2. HANDLER SOURCE, as an AST, over `internal/api` and nothing else. Some permissions shape a
//     response rather than gate a route — `tod.read.attribution` is the observer role, and it is
//     asked inside the board handler — so a registry-only walk would call it unreachable and be
//     wrong. The walk is confined to the edge because "consulted" has to mean *asked about a
//     caller*: a domain package that GRANTS a permission names the same constant, and counting
//     that as reachability is how arm 3 goes quiet — `internal/membership` writes
//     `instance.owner` when it admits an instance's first administrator (ADR-0016), and a
//     whole-`internal` walk read that write as a check.
//  3. AN EXPANSION, from [authz.Implies]. `instance.owner` reaches no route itself and expands to
//     the instance realm, whose members must be reachable by (1) or (2) in their own right. The
//     expansion may not contain the key itself, or this arm would prove anything.
//
// Anything else must be named in [unreachedPermissions] with a reason, and naming a reachable one
// is red too.
func TestPermissions_EveryPermission_IsRequiredByARouteOrExpandsToOnesThatAre(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	required := permissionsRequiredByRoutes(t)
	consulted := permissionsConsultedByHandlers(t, root)
	named := unreachedPermissions()

	// Arm 1 and arm 2, unioned. This is what an expansion has to land on: a key that expands only
	// to other expansions would be a chain nothing at the end of it ever checks.
	direct := map[authz.Permission]bool{}
	for p := range required {
		direct[p] = true
	}
	for p := range consulted {
		direct[p] = true
	}

	expanded := 0
	for _, def := range authz.Permissions() {
		reason, isNamed := named[def.Key]
		if isNamed {
			require.NotEmpty(t, reason, "%q is named as unreached with no reason", def.Key)
		}

		reach := direct[def.Key]
		if !reach {
			// The expansion arm. Every member has to be directly reachable, and the key may not
			// be a member of its own expansion — the first is what stops `instance.owner`
			// expanding into a second unreachable key, the second is what stops it expanding into
			// itself and calling that coverage.
			implied := authz.Implies(def.Key)
			if implied.Len() > 0 {
				require.False(t, implied.Has(def.Key),
					"%q is in its own expansion, so the expansion proves nothing about it", def.Key)
				reach = true
				for _, into := range implied.Slice() {
					if !direct[into] {
						reach = false
						t.Errorf("%q expands to %q, which no route requires and no handler "+
							"consults, so the expansion reaches nothing", def.Key, into)
					}
				}
				if reach {
					expanded++
				}
			}
		}

		if reach == isNamed {
			// Reported rather than fataled, so one run names every permission that needs a
			// decision instead of stopping at the first.
			t.Errorf("%q is reachable=%t and named-as-unreached=%t: it must be exactly one. A "+
				"permission nothing checks is a grant somebody can make, audit and be told to "+
				"make, that hands over nothing",
				def.Key, reach, isNamed)
		}
	}

	for p := range named {
		_, ok := authz.LookupPermission(p)
		require.True(t, ok, "%q is named as unreached and is not in the catalogue", p)
	}

	// The vacuity guards. Each arm is asked whether it found anything, because a gate that passes
	// over three empty searches passes over any tree at all — and the third is the one this change
	// added: with no expansion, `instance.owner` is unreachable and this is the second red test.
	require.NotEmpty(t, required, "no route requires any permission; the registry walk is wrong")
	require.NotEmpty(t, consulted, "no handler consults any permission; the source walk is wrong")
	require.Positive(t, expanded,
		"no permission is reachable only through an expansion, so that arm proved nothing")
}

// permissionsRequiredByRoutes reads arm 1 out of the route registry, as data.
func permissionsRequiredByRoutes(t *testing.T) map[authz.Permission]bool {
	t.Helper()
	out := map[authz.Permission]bool{}
	for _, r := range api.Routes() {
		for _, p := range r.Permissions {
			out[p] = true
		}
	}
	return out
}

// permissionIdentifier matches a permission constant's name as a whole word, so `PermissionTodRead`
// does not match `PermissionTodReadAttribution`.
var permissionIdentifier = regexp.MustCompile(`\bPermission[A-Za-z0-9]+\b`)

// permissionsConsultedByHandlers reads arm 2: every permission constant named in `internal`,
// outside the catalogue that declares them and outside the route registry that arm 1 already read.
//
// `cmd` is not searched, and that exclusion is the point rather than a convenience. The console
// holds the database and authorises nothing — `internal/instancegrant` and `cmd/tod-serve/instance.go`
// both say so — so a permission named there is named in a flag list or a next-steps message, never
// in a decision. `tod-serve init` prints `instance.owner` at the end of its bootstrap; counting
// that as a checker is exactly how a key that grants nothing looks reachable, which is the bug
// this test exists for.
//
// Comments are stripped by parsing rather than by grepping: a permission named only in a sentence
// about it is a permission nothing checks.
func permissionsConsultedByHandlers(t *testing.T, root string) map[authz.Permission]bool {
	t.Helper()

	names := permissionConstants(t, root)
	// The registry is arm 1's source and must not double as arm 2's: a route's declared
	// permissions are already counted, and reading them here again would make every arm agree
	// with itself.
	skipFile := filepath.Join(root, "internal", "api", "registry.go")

	used := map[string]bool{}
	files := 0
	err := filepath.WalkDir(filepath.Join(root, "internal", "api"), func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() || !strings.HasSuffix(path, ".go"):
			return nil
		case strings.HasSuffix(path, "_test.go") || path == skipFile:
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		files++
		ast.Inspect(file, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok && permissionIdentifier.MatchString(ident.Name) {
				used[ident.Name] = true
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	require.Positive(t, files, "no production Go under internal/api was parsed; the walk is wrong")

	out := map[authz.Permission]bool{}
	for key, ident := range names {
		if used[ident] {
			out[key] = true
		}
	}
	return out
}

// permissionConstants maps each permission VALUE to the Go constant holding it, parsed out of the
// catalogue. Going through the AST rather than deriving the name from the key is what lets arm 2
// find `authz.PermissionTodReadAttribution` without this test knowing how the two are spelled.
func permissionConstants(t *testing.T, root string) map[authz.Permission]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, catalogueFile), nil, 0)
	require.NoError(t, err, "parse %s", catalogueFile)

	out := map[authz.Permission]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Values) != 1 || len(spec.Names) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, unquoteErr := strconv.Unquote(lit.Value)
		if unquoteErr != nil {
			return true
		}
		if name := spec.Names[0].Name; strings.HasPrefix(name, "Permission") {
			out[authz.Permission(value)] = name
		}
		return true
	})

	// Every key in the catalogue has to have been found, or arm 2 is silently blind to the ones it
	// missed and every one of them looks unreachable.
	for _, def := range authz.Permissions() {
		require.Contains(t, out, def.Key,
			"%q is in the catalogue and no constant in %s was parsed for it", def.Key, catalogueFile)
	}
	return out
}
