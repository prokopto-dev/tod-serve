package repo

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// DOC003 is the reverse of DOC002, and the direction nothing checked until three phantom mechanisms
// were found by hand. It is the gate that would have caught all three — and that took three
// checks, not one: only SQL002 was a gate id, while `scripts/licence-gate.sh` was a path and
// TestAuthFlow_RateLimitedCaller_CreatesNoRows was a test function. An id-only gate would have
// caught one of the three while claiming all of them, which is the same confident overstatement
// the page itself exists to stop.
//
// So it is exactly the gate that must be watched failing: one reporting success over a page it
// could not parse, or over a shape of claim it never looks at, would restore the hole it closes.
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
		{
			// A cited TEST that nobody wrote. This is the shape
			// TestAuthFlow_RateLimitedCaller_CreatesNoRows had, and an id-only gate read straight
			// past it — which is why the claim that DOC003 would have caught all three phantoms
			// was false until this case existed.
			name: "a test nobody wrote",
			page: "| `DOC003` | a real gate |\n" +
				"| `TestNope_DoesNotExist_Anywhere` | named as the mechanism |",
			want: "names a test that no _test.go file defines",
		},
		{
			// A cited SCRIPT that nobody wrote — the shape `scripts/licence-gate.sh` had, named
			// as enforcement on this very page for a long time before the file existed.
			name: "a script nobody wrote",
			page: "| `DOC003` | a real gate |\n" +
				"| `scripts/nope-gate.sh` | named as the mechanism |",
			want: "names a repository path that does not exist",
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
func runDocsCheck(t *testing.T, page string, extraEnv ...string) (string, error) {
	t.Helper()

	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "docs-check.sh"))
	cmd.Dir = root
	env := os.Environ()
	if page != "" {
		env = append(env, "TOD_INVARIANTS_PAGE="+page)
	}
	env = append(env, extraEnv...)
	cmd.Env = env
	// The exit code is discarded deliberately: DOC002 fails whenever the substitute page does not
	// register every gate in the repository, which every fixture here does not, so a non-zero exit
	// says nothing about DOC003's verdict. Only the DOC003 lines are this test's business, and they
	// are both what is returned and what the verdict is read from.
	out, _ := cmd.CombinedOutput()

	// A finding lists its items on the indented lines that follow it, so those are kept too: a
	// report saying only "names a test that does not exist" and not WHICH is half a finding, and
	// it cost a debugging session here before this line existed.
	var doc003 []string
	keep := false
	for _, ln := range strings.Split(string(out), "\n") {
		switch {
		case strings.Contains(ln, "DOC003") || strings.Contains(ln, "NOPE999"):
			keep = true
		case keep && strings.HasPrefix(ln, "  "):
		default:
			keep = false
		}
		if keep {
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

// The NOT HELD escape, which is the test-and-path analogue of writing a gate id bare.
//
// The page's header fixes the convention: "Some rows say the mechanism is missing, and they start
// `NOT HELD`." Such a row names a test or a package precisely to describe what does NOT exist yet
// — the goleak row names `internal/events` and a `TestMain` that Phase 6 will bring. Checking those
// would make DOC003 demand that a recorded gap be closed before it could be recorded, and a page
// that cannot write down a gap is how gaps stop being written down.
//
// Gate ids are deliberately NOT exempted by the row label, because they have their own in-band
// marker: an id is bare when it is not a claim. A path and a test name are backticked either way,
// since that is just code formatting, so for those two the row label is the only marker there is.
func TestDOC003_ANotHeldRow_MayNameWhatDoesNotExistYet(t *testing.T) {
	t.Parallel()

	page := filepath.Join(t.TempDir(), "invariants.md")
	require.NoError(t, os.WriteFile(page, []byte(
		"| `DOC003` | a real gate |\n"+
			"| **NOT HELD: the event package has no leak check** | `internal/events` does not exist; "+
			"the plan is `TestNope_DoesNotExist_Anywhere` in it, and `scripts/nope-gate.sh` to run it |\n"),
		0o600))

	out, err := runDocsCheck(t, page)
	require.NoError(t, err,
		"DOC003 fired on a NOT HELD row, so the page can no longer record a gap:\n%s", out)
}

// The same two claims WITHOUT the NOT HELD label must still be findings. Without this, the exemption
// above could widen to the whole page and every test here would keep passing.
func TestDOC003_TheNotHeldEscape_IsTheLabelAndNotTheProse(t *testing.T) {
	t.Parallel()

	page := filepath.Join(t.TempDir(), "invariants.md")
	require.NoError(t, os.WriteFile(page, []byte(
		"| `DOC003` | a real gate |\n"+
			"| the event package has no leak check | the plan is `TestNope_DoesNotExist_Anywhere`, "+
			"and `scripts/nope-gate.sh` to run it |\n"), 0o600))

	out, err := runDocsCheck(t, page)
	require.Error(t, err, "the NOT HELD exemption leaked to an ordinary row:\n%s", out)
	require.Contains(t, out, "names a test that no _test.go file defines")
	require.Contains(t, out, "names a repository path that does not exist")
}

// A gate that cannot read the repository must say so ABOUT ITSELF. An empty extraction makes every
// test the page cites look absent, so a broken parse does not fail quietly here — it fails loudly
// and wrongly, reporting two hundred phantoms with the one real finding buried in them. That is
// worse than no gate, because somebody would read the first few names, see they plainly exist, and
// stop believing the gate.
func TestDOC003_WhenItCanReadNoTests_ItBlamesItselfNotThePage(t *testing.T) {
	t.Parallel()

	out, err := runDocsCheck(t, "", "TOD_TEST_ROOTS="+t.TempDir())
	require.Error(t, err, "DOC003 passed while it could read no tests at all:\n%s", out)
	require.Contains(t, out, "this gate's own parse is wrong")
	require.NotContains(t, out, "names a test that no _test.go file defines",
		"a gate that cannot read the tests must not accuse the page of naming absent ones")
}

// The list of roots the gate reads is a claim about this repository, so it is checked rather than
// trusted: a package of tests added under a fourth directory would silently leave every test it
// defines invisible to DOC003, and the page could then cite one that does not exist. web/ is out
// because it is a separate module and holds no Go tests.
func TestDOC003_EveryTestFile_IsUnderADirectoryTheGateReads(t *testing.T) {
	t.Parallel()

	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	var stray []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "web", "node_modules", "bin":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		switch strings.SplitN(rel, string(filepath.Separator), 2)[0] {
		case "internal", "cmd", "test":
		default:
			stray = append(stray, rel)
		}
		return nil
	}))

	require.Empty(t, stray,
		"these test files are outside the roots scripts/docs-check.sh reads, so DOC003 cannot see "+
			"the tests they define:\n  %s\nAdd the directory to TEST_ROOTS in that script",
		strings.Join(stray, "\n  "))
}
