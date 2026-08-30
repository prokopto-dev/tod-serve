package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// ENV002 — the `.env` gates.
//
// `deploy/env.example` opens with "Never commit the real thing", and until these existed that was a
// wish: nothing in `.gitignore` matched `.env`, so the one file every reader of
// docs/operations/getting-started.md is told to create — holding TOD_TOKEN_PEPPER, TOD_SESSION_KEY
// and TOD_SETUP_TOKEN — was one `git add -A` from the history of a public repository. The pepper
// keys every credential hash in the database, so this is the same rule as `*.db`'s "a committed
// database is a committed credential", applied to the file that makes the database readable.
//
// Three gates, because there are three different ways to get this wrong:
//
//	the ignore rule is missing or too narrow      -> a `.env` shows up in `git status` and gets added
//	a `.env` is already tracked                   -> ignoring it now changes nothing; git keeps it
//	a secret is committed under some OTHER name   -> a runbook pasting a real value into a fenced
//	                                                 block, which is the likely accident here

// secretEnvNames are the variables whose values must never appear in this repository as literals.
// TOD_METRICS_TOKEN is here for the same reason the other three are: it is compared in constant
// time and is the only thing in front of the metrics listener.
var secretEnvNames = []string{
	"TOD_TOKEN_PEPPER",
	"TOD_SESSION_KEY",
	"TOD_SETUP_TOKEN",
	"TOD_METRICS_TOKEN",
}

// secretAssignment matches both spellings an assignment takes in this repository: `NAME=VALUE`,
// which is the dotenv and shell form, and `NAME: VALUE`, which is the Compose form. The compose
// files here are the ones that actually name these variables, so a gate that only understood `=`
// would miss a secret hardcoded where the interpolation belongs — the likeliest way one gets
// committed in a repository whose deployment is two YAML files.
//
// The separator is captured because the two are validated DIFFERENTLY, and they have to be: `=` is
// only ever an assignment, while `NAME: text` is also how English writes a label. This file itself
// contains `services.tod-serve.environment.TOD_TOKEN_PEPPER: required variable` inside a quoted
// error message.
var secretAssignment = regexp.MustCompile(
	`(` + strings.Join(secretEnvNames, "|") + `)(=|:[ \t]*)(\S*)`)

// generatedSecretShape is what `openssl rand -base64 48` produces (64 characters of base64, no
// padding) or a hex token. It is required of the `NAME: VALUE` form ONLY, where an ordinary English
// word would otherwise satisfy a bare length rule.
var generatedSecretShape = regexp.MustCompile(`^(?:[A-Za-z0-9+/]{16,}={0,2}|[0-9a-fA-F]{32,})$`)

// minSecretLength is what separates a generated secret from a fixture. `openssl rand -base64 48`
// produces 64 characters; the existing gate fixtures assign `x`. Sixteen is comfortably between
// them, and a secret short enough to slip past it is one weak enough that the pepper it keys is a
// separate problem.
const minSecretLength = 16

// looksLikeARealSecret reports whether the right-hand side of NAME=VALUE is a value somebody
// generated, as opposed to a placeholder, an interpolation, or a test fixture.
//
// Every exclusion here is a line that exists in this repository today, and dropping one would make
// the gate fire on a green tree — which is how a gate gets switched off.
func looksLikeARealSecret(value string) bool {
	v := strings.Trim(strings.TrimSpace(value), `"'`)
	switch {
	case v == "":
		// `# TOD_METRICS_TOKEN=` in deploy/env.example: the variable named, no value.
		return false
	case strings.HasPrefix(v, "$"):
		// `"$(openssl rand -base64 48)"` in deploy/smoke.sh, and `${VAR:?}` in the compose files.
		return false
	case strings.HasPrefix(v, "CHANGE_ME"):
		// deploy/env.example, deliberately: `serve` refuses these, and
		// TestServe_PlaceholderSecret_Refused reads that file and requires them.
		return false
	case strings.HasPrefix(v, "<"):
		// `<YOUR_TOKEN_PEPPER>` — the placeholder spelling the documentation uses.
		return false
	case len(v) < minSecretLength:
		// `TOD_TOKEN_PEPPER=x`, the ENV001 fixture in deploygates_test.go.
		return false
	}
	return true
}

// isDotenv reports whether a path is a dotenv file rather than the committed example of one.
//
// `deploy/env.example` has no leading dot and never matches. The `.example`, `.sample` and
// `.template` suffixes are spelled out because a project that grows a `.env.example` should not
// have this gate start failing on the day somebody adds it.
func isDotenv(path string) bool {
	base := filepath.Base(path)
	if base != ".env" && !strings.HasPrefix(base, ".env.") {
		return false
	}
	for _, suffix := range []string{".example", ".sample", ".template"} {
		if strings.HasSuffix(base, suffix) {
			return false
		}
	}
	return true
}

// git runs a git command at the repository root and requires it to succeed.
//
// It does NOT skip when git is absent. A gate that goes quiet when its toolchain is missing reports
// success for a run that never happened, which is the failure this whole directory exists to
// prevent.
func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "git %s", strings.Join(args, " "))
	return string(out)
}

// Every spelling of the file the documentation tells an operator to create is ignored — and the
// example it is copied FROM is not.
//
// Both directions matter. An ignore rule broad enough to cover `.env` is one character away from
// hiding `deploy/env.example`, which is the file `TestServe_PlaceholderSecret_Refused` reads and
// the file every runbook points at; losing it from the tree would be a silent regression that this
// gate's positive half would happily report as success.
func TestENV002_EveryDotenvSpelling_IsIgnored(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	tests := []struct {
		path    string
		ignored bool
		why     string
	}{
		{"deploy/.env", true, "the real one: docker compose resolves `.env` from the directory of the first -f file"},
		{".env", true, "the one people create at the repository root because a runbook said `.env`"},
		{"deploy/.env.production", true, "a second environment beside the first"},
		{".env.local", true, "the convention half the ecosystem uses"},
		{"deploy/env.example", false, "shipped, read by a test, and pointed at by every runbook"},
		{".env.example", false, "the dotted spelling of the same thing, if one is ever added"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			cmd := exec.CommandContext(t.Context(), "git", "check-ignore", "-q", "--no-index", tc.path)
			cmd.Dir = root
			err := cmd.Run()
			ignored := err == nil
			require.Equal(t, tc.ignored, ignored,
				"git check-ignore %s: want ignored=%v — %s", tc.path, tc.ignored, tc.why)
		})
	}
}

// No dotenv file is tracked. Adding the ignore rule does nothing for a file git already holds:
// `.gitignore` is consulted for untracked paths only, so a `.env` committed before the rule existed
// stays committed, stays exported in every tarball, and stays in the history for good.
func TestENV002_NoDotenvFile_IsCommitted(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	var found []string
	for _, path := range strings.Split(strings.TrimSpace(git(t, root, "ls-files", "-z")), "\x00") {
		if path != "" && isDotenv(path) {
			found = append(found, path)
		}
	}
	require.Empty(t, found,
		"a dotenv file is tracked: `git rm --cached` it, and rotate every secret in it — "+
			"ignoring it now does not remove it from the history")
}

// No tracked file assigns one of the secret variables a value somebody generated.
//
// This is the one that catches the likely accident: a runbook, a comment or a fixture with a real
// `openssl rand -base64 48` pasted into it. The file need not be named `.env` for the secret in it
// to be just as published.
func TestENV002_NoSecretVariable_IsAssignedARealValue(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	var findings []string
	var scanned int
	for _, path := range strings.Split(strings.TrimSpace(git(t, root, "ls-files", "-z")), "\x00") {
		if path == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			continue // a submodule, or a path this checkout does not materialise
		}
		scanned++
		for _, f := range scanSecrets(string(body)) {
			findings = append(findings, path+": "+f)
		}
	}

	// The search space is asserted, not assumed. A gate reporting no findings over a tree it never
	// read is the exact shape of failure this repository is built against.
	require.Greater(t, scanned, 100, "the scan read %d tracked files; the listing is wrong", scanned)
	require.Empty(t, findings,
		"a generated secret is committed: remove it, and rotate it — it is in the history now")
}

// scanSecrets returns one description per assignment of a secret variable to a real-looking value.
//
// The two separators are held to different standards on purpose. After `=` any non-placeholder
// token of a plausible length is a finding, because nothing writes `NAME=` except an assignment.
// After `:` the value must additionally LOOK generated — base64 or hex — because `NAME: words` is
// how prose writes a label, and a bare length rule there would fire on documentation.
func scanSecrets(body string) []string {
	var out []string
	for _, m := range secretAssignment.FindAllStringSubmatch(body, -1) {
		name, sep, value := m[1], m[2], m[3]
		if !looksLikeARealSecret(value) {
			continue
		}
		trimmed := strings.Trim(strings.TrimSpace(value), `"'`)
		if strings.HasPrefix(sep, ":") && !generatedSecretShape.MatchString(trimmed) {
			continue
		}
		out = append(out, name+" is assigned a "+itoa(len(trimmed))+"-character literal")
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// The scanner fires, and spares every line this repository actually contains.
//
// Without this the gate above is unfalsifiable: it passes on a clean tree and would pass just as
// happily with a heuristic that never matches anything. The excluded rows are not hypothetical —
// each is a real line in deploy/env.example, deploy/smoke.sh or deploygates_test.go, and each was
// the reason one exclusion exists.
func TestENV002_Scanner_FindsARealSecretAndSparesAFixture(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		body  string
		finds bool
	}{
		{"a generated pepper", "TOD_TOKEN_PEPPER=" + strings.Repeat("A", 64) + "\n", true},
		{"a quoted generated key", `TOD_SESSION_KEY="` + strings.Repeat("b", 64) + `"` + "\n", true},
		{"a setup token in a fenced block", "```\nTOD_SETUP_TOKEN=" + strings.Repeat("c", 64) + "\n```\n", true},
		{"a commented-out real secret", "# TOD_SESSION_KEY=" + strings.Repeat("d", 64) + "\n", true},
		// The Compose form. `deploy/compose.yaml` and `compose.local.yaml` name these variables
		// this way, so hardcoding one where the interpolation belongs is the likeliest accident in
		// this repository — and an equals-only matcher walks straight past it.
		{
			"a secret hardcoded in a compose file",
			"    environment:\n      TOD_SESSION_KEY: " + strings.Repeat("e", 64) + "\n", true,
		},
		{
			"a quoted secret in a compose file",
			`      TOD_TOKEN_PEPPER: "` + strings.Repeat("f", 64) + `"` + "\n", true,
		},
		{
			"a hex secret in a compose file",
			"      TOD_METRICS_TOKEN: " + strings.Repeat("a1b2", 12) + "\n", true,
		},

		{"env.example's placeholder", "TOD_TOKEN_PEPPER=CHANGE_ME_generate_with_openssl_rand_base64_48\n", false},
		{"smoke.sh's command substitution", "TOD_SESSION_KEY=\"$(openssl rand -base64 48)\"\n", false},
		{"a compose interpolation", "TOD_TOKEN_PEPPER: ${TOD_TOKEN_PEPPER:?set it}\n", false},
		{"a compose default interpolation", "TOD_SETUP_TOKEN: ${TOD_SETUP_TOKEN:-}\n", false},
		{"prose using a colon as a label", "TOD_TOKEN_PEPPER: required variable\n", false},
		{
			"an error message quoted in a doc",
			"services.tod-serve.environment.TOD_TOKEN_PEPPER: required variable\n", false,
		},
		{"the documentation's placeholder", "TOD_SETUP_TOKEN=<YOUR_SETUP_TOKEN>\n", false},
		{"the ENV001 fixture", "TOD_TOKEN_PEPPER=x\n", false},
		{"env.example's commented metrics token", "# TOD_METRICS_TOKEN=\n", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanSecrets(tc.body)
			if tc.finds {
				require.NotEmpty(t, got, "the scanner missed a real secret")
				return
			}
			require.Empty(t, got, "the scanner fired on a line this repository contains")
		})
	}
}

// isDotenv tells the real thing from the example, in both directions.
func TestENV002_TheExample_IsNotTheThing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want bool
	}{
		{"deploy/.env", true},
		{".env", true},
		{".env.production", true},
		{"deploy/.env.local", true},
		{"deploy/env.example", false},
		{".env.example", false},
		{".env.sample", false},
		{".env.template", false},
		{"internal/api/setup.go", false},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, isDotenv(tc.path))
		})
	}
}
