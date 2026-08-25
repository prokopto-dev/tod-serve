package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
)

// consoleMarker is what the stub console answers with, so a test can tell which half served a
// request without depending on the real console being built.
const consoleMarker = "<!-- the console -->"

func stubConsole() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Deliberate waiver: a test stub with nothing to do about a short write.
		_, _ = w.Write([]byte(consoleMarker))
	})
}

// **An unknown path under `/api/v1` must still reach the API.**
//
// This is the one direction that bites. Falling through to the console would answer `200` with a
// page of HTML, and a client that asked for JSON would have to parse HTML to discover it had
// failed — which is canonical §7's "never HTTP 200 with an error body", broken by a routing
// decision rather than by a handler.
func TestConsole_AnUnknownAPIPath_IsAProblemAndNotThePage(t *testing.T) {
	t.Parallel()
	h := newHarnessWithConsole(t)

	got := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/no-such-operation"})
	require.Equal(t, http.StatusNotFound, got.Status)
	require.Equal(t, apierr.CodeNotFound, got.Problem.Code)
	require.NotContains(t, got.Body, consoleMarker)
	require.Equal(t, apierr.ContentType, contentTypeOf(got))
}

// And the console owns everything else, including the paths its own router owns.
func TestConsole_AClientRoutedPath_ReachesTheConsole(t *testing.T) {
	t.Parallel()
	h := newHarnessWithConsole(t)

	for _, path := range []string{"/", "/board", "/join", "/invites", "/assets/index-abc123.js"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			got := h.do(request{Method: http.MethodGet, Path: path})
			require.Equal(t, http.StatusOK, got.Status)
			require.Contains(t, got.Body, consoleMarker)
		})
	}
}

// The operational endpoints sit at the ROOT, beside the console's own routes, so there is no
// prefix that separates them. They are listed out of the registry rather than here, which is what
// TestConsole_TheAPIPrefixSet_ComesFromTheRegistry closes.
func TestConsole_TheOperationalEndpoints_StillReachTheAPI(t *testing.T) {
	t.Parallel()
	h := newHarnessWithConsole(t)

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			got := h.do(request{Method: http.MethodGet, Path: path})
			require.NotContains(t, got.Body, consoleMarker,
				"%s was served by the console; a load balancer would read a page of HTML as healthy",
				path)
		})
	}
}

// A path that merely STARTS with the same letters is the console's.
//
// The split is a prefix test and a naive one matches `/readyz-please` and `/api/v1x`, which would
// hand a console URL to the API and answer a problem document for a page.
func TestConsole_APathThatMerelyStartsTheSame_IsTheConsoles(t *testing.T) {
	t.Parallel()
	h := newHarnessWithConsole(t)

	for _, path := range []string{"/readyzz", "/healthzed", "/api/v1x/meta", "/apiv1/meta"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			got := h.do(request{Method: http.MethodGet, Path: path})
			require.Equal(t, http.StatusOK, got.Status)
			require.Contains(t, got.Body, consoleMarker)
		})
	}
}

// The set of paths that reach the API is DERIVED from the route registry, so an operational
// endpoint added later is covered without anybody remembering internal/api/console.go.
//
// It is asserted behaviourally — every unversioned route is driven and must not be answered by the
// console — rather than by comparing two lists, which would be the same derivation twice.
func TestConsole_TheAPIPrefixSet_ComesFromTheRegistry(t *testing.T) {
	t.Parallel()
	h := newHarnessWithConsole(t)

	unversioned := 0
	for _, route := range api.Routes() {
		if route.Versioned {
			continue
		}
		unversioned++
		got := h.do(request{Method: route.Method, Path: route.Path, Metrics: route.Auth == api.AuthMetricsToken})
		require.NotContainsf(t, got.Body, consoleMarker,
			"%s is an unversioned route and the console answered it. The prefix set in "+
				"console.go is not reading the registry", route.ID)
	}
	require.Positive(t, unversioned, "the registry holds no unversioned routes; the filter is wrong")

	// And every versioned route is under one prefix, so [BasePath] covers them all.
	for _, route := range api.Routes() {
		if !route.Versioned {
			continue
		}
		require.Truef(t, strings.HasPrefix(route.FullPath(), api.BasePath+"/"),
			"%s is versioned and does not sit under %s, so the console would answer it",
			route.ID, api.BasePath)
	}
}

// A binary with no console serves the API alone and does not grow a catch-all that answers
// everything. Without this, `Handler()` returning the bare API for a nil console is untested and a
// future refactor could quietly install an empty handler instead.
func TestConsole_NoConsole_LeavesTheAPIAlone(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	got := h.do(request{Method: http.MethodGet, Path: "/board"})
	require.Equal(t, http.StatusNotFound, got.Status)
	require.Equal(t, apierr.CodeNotFound, got.Problem.Code)
}

// TestConsole_ServesASelfContainedCSP — the console's own security headers.
//
// They are the binary's, not the proxy's, so that `docker run`, `deploy/compose.local.yaml` and a
// laptop get them too. A header configured on the reverse proxy protects the one deployment
// somebody remembered to configure it on.
//
// The CSP is asserted by DIRECTIVE rather than as one string, so that reordering it or adding a
// directive is not a red test while weakening one is. The claim being pinned is "no external
// origin": every source in it is `'self'`, `'none'`, `data:` or `'unsafe-inline'`, and a policy
// that names a host — a CDN somebody added for a font, an analytics endpoint — fails here.
func TestConsole_ServesASelfContainedCSP(t *testing.T) {
	t.Parallel()
	h := newHarnessWithConsole(t)

	// The middle of the range as well as its ends: the document, a client-routed path, a hashed
	// asset, and a request the console REFUSES. A 405 is still a response a browser renders, and
	// headers attached only on the success path are headers missing exactly where an injected
	// document would be most useful to an attacker.
	paths := []string{"/", "/board", "/join", "/assets/index-abc123.js", "/not-a-page"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				got := h.do(request{Method: method, Path: path})
				require.Equal(t, "nosniff", got.Header.Get("X-Content-Type-Options"),
					"%s %s carries no X-Content-Type-Options", method, path)
				require.Equal(t, "no-referrer", got.Header.Get("Referrer-Policy"),
					"%s %s carries no Referrer-Policy", method, path)

				csp := got.Header.Get("Content-Security-Policy")
				require.NotEmpty(t, csp, "%s %s carries no Content-Security-Policy", method, path)
				directives := parseCSP(t, csp)

				require.Equal(t, []string{"'self'"}, directives["default-src"])
				require.Equal(t, []string{"'none'"}, directives["frame-ancestors"],
					"the console must not be framable; that is the clickjacking answer")
				// Not covered by default-src, so each has to be spelled out or it is simply absent.
				require.Equal(t, []string{"'none'"}, directives["base-uri"])
				require.Equal(t, []string{"'none'"}, directives["object-src"])
				require.Equal(t, []string{"'self'"}, directives["form-action"])

				for directive, sources := range directives {
					for _, source := range sources {
						require.Contains(t,
							[]string{"'self'", "'none'", "data:", "'unsafe-inline'"}, source,
							"%s names %q: the console bundles everything and reaches no external "+
								"host, so a source that is not one of these is an origin nobody "+
								"meant to allow", directive, source)
					}
				}
				// `script-src` is where 'unsafe-inline' would actually matter, and the policy
				// leaves it to default-src precisely so it cannot be relaxed on its own.
				require.NotContains(t, directives["script-src"], "'unsafe-inline'")
				require.NotContains(t, directives["default-src"], "'unsafe-inline'")
			}
		})
	}
}

// The headers belong to the console, and the API answers `application/problem+json` through its own
// middleware chain — so this is also the check that the two halves did not get wired together.
func TestConsole_TheCSP_IsNotAttachedToAPIResponses(t *testing.T) {
	t.Parallel()
	h := newHarnessWithConsole(t)

	got := h.do(request{Method: http.MethodGet, Path: "/healthz"})
	require.Equal(t, http.StatusOK, got.Status)
	require.Empty(t, got.Header.Get("Content-Security-Policy"),
		"a content policy on a JSON response protects nothing and hides where the rule lives")
}

// parseCSP splits a policy into directive -> sources.
func parseCSP(t *testing.T, policy string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, directive := range strings.Split(policy, ";") {
		fields := strings.Fields(directive)
		if len(fields) == 0 {
			continue
		}
		require.NotContains(t, out, fields[0], "%q is declared twice; the first one wins", fields[0])
		out[fields[0]] = fields[1:]
	}
	require.NotEmpty(t, out, "the policy parsed to nothing")
	return out
}
