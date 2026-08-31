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

// A gate is defined by a DEFINITION, never by a mention of one — and this is the half that keeps
// DOC003 from reintroducing the failure it exists to catch.
//
// The scan used to accept any matching text, so deleting a gate and leaving its `# report FOO001`
// comment behind kept a backticked `FOO001` on the page resolving. The page would then say the rule
// was enforced, DOC003 would agree, and nothing would enforce it: a phantom mechanism, certified by
// the phantom-mechanism gate. Both directions are driven, because a scan tightened until it matches
// nothing would pass this test's first half and fail the repository.
func TestDOC003_AGateNamedOnlyInAComment_IsStillAPhantom(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		script  string
		wantErr bool
	}{
		{
			// The deleted gate's comment, left behind. NOPE999 is named nowhere else.
			name:    "a comment is a mention, not a definition",
			script:  "#!/usr/bin/env bash\n# report NOPE999 \"this gate was deleted in 2026\"\nreport REAL001 \"a real one\"\n",
			wantErr: true,
		},
		{
			// The same id, actually called. Without this the test above passes for a scan that
			// finds nothing at all, which would make every gate on the real page a phantom.
			name:    "a call is a definition",
			script:  "#!/usr/bin/env bash\nreport NOPE999 \"a gate that exists\"\nreport REAL001 \"a real one\"\n",
			wantErr: false,
		},
		{
			// A call reached through a guard still defines the gate: the rule is "no `#` before it
			// on the line", not "the line starts with report".
			name:    "a guarded call is still a definition",
			script:  "#!/usr/bin/env bash\n[ -n \"$x\" ] && report NOPE999 \"guarded\"\nreport REAL001 \"a real one\"\n",
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o750))
			require.NoError(t, os.WriteFile(
				filepath.Join(root, "scripts", "gate.sh"), []byte(tc.script), 0o600))

			page := filepath.Join(t.TempDir(), "invariants.md")
			require.NoError(t, os.WriteFile(page, []byte(
				"| `NOPE999` | the gate under test |\n"+
					"| `REAL001` | present so the scan is never empty |\n"), 0o600))

			out, err := runDocsCheck(t, page, "TOD_GATE_ROOT="+root)
			if tc.wantErr {
				require.Error(t, err, "DOC003 accepted a gate that only a comment names:\n%s", out)
				require.Contains(t, out, "NOPE999")
				return
			}
			require.NoError(t, err, "DOC003 called a defined gate a phantom:\n%s", out)
		})
	}
}

// And its vacancy half, for the same reason the test scan has one: a scan that finds no definitions
// must say its own scan is wrong, rather than report every gate the page names as a phantom and
// bury the real finding under three dozen false ones.
func TestDOC003_WhenItCanReadNoGates_ItBlamesItselfNotThePage(t *testing.T) {
	t.Parallel()

	out, err := runDocsCheck(t, "", "TOD_GATE_ROOT="+t.TempDir())
	require.Error(t, err, "DOC003 passed while it could read no gate definitions:\n%s", out)
	require.Contains(t, out, "this gate's own scan is wrong")
	require.NotContains(t, out, "is defined in no script",
		"a gate that cannot read the definitions must not accuse the page of naming absent ones")
}

// A path git is told to ignore is one the repository is REQUIRED not to contain, so its absence is
// the invariant rather than a broken claim. `deploy/.env` is named by the rule that it must never be
// committed; demanding it exist would invert that rule and make the page unable to state it.
//
// The second case is what keeps the exemption honest: a file that is merely missing, and not
// ignored, is still a finding. Without it the escape could widen to every path and this test would
// keep passing.
func TestDOC003_APathTheRepositoryMustNotContain_IsNotAPhantom(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			// Gitignored, absent on purpose, and named by the invariant about its absence.
			name:    "a gitignored path is exempt",
			path:    "deploy/.env",
			wantErr: false,
		},
		{
			// Not ignored, simply absent. This is the phantom shape.
			name:    "a merely missing path is still a finding",
			path:    "deploy/nope.env",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			page := filepath.Join(t.TempDir(), "invariants.md")
			require.NoError(t, os.WriteFile(page, []byte(
				"| `DOC003` | a real gate |\n"+
					"| the rule about it | `"+tc.path+"` |\n"), 0o600))

			out, err := runDocsCheck(t, page)
			if tc.wantErr {
				require.Error(t, err, "DOC003 accepted a path that is simply missing:\n%s", out)
				require.Contains(t, out, tc.path)
				return
			}
			require.NoError(t, err,
				"DOC003 demanded a path the repository is required NOT to contain:\n%s", out)
		})
	}
}

// The other half of "a mention is not a definition", and the one excluding comments does not reach:
// quoted TEXT. `echo "report NOPE999 was removed"` is prose about a gate, not a call to one, and it
// certified NOPE999 as implemented — the scan saw a line with no `#` on it and a `report FOO001`
// in the middle, which is exactly what a real call looks like once you stop reading shell as shell.
//
// The two real calls are driven in the same table so the strip cannot be widened until it matches
// nothing: a `sed` that ate the whole line would satisfy the phantom cases and silently turn every
// gate in the repository into a phantom of its own.
func TestDOC003_AGateNamedOnlyInAString_IsStillAPhantom(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		line    string
		wantErr bool
	}{
		{
			name:    "a double-quoted string is a mention",
			line:    `echo "report NOPE999 was removed in 2026"`,
			wantErr: true,
		},
		{
			name:    "a single-quoted string is a mention",
			line:    `echo 'pass NOPE999 also went'`,
			wantErr: true,
		},
		{
			// A real call keeps its id through the strip: only the message is quoted.
			name:    "a call with a quoted message is a definition",
			line:    `report NOPE999 "a gate that exists"`,
			wantErr: false,
		},
		{
			// And the strip must not eat a guarded call, whose condition carries quotes of its own.
			name:    "a guarded call with quotes in its condition is a definition",
			line:    `[ -n "$x" ] && report NOPE999 "still a call"`,
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "gate.sh"),
				[]byte("#!/usr/bin/env bash\n"+tc.line+"\nreport REAL001 \"keeps the scan non-empty\"\n"),
				0o600))

			page := filepath.Join(t.TempDir(), "invariants.md")
			require.NoError(t, os.WriteFile(page, []byte(
				"| `NOPE999` | the gate under test |\n"+
					"| `REAL001` | present so the scan is never empty |\n"), 0o600))

			out, err := runDocsCheck(t, page, "TOD_GATE_ROOT="+root)
			if tc.wantErr {
				require.Error(t, err, "DOC003 certified a gate that only a string names:\n%s", out)
				require.Contains(t, out, "NOPE999")
				return
			}
			require.NoError(t, err, "DOC003 called a defined gate a phantom:\n%s", out)
		})
	}
}

// The third and narrowest way a mention reads as a definition, and the one stripping quotes does not
// reach: an UNQUOTED argument. `echo report NOPE999 was removed` is the same prose as the quoted
// case with the quotes taken off, and there `report` is an argument to `echo` — not a command at
// all. A scan matching bare words anywhere on a line cannot tell it from a call.
//
// So a call is recognised only where a command can BEGIN: start of line, after `; & | ( ) { }`
// — which covers `&&`, `||`, a case arm's `)` and a `{ … }` group — or after `then`, `else`, `do`,
// `elif`. Every command position that appears in this repository is driven below beside the
// mentions, because the failure mode of this fix is a pattern narrowed until real calls stop
// matching: that would turn every gate in the repository into a phantom, and the mention cases
// alone would still pass.
func TestDOC003_AGateNamedAsAnArgument_IsStillAPhantom(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		line    string
		wantErr bool
	}{
		{
			name:    "an unquoted argument to echo is a mention",
			line:    `echo report NOPE999 was removed`,
			wantErr: true,
		},
		{
			name:    "an unquoted argument to any command is a mention",
			line:    `printf '%s' vacant NOPE999`,
			wantErr: true,
		},
		{name: "a plain call", line: `report NOPE999 "plain"`, wantErr: false},
		{name: "a call after &&", line: `[ -n "$x" ] && report NOPE999 "after &&"`, wantErr: false},
		{name: "a call after || {", line: `[ -f x ] || { report NOPE999 "grouped"; }`, wantErr: false},
		{
			name:    "a call in a case arm",
			line:    `case "$m" in missing) report NOPE999 "arm" ;; esac`,
			wantErr: false,
		},
		{
			name:    "a call after then and after else",
			line:    `if [ -n "$y" ]; then report NOPE999 "t"; else report NOPE999 "e"; fi`,
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "gate.sh"),
				[]byte("#!/usr/bin/env bash\n"+tc.line+"\nreport REAL001 \"keeps the scan non-empty\"\n"),
				0o600))

			page := filepath.Join(t.TempDir(), "invariants.md")
			require.NoError(t, os.WriteFile(page, []byte(
				"| `NOPE999` | the gate under test |\n"+
					"| `REAL001` | present so the scan is never empty |\n"), 0o600))

			out, err := runDocsCheck(t, page, "TOD_GATE_ROOT="+root)
			if tc.wantErr {
				require.Error(t, err, "DOC003 certified a gate that is only an argument:\n%s", out)
				require.Contains(t, out, "NOPE999")
				return
			}
			require.NoError(t, err, "DOC003 called a defined gate a phantom:\n%s", out)
		})
	}
}

// The fourth and last reading of "names a gate", and the one that killed the regex outright: an
// ESCAPED quote. A regex cannot pair quotes it cannot count. `s/"[^"]*"//g` pairs left to right, so
// on
//
//	echo "a\"; report NOPE999 \"b"
//
// — one argument to echo, escaped quotes and all — it removes `"a\"` and `\"b"`, leaving
// `echo ; report NOPE999`. That hands the command-position check a semicolon and a call which exist
// in no shell reading of the line, and it certified the phantom. The stripper is a character
// scanner now, and these are the rules it exists to get right.
//
// The last case is the one that motivated replacing the regex rather than patching it: a `#` opens
// a comment only at a word boundary, so `${x#prefix}` is an expansion. The old `s/#.*//` truncated
// there and silently lost any real call that followed on the same line.
func TestDOC003_ShellTextIsScanned_NotPatternMatched(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		line    string
		wantErr bool
	}{
		{
			name:    "an escaped quote does not end the string",
			line:    `echo "a\"; report NOPE999 \"b"`,
			wantErr: true,
		},
		{
			name:    "escaped quotes around a whole clause",
			line:    `echo "he said \"stop\"; report NOPE999 was removed"`,
			wantErr: true,
		},
		{
			// Single quotes take no escapes at all: the backslash is literal, and the string ends
			// at the next quote whatever precedes it.
			name:    "a backslash inside single quotes is literal",
			line:    `echo 'a\'"'"'; report NOPE999 is still quoted text'`,
			wantErr: true,
		},
		{
			name:    "a hash inside a parameter expansion is not a comment",
			line:    `v=${x#prefix}; report NOPE999 "a real call after an expansion"`,
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "gate.sh"),
				[]byte("#!/usr/bin/env bash\n"+tc.line+"\nreport REAL001 \"keeps the scan non-empty\"\n"),
				0o600))

			page := filepath.Join(t.TempDir(), "invariants.md")
			require.NoError(t, os.WriteFile(page, []byte(
				"| `NOPE999` | the gate under test |\n"+
					"| `REAL001` | present so the scan is never empty |\n"), 0o600))

			out, err := runDocsCheck(t, page, "TOD_GATE_ROOT="+root)
			if tc.wantErr {
				require.Error(t, err,
					"DOC003 read shell text inside a string as a command:\n%s", out)
				require.Contains(t, out, "NOPE999")
				return
			}
			require.NoError(t, err, "DOC003 called a defined gate a phantom:\n%s", out)
		})
	}
}
