package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// DOC003 is the reverse of DOC002, and the direction nothing checked until three phantom mechanisms
// were found by hand. It is the gate that would have caught all three, so it is exactly the gate
// that must be watched failing: one reporting success over a page it could not parse would restore
// the hole it exists to close.
func TestDOC003_APhantomGate_IsReported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		page string
		want string
	}{
		{
			// The whole point: a page CLAIMING enforcement that does not exist.
			name: "a gate nobody wrote",
			page: "| `NOPE999` | refuses something | landed |",
			want: "names a gate that is defined in no script",
		},
		{
			// Vacancy. A gate that parses nothing must say so rather than pass, or deleting the
			// table silently disables it.
			name: "a page with no gate ids at all",
			page: "# Invariants\n\nProse with no identifiers in it.",
			want: "no gate ids were parsed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			page := filepath.Join(t.TempDir(), "invariants.md")
			require.NoError(t, os.WriteFile(page, []byte(tc.page), 0o600))

			out, err := runDocsCheck(t, page)
			require.Error(t, err, "DOC003 passed over %s:\n%s", tc.name, out)
			require.Contains(t, out, "DOC003")
			require.Contains(t, out, tc.want)
		})
	}
}

// The false-positive half. A gate that fired on the real page would be turned off within a week,
// and this is also what pins the two conventions DOC003 relies on: a BARE id is prose about a dead
// gate and must not be a finding, and an algorithm name that happens to share the shape is not a
// gate at all.
//
// The bare id below is NOPE999 — the same phantom TestDOC003_APhantomGate_IsReported requires a
// finding for — so the pair differs in the backticks and nothing else, and the backtick is proved
// to be what decides. An id that merely happens to exist would pass this whether the rule worked
// or not: SQL002 was that id until it was written, at which point this assertion became vacuous
// without going red, which is the failure a false-positive half is least able to notice.
func TestDOC003_TheRealPage_Passes(t *testing.T) {
	t.Parallel()

	out, err := runDocsCheck(t, "")
	require.NoError(t, err, "the checked-in invariants page does not pass its own gate:\n%s", out)
	require.Contains(t, out, "DOC003")

	page := filepath.Join(t.TempDir(), "invariants.md")
	require.NoError(t, os.WriteFile(page, []byte(
		// A live gate, a dead one named bare, and an algorithm that looks like an id.
		"| `DOC003` | a real gate |\n"+
			"| NOPE999 | a gate nobody wrote, discussed rather than claimed |\n"+
			"| `SHA256` | the hash, not a gate |\n"), 0o600))

	out, err = runDocsCheck(t, page)
	require.NoError(t, err,
		"DOC003 fired on a bare dead id or on an algorithm name:\n%s", out)
}

// runDocsCheck runs the whole documentation gate, optionally against a substitute invariants page.
// The other gates in that script read the real tree, so only DOC003's verdict is asserted on.
func runDocsCheck(t *testing.T, page string) (string, error) {
	t.Helper()

	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "docs-check.sh"))
	cmd.Dir = root
	if page != "" {
		cmd.Env = append(os.Environ(), "TOD_INVARIANTS_PAGE="+page)
	}
	// The exit code is discarded deliberately: DOC002 fails whenever the substitute page does not
	// register every gate in the repository, which every fixture here does not, so a non-zero exit
	// says nothing about DOC003's verdict. Only the DOC003 lines are this test's business, and they
	// are both what is returned and what the verdict is read from.
	out, _ := cmd.CombinedOutput()

	var doc003 []string
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.Contains(ln, "DOC003") || strings.Contains(ln, "NOPE999") {
			doc003 = append(doc003, ln)
		}
	}
	joined := strings.Join(doc003, "\n")
	if strings.Contains(joined, "\033[31m") {
		return joined, errDOC003
	}
	return joined, nil
}

// errDOC003 marks a DOC003 finding, so the tests above assert on the gate's verdict rather than on
// the exit code of a script whose other gates the fixtures deliberately break.
var errDOC003 = errDoc("DOC003 reported a finding")

type errDoc string

func (e errDoc) Error() string { return string(e) }
