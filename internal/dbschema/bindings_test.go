package dbschema_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/dbschema"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// updateEnums rewrites the generated Atlas file. A flag variable is package-level mutable state,
// which this repository bans; the exemption is that the testing package's flags have to be
// registered somewhere and there is no other way to spell this.
var updateEnums = flag.Bool("update", false, "rewrite "+dbschema.EnumsHCLPath)

const regenerate = "go test ./internal/dbschema -run TestEnumsHCL -update"

func TestEnumsHCL_Generated_MatchesTheCheckedInFile(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	want, err := dbschema.EnumsHCL()
	require.NoError(t, err)
	path := filepath.Join(root, dbschema.EnumsHCLPath)

	if *updateEnums {
		// Refused under CI for the same reason the golden corpus refuses it: the fastest route to
		// a green build must never be rewriting what the build checks.
		require.Empty(t, os.Getenv("CI"), "-update is refused in CI")
		require.NoError(t, os.WriteFile(path, []byte(want), 0o644))
		return
	}

	got, err := os.ReadFile(path)
	require.NoError(t, err, "run `%s`", regenerate)
	if diff := cmp.Diff(want, string(got)); diff != "" {
		t.Errorf("%s is stale (-generated +checked-in):\n%s\nregenerate it with `%s`",
			dbschema.EnumsHCLPath, diff, regenerate)
	}
}

// The binding table is the only thing standing between the catalogue and a column nobody
// constrained, so it is checked in both directions: every binding names a real enum, and every
// enum is either bound or recorded as deliberately unstored.
func TestEnumBindings_EveryCatalogueEnum_IsBoundOrExplicitlyUnstored(t *testing.T) {
	t.Parallel()

	bound := map[string]bool{}
	for _, b := range dbschema.Bindings() {
		_, ok := schemaenum.Lookup(b.Enum)
		require.True(t, ok, "%s.%s binds %q, which is not in the catalogue", b.Table, b.Column, b.Enum)
		bound[b.Enum] = true
	}

	unstored := dbschema.UnstoredEnums()
	for _, e := range schemaenum.All() {
		reason, recorded := unstored[e.Name]
		require.NotEqual(t, bound[e.Name], recorded,
			"enum %s must be either bound to a column or recorded as unstored, not both or neither",
			e.Name)
		if recorded {
			require.NotEmpty(t, reason, "enum %s is recorded as unstored with no reason", e.Name)
		}
	}
	for name := range unstored {
		_, ok := schemaenum.Lookup(name)
		require.True(t, ok, "%q is recorded as unstored but is not in the catalogue", name)
	}
}

func TestEnumBindings_EveryBinding_NamesATableInTheDomainModel(t *testing.T) {
	t.Parallel()
	known := domainModelTables(t)
	for _, b := range dbschema.Bindings() {
		require.True(t, known[b.Table],
			"binding %s.%s names a table the domain model does not list", b.Table, b.Column)
	}
}

func TestEnumsHCL_File_DeclaresOneLocalPerBinding(t *testing.T) {
	t.Parallel()
	hcl, err := dbschema.EnumsHCL()
	require.NoError(t, err)

	for _, b := range dbschema.Bindings() {
		predicate, err := b.Predicate()
		require.NoError(t, err)
		require.Contains(t, hcl, b.LocalName()+" ")
		require.Contains(t, hcl, predicate)
	}
	// A generated file with no regeneration command in it is a file somebody edits by hand.
	require.Contains(t, hcl, dbschema.RegenerateCommand)
}

// domainModelTables reads every table the domain model lists, from both of its scope tables. It is
// parsed rather than copied for the reason the whole canondoc package exists: a copied list makes
// the test agree with the copy.
func domainModelTables(t *testing.T) map[string]bool {
	t.Helper()
	doc, err := canondoc.LoadDomainModel()
	require.NoError(t, err)

	out := map[string]bool{}
	for _, heading := range []string{"Instance-scoped tables", "Circle-scoped tables"} {
		table, err := doc.TableUnder(heading, 0)
		require.NoError(t, err)
		names, err := table.Column("Table")
		require.NoError(t, err)
		for _, n := range names {
			name := canondoc.Unquote(n)
			require.NotEmpty(t, name)
			require.False(t, strings.Contains(name, " "), "table cell %q is not an identifier", n)
			out[name] = true
		}
	}
	require.NotEmpty(t, out)
	return out
}
