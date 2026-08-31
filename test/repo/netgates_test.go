package repo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// NET001 is law 6's mechanism, and it had no test. Every other grep gate in scripts/repo-gates.sh
// is pointed at a deliberately broken fixture somewhere in this directory and required to fire;
// this one was trusted, which is the state AGENTS.md calls a wish rather than a rule.
//
// It matters more than most, because the rule it carries is not "keep the tree tidy". The guarded
// client in internal/identity/outbound resolves a name once, checks every address it got against a
// deny list covering private, link-local, loopback and cloud-metadata ranges, and then dials the
// checked ADDRESS so a DNS rebind has no second lookup to win. A provider package that constructs
// its own `http.Client` gets none of that — and the code still compiles, still passes review at a
// glance, and still works against a well-behaved endpoint. The only thing that notices is this
// grep.
//
// NET001a is the half that says internal/identity/discord and internal/identity/oidc **cannot
// construct a client at all**, which is what makes "they go through the guard" a property of the
// build rather than of everybody's memory.
func TestNET001_AProviderConstructingItsOwnClient_IsReported(t *testing.T) {
	t.Parallel()

	// Every spelling of "I have my own way out", including the ones that do not say `http.Client`.
	spellings := map[string]string{
		"a bare http.Client": `package discord

import "net/http"

var c = &http.Client{}
`,
		"a custom transport, which is the guard removed under a client that looks fine": `package discord

import "net/http"

var t = &http.Transport{}
`,
		"the package-level default, which is every guard skipped at once": `package discord

import "net/http"

func get(u string) (*http.Response, error) { return http.DefaultClient.Get(u) }
`,
		"the convenience helper": `package discord

import "net/http"

func get(u string) (*http.Response, error) { return http.Get(u) }
`,
		"a dialer, which is where the deny list actually lives": `package discord

import "net"

func dial(a string) (net.Conn, error) { return net.Dial("tcp", a) }
`,
	}

	for name, body := range spellings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := goTree(t, "internal/identity/discord/leak.go", body)

			out, err := runGates(t, "TOD_GO_DIRS="+dir)

			require.Error(t, err, "the gate accepted an unguarded client:\n%s", out)
			require.Contains(t, out, "NET001")
			require.Contains(t, out, "leak.go")
		})
	}
}

// The other half: an outbound REQUEST from a package that is not internal/identity. A handler that
// calls a webhook, a job that posts to an API — each of them is a route out of the process that
// the allowlist never sees, because the allowlist is a property of a client this code never built.
func TestNET001_AnOutboundRequestOutsideIdentity_IsReported(t *testing.T) {
	t.Parallel()

	dir := goTree(t, "internal/api/webhook.go", `package api

import (
	"context"
	"net/http"
)

func notify(ctx context.Context, url string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
}
`)

	out, err := runGates(t, "TOD_GO_DIRS="+dir)

	require.Error(t, err, "the gate accepted an outbound request outside internal/identity:\n%s", out)
	require.Contains(t, out, "NET001")
	require.Contains(t, out, "webhook.go")
}

// The passing direction, without which every case above would pass just as well against a gate
// that reported NET001 for any input at all.
//
// The three files are the three allowances, and each is an allowance for a stated reason:
// internal/identity/outbound is the one place a client may be built; internal/probe is law 6's one
// exception, because the image is FROM scratch and the binary has to probe its own listener; and a
// provider package may issue requests through the guarded client it was handed.
func TestNET001_PassesWhatItShould(t *testing.T) {
	t.Parallel()

	dir := goTree(t, "internal/identity/outbound/client.go", `package outbound

import "net/http"

var c = &http.Client{Transport: &http.Transport{}}
`)
	writeGoFile(t, dir, "internal/probe/probe.go", `package probe

import "net/http"

// The one exception, allowed by name: no shell in the image, so the binary probes 127.0.0.1.
var c = &http.Client{}
`)
	writeGoFile(t, dir, "internal/identity/discord/discord.go", `package discord

import (
	"context"
	"net/http"
)

// Issuing a request is fine. It goes through the Doer this package was given, and the header type
// is not a client.
func headers() http.Header { return http.Header{} }

func do(ctx context.Context) error { _ = ctx; return nil }
`)
	// An ordinary package, outside every allowance, so BOTH halves have something to check. The
	// empty-search-space guard caught its absence: NET001b excludes internal/identity entirely,
	// so without this file that half was scanning nothing and would now report rather than pass.
	writeGoFile(t, dir, "internal/circle/circle.go", `package circle

// The overwhelming majority of this repository: a package that reaches no network at all.
func name() string { return "circle" }
`)

	out, err := runGates(t, "TOD_GO_DIRS="+dir)

	require.NoError(t, err, out)
	require.Contains(t, out, "NET001")
	require.NotContains(t, out, "\033[31mNET001")
}

// `http.Head` is a prefix of `http.Header`, and a gate that reported every file constructing a
// header would be switched off within a week. The convenience helpers are matched WITH their
// opening parenthesis for exactly this reason, and this is what holds that true.
func TestNET001_AnHTTPHeader_IsNotAnHTTPRequest(t *testing.T) {
	t.Parallel()

	dir := goTree(t, "internal/api/problem.go", `package api

import "net/http"

func headers() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/problem+json")
	return h
}
`)

	out, err := runGates(t, "TOD_GO_DIRS="+dir)

	require.NoError(t, err, "a package building a header was reported as building a client:\n%s", out)
}

// goTree writes one Go file at a path inside a temporary tree and returns the tree root, ready to
// be handed to the gate as $TOD_GO_DIRS.
//
// The PATH matters as much as the contents: NET001's allowances are directory prefixes, so a
// fixture written at the wrong depth would be testing a different rule.
func goTree(t *testing.T, path, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeGoFile(t, dir, path, body)
	return dir
}

func writeGoFile(t *testing.T, dir, path, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o600))
}

// The gate must not go green over nothing, and this is the case that makes that reachable.
//
// Once the allowances actually exclude — which is what the separator anchoring fixed — a search
// space consisting ONLY of allowed directories leaves no files to grep. `echo "" | xargs grep -ln`
// with no file arguments does not error: grep falls back to reading stdin, finds nothing, and the
// gate reports a pass over zero files.
//
// That is this script's own opening rule violated by the gate itself — "a gate reporting success
// over an empty search space is how a rule quietly stops being enforced" — and it is the same
// shape as a tenancy gate silently dropping queries and still printing a tick. The direction
// matters: reporting permitted files is noisy and fail-CLOSED, while passing over nothing is
// fail-OPEN, and only the second one lets a real violation through unseen.
func TestNET001_AnEmptySearchSpace_IsReportedRatherThanPassed(t *testing.T) {
	t.Parallel()

	// Every file is in a directory the gate allows, so both halves exclude everything they see.
	dir := goTree(t, "internal/identity/outbound/client.go", `package outbound

import "net/http"

var c = &http.Client{Transport: &http.Transport{}}
`)
	writeGoFile(t, dir, "internal/probe/probe.go", `package probe

import "net/http"

var c = &http.Client{}
`)

	out, err := runGates(t, "TOD_GO_DIRS="+dir)

	require.Error(t, err, "NET001 passed over an empty search space:\n%s", out)
	require.Contains(t, out, "NET001")
	require.Contains(t, out, "checked NOTHING")
}
