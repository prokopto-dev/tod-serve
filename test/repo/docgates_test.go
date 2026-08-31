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
// were found by hand. It reads what the gates EMITTED when they ran, never what their source
// contains — which is the whole design, arrived at the expensive way: the first implementation
// grepped the scripts and took seven review rounds, one lexical case each, with `$(...)` and
// backticks still unhandled when it was abandoned.
//
// So the fixtures here are CAPTURES, not scripts. There is no shell to lex, and the cases that
// consumed those seven rounds — a gate named in a comment, in a quoted string, as an unquoted
// argument, inside a heredoc — cannot be expressed against this gate at all. That is the point of
// the change, and the reason this file is a third the size of the one it replaces.

// writeCapture writes a capture holding `ids` as emitted gate lines, padded to clear the floor.
func writeCapture(t *testing.T, ids ...string) string {
	t.Helper()
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(id + "     something it checked\n")
	}
	// The floor exists to catch a truncated run, so a fixture testing anything else must clear it.
	for i := range 40 {
		b.WriteString("PAD" + string(rune('A'+i%26)) + "999     padding\n")
	}
	b.WriteString("TestPadding_Exists_SoTheTestScanIsNotEmpty\n")
	f := filepath.Join(t.TempDir(), "capture.txt")
	require.NoError(t, os.WriteFile(f, []byte(b.String()), 0o600))
	return f
}

func runDocToGate(t *testing.T, page, capture string) (string, error) {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "doc-to-gate.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"TOD_INVARIANTS_PAGE="+page, "TOD_GATE_CAPTURE="+capture, "TOD_GATE_FLOOR=20")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writePage(t *testing.T, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "invariants.md")
	require.NoError(t, os.WriteFile(f, []byte(body), 0o600))
	return f
}

// The whole point: a page CLAIMING a mechanism that does not exist. One case per shape the page
// uses to name one, because the three phantoms that motivated this gate came one of each — only
// SQL002 was a gate id; `scripts/licence-gate.sh` was a path and
// TestAuthFlow_RateLimitedCaller_CreatesNoRows was a test function.
func TestDOC003_APhantomGate_IsReported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		page string
		want string
	}{
		{
			name: "a gate nobody emitted",
			page: "| `NOPE999` | refuses something | landed |",
			want: "names a gate that no gate emitted when it ran",
		},
		{
			name: "a test nobody wrote",
			page: "| `DOC002` | real | \n| `TestNope_DoesNotExist_Anywhere` | named as the mechanism |",
			want: "names a test that no _test.go file defines",
		},
		{
			name: "a script nobody wrote",
			page: "| `DOC002` | real |\n| `scripts/nope-gate.sh` | named as the mechanism |",
			want: "names a repository path that does not exist",
		},
		{
			name: "a page with no gate ids at all",
			page: "# Invariants\n\nProse with no identifiers in it.",
			want: "no gate ids were parsed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := runDocToGate(t, writePage(t, tc.page),
				writeCapture(t, "DOC001", "DOC002", "PIN001"))
			require.Error(t, err, "DOC003 passed over %s:\n%s", tc.name, out)
			require.Contains(t, out, tc.want)
		})
	}
}

// The false-positive half. A gate that fired on well-formed input would be turned off within a week.
//
// It does not drive the checked-in page, deliberately: that page can only be verified against a
// capture from a real run of every gate, and producing one inside a test would mean invoking the
// build from the build. `make doc-to-gate` IS that check, on the real page and the real capture, in
// CI — so the real page is gated by the gate itself rather than by a test pretending to be it.
func TestDOC003_AWellFormedPageAndCapture_Passes(t *testing.T) {
	t.Parallel()

	capture := writeCapture(t, "DOC001", "DOC002", "PIN001")
	page := writePage(t,
		"| `DOC001` | a gate that emitted |\n"+
			"| `DOC002` | another |\n"+
			"| SQL002 | discussed rather than claimed, so written bare |\n"+
			"| `SHA256` | the hash, not a gate |\n"+
			"| `scripts/doc-to-gate.sh` | a file that exists |\n")

	out, err := runDocToGate(t, page, capture)
	require.NoError(t, err, "DOC003 fired on well-formed input:\n%s", out)
	require.Contains(t, out, "each one emitted by a gate that actually ran")
}

// An absent or truncated capture must be a FINDING, never a pass.
//
// Five gates were caught this week reporting green over an empty or truncated input — SQL001 and
// NET001 when their allowances excluded every file, ENV001 on a failed name-scan, LIC001 over a
// partial `go list` and again over zero classified modules. Every one looked exactly like a pass.
// A gate whose entire job is detecting phantoms is the worst possible sixth, because a capture that
// arrived empty makes every real gate on the page look absent — it does not fail quietly, it fails
// loudly and wrongly, and the first false finding is what teaches everybody to ignore the true ones.
func TestDOC003_AnAbsentOrTruncatedCapture_IsAFinding(t *testing.T) {
	t.Parallel()

	page := writePage(t, "| `DOC001` | a real gate |\n")

	t.Run("no capture at all", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "never-written.txt")
		out, err := runDocToGate(t, page, missing)
		require.Error(t, err, "DOC003 passed with no capture to check against:\n%s", out)
		require.Contains(t, out, "does not exist")
	})

	t.Run("an empty capture", func(t *testing.T) {
		t.Parallel()
		empty := filepath.Join(t.TempDir(), "capture.txt")
		require.NoError(t, os.WriteFile(empty, nil, 0o600))
		out, err := runDocToGate(t, page, empty)
		require.Error(t, err, "DOC003 passed over an empty capture:\n%s", out)
		require.Contains(t, out, "below the floor")
	})

	t.Run("a capture below the floor", func(t *testing.T) {
		t.Parallel()
		short := filepath.Join(t.TempDir(), "capture.txt")
		require.NoError(t, os.WriteFile(short,
			[]byte("DOC001     one gate\nDOC002     two\nTestX_Y_Z\n"), 0o600))
		out, err := runDocToGate(t, page, short)
		require.Error(t, err, "DOC003 passed over a truncated capture:\n%s", out)
		require.Contains(t, out, "below the floor")
	})

	t.Run("a capture listing no tests", func(t *testing.T) {
		t.Parallel()
		var b strings.Builder
		for i := range 40 {
			b.WriteString("PAD" + string(rune('A'+i%26)) + "999     padding\n")
		}
		noTests := filepath.Join(t.TempDir(), "capture.txt")
		require.NoError(t, os.WriteFile(noTests, []byte(b.String()), 0o600))
		out, err := runDocToGate(t, writePage(t, "| `PADA999` | a gate |\n"), noTests)
		require.Error(t, err, "DOC003 passed with no tests listed:\n%s", out)
		require.Contains(t, out, "lists no tests at all")
	})
}

// A VACANT gate printed its id to say the code it guards is absent. That is the gate working, not
// the gate missing, so it counts as existing — treating it as absent would report a real gate as a
// phantom, which is the direction that gets a gate deleted.
func TestDOC003_AVacantGate_CountsAsExisting(t *testing.T) {
	t.Parallel()

	capture := writeCapture(t, "DOC001", "DOC002")
	f, err := os.OpenFile(capture, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("\033[33mWEB001    \033[0m no fetch outside web/src/api (no code to check yet)\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	out, err := runDocToGate(t, writePage(t, "| `WEB001` | vacant, but it ran |\n"), capture)
	require.NoError(t, err, "DOC003 reported a vacant gate as a phantom:\n%s", out)
}

// The waiver list is checked in BOTH directions, which is what stops it becoming the place phantoms
// hide: a gate listed as never emitted that DOES emit must leave the list.
func TestDOC003_TheNotEmittedList_IsCheckedBothWays(t *testing.T) {
	t.Parallel()

	// ADR000 is on the list because it is a vacancy check that a healthy tree never triggers.
	// A capture in which it emitted anyway means the list is stale.
	out, err := runDocToGate(t, writePage(t, "| `ADR000` | a waived gate |\n"),
		writeCapture(t, "DOC001", "ADR000"))
	require.Error(t, err, "the waiver list was not checked in the other direction:\n%s", out)
	require.Contains(t, out, "remove it from NOT_EMITTED_BY_CHECK")
}
