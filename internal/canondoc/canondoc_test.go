package canondoc_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// write puts a document in a temporary directory and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.md")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

const sample = "# Title\n" +
	"\n" +
	"## First section\n" +
	"\n" +
	"```\n" +
	"alpha beta\n" +
	"gamma\n" +
	"```\n" +
	"\n" +
	"prose\n" +
	"\n" +
	"```sql\n" +
	"SELECT 1;\n" +
	"```\n" +
	"\n" +
	"### A subsection\n" +
	"\n" +
	"```\n" +
	"delta\n" +
	"```\n"

func TestLoad_Blocks_BelongToTheNearestPrecedingHeading(t *testing.T) {
	t.Parallel()
	doc, err := canondoc.Load(write(t, sample))
	require.NoError(t, err)

	first, err := doc.BlocksUnder("First section")
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, "", first[0].Language)
	require.Equal(t, "sql", first[1].Language)
	if diff := cmp.Diff([]string{"alpha", "beta", "gamma"}, first[0].Fields()); diff != "" {
		t.Errorf("fields (-want +got):\n%s", diff)
	}

	// A subsection's blocks belong to the subsection, which is what makes "the capability floor"
	// addressable separately from the section it sits in.
	sub, err := doc.BlockUnder("A subsection", 0)
	require.NoError(t, err)
	require.Equal(t, []string{"delta"}, sub.Fields())
	require.Equal(t, 18, sub.Line)
}

func TestBlocksUnder_NoUniqueHeading_IsAnError(t *testing.T) {
	t.Parallel()
	doc, err := canondoc.Load(write(t, sample))
	require.NoError(t, err)

	// A heading that matches nothing must fail rather than return an empty list, or a gate that
	// compares against it passes over an empty search space.
	_, err = canondoc.Load(write(t, sample))
	require.NoError(t, err)
	_, err = doc.BlocksUnder("Second section")
	require.ErrorIs(t, err, canondoc.ErrNotFound)

	// A substring matching two headings must fail rather than silently merge them.
	_, err = doc.BlocksUnder("section")
	require.ErrorIs(t, err, canondoc.ErrNotFound)

	// An index outside the blocks that exist, in either direction. A negative one reached the
	// slice and panicked; a gate that crashes reads as a broken build rather than as a finding.
	_, err = doc.BlockUnder("A subsection", 7)
	require.ErrorIs(t, err, canondoc.ErrNotFound)
	_, err = doc.BlockUnder("A subsection", -1)
	require.ErrorIs(t, err, canondoc.ErrNotFound)
	_, err = doc.BlockUnder("First section", -3)
	require.ErrorIs(t, err, canondoc.ErrNotFound)
}

func TestLoad_Malformed_IsAnError(t *testing.T) {
	t.Parallel()
	_, err := canondoc.Load(write(t, "# Title\n\n```\nunterminated\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unterminated fence")

	_, err = canondoc.Load(filepath.Join(t.TempDir(), "absent.md"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// The blocks three gates depend on, asserted here so that a document reorganisation fails in one
// obvious place rather than as three confusing failures elsewhere.
func TestLoadCanonical_TheRealDocument_HasTheBlocksTheGatesParse(t *testing.T) {
	t.Parallel()
	doc, err := canondoc.LoadCanonical()
	require.NoError(t, err)

	enums, err := doc.BlockUnder("5. Enums", 0)
	require.NoError(t, err)
	require.Contains(t, enums.Body, "membership.role:")

	permissions, err := doc.BlockUnder("6. Permissions and scopes", 0)
	require.NoError(t, err)
	require.Contains(t, permissions.Fields(), "tod.read.attribution")

	scopes, err := doc.BlockUnder("6. Permissions and scopes", 1)
	require.NoError(t, err)
	require.Contains(t, scopes.Fields(), "events:subscribe")

	floor, err := doc.BlockUnder("The capability floor", 0)
	require.NoError(t, err)
	require.Contains(t, floor.Fields(), "token.mint")

	require.Contains(t, doc.Raw(), "observer < member < officer < owner")
	require.Equal(t, canondoc.CanonicalPath, filepath.ToSlash(
		doc.Path[len(doc.Path)-len(canondoc.CanonicalPath):]))
}

func TestRepoRoot_FromAnyPackage_FindsTheModuleRoot(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(root, "go.mod"))
	require.FileExists(t, filepath.Join(root, canondoc.CanonicalPath))
}
