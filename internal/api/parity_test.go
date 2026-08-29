package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// consoleCall is one place the console reaches the API, so a failure names the screen rather than
// only the operation.
type consoleCall struct {
	Operation api.OperationID
	File      string
	Line      int
}

// callPattern matches `api.<operationId>(` as the console actually writes it, including across a
// line break — `api\n  .previewInvite({…})` is the ordinary shape of a promise chain and a
// single-line pattern would silently miss most of the call sites, which for a gate is worse than
// missing all of them.
var callPattern = regexp.MustCompile(`\bapi\s*\.\s*([A-Za-z][A-Za-z0-9_]*)\s*\(`)

// TestAPIParity_EveryConsoleRequest_IsReachableWithAScopedToken is the gate that stops the console
// growing a private back door.
//
// **The two sides do not share a derivation.** One side is the console's own source: every
// `api.<operationId>(` under `web/src`, read off disk. The other side is BEHAVIOUR — each of those
// operations is actually driven over HTTP against the wired server with a real personal access
// token, and what the server answers is what decides the verdict. Neither side is computed from
// the other, so the test cannot pass by agreeing with itself.
//
// The verdict, per operation:
//
//   - Answered anything other than 401/403 → a scoped token reaches it. This is the ordinary case
//     and it is what "API-first" means: whatever the console just did, the nParse+ plugin can do.
//   - Answered `403 session_required` → no token reaches it at any scope. That is legitimate only
//     where the REGISTRY already says so, and the registry is not what produced the refusal — the
//     middleware is — so the two sides still do not share a derivation. A session-only operation is
//     not browser-only: a session is a published credential, the document offers the cookie scheme
//     on it, and any client that obtains one reaches it.
//   - Any other 401/403 → **FAIL**. The console can do something a token cannot, for a reason that
//     is not the documented floor. That is a browser-only capability.
//
// The floor has TWO shapes and the check knows both, because the first run of this test found the
// second one:
//
//   - A `permission` route whose permission is in `authz.CapabilityFloor()` — `revokeInvite`,
//     `updateMember`, `listCircleAudit`, the instance-admin four.
//   - A `self` route that alters authentication state and therefore carries no scope at all.
//     `revokeToken` is the whole of this set: cutting off a device is exactly what a stolen token
//     must not be able to do, and a `self` route consults no permission — the resource IS the
//     caller — so there is no catalogue key to look up. Canonical §6 puts `token.revoke` in the
//     floor for the capability; the route expresses it as `AuthSelf` with no scopes.
//
// There is a THIRD shape, and it is checked rather than waved through: a route authorised by
// `TOD_SETUP_TOKEN`. It refuses a PAT with `404` — not `401` or `403` — so the ordinary verdict
// above would have counted it as reachable without a token ever having reached it, which is a pass
// over nothing. Those routes are driven with the credential the DOCUMENT publishes for them
// instead, and both halves are asserted: the setup token works, and the strongest possible PAT
// does not. That is still API-first — `TOD_SETUP_TOKEN` is a published security scheme any client
// can present, so nothing here is browser-only.
func TestAPIParity_EveryConsoleRequest_IsReachableWithAScopedToken(t *testing.T) {
	t.Parallel()
	calls := consoleCalls(t)

	h := newHarness(t)
	h.seedInstance(true)
	circleID := h.seedCircle("Parity")
	owner := h.seedMember(circleID, authz.RoleOwner)
	// Every scope in the catalogue. For a floor operation this is what makes the refusal mean
	// something: it is not "this token was missing a scope", it is "no token reaches this at any
	// scope", which is the capability floor's whole claim.
	strongest := h.seedToken(owner, allScopes()...)

	reachable, floored, setupReachable := 0, 0, 0
	var floorOps []api.OperationID

	for _, call := range calls {
		route, err := api.MustLookup(call.Operation)
		require.NoErrorf(t, err, "%s:%d calls api.%s, which is not in the route registry. "+
			"The console reaches the API only through operations the document publishes",
			call.File, call.Line, call.Operation)

		served := false
		for _, id := range h.server.Registered() {
			if id == call.Operation {
				served = true
			}
		}
		require.Truef(t, served, "%s:%d calls api.%s, which this binary does not serve. "+
			"A console button wired to a route with no handler is a button that 404s",
			call.File, call.Line, call.Operation)

		if route.Auth == api.AuthSetupToken {
			setupReachable++
			requireSetupParity(t, h, route, call, strongest)
			continue
		}

		// Exactly the scopes the document declares for the operation, so a success proves the
		// DECLARED scope set is sufficient — not merely that some token somewhere got through.
		token := strongest
		if !route.SessionOnly() && !route.AnyScope && len(route.Scopes) > 0 {
			token = h.seedToken(owner, route.Scopes...)
		}

		got := h.do(request{
			Method: route.Method,
			Path:   pathFor(route, circleID),
			Token:  token,
			Body:   bodyFor(route),
			Headers: map[string]string{
				api.IdempotencyKeyHeader: "parity-" + string(call.Operation),
				api.IfMatchHeader:        "*",
			},
		})

		if got.Status != http.StatusUnauthorized && got.Status != http.StatusForbidden {
			reachable++
			continue
		}

		require.Equalf(t, apierr.CodeSessionRequired, got.Problem.Code,
			"%s:%d calls api.%s, and a token carrying %v was refused with %q — not the capability "+
				"floor's session_required. Whatever that screen does, no API client can do: it is "+
				"a browser-only capability. Body was: %s",
			call.File, call.Line, call.Operation, scopeStrings(route.Scopes),
			got.Problem.Code, got.Body)

		// The refusal has to be one the registry ALREADY declared, so a route that simply forgot
		// to declare a scope cannot pass as a deliberate floor operation.
		require.Truef(t, route.SessionOnly(),
			"%s:%d calls api.%s. The registry says a token reaches it — scopes %v, any-scope %t — "+
				"and the server refused one anyway. The declaration and the behaviour disagree, "+
				"and the published document is the one that is wrong",
			call.File, call.Line, call.Operation, scopeStrings(route.Scopes), route.AnyScope)

		// A permission route additionally has to name a floor permission, read from the catalogue
		// rather than from the route. A `self` route names no permission at all — the resource IS
		// the caller — so there is nothing to look up and the registry's own session-only
		// declaration above is the whole check.
		if len(route.Permissions) > 0 {
			floor := map[authz.Permission]bool{}
			for _, p := range authz.CapabilityFloor() {
				floor[p] = true
			}
			inFloor := false
			for _, p := range route.Permissions {
				if floor[p] {
					inFloor = true
				}
			}
			require.Truef(t, inFloor,
				"%s:%d calls api.%s, which refuses every token, and none of its permissions %v is "+
					"in the capability floor. Either it belongs in the floor or it is browser-only",
				call.File, call.Line, call.Operation, route.Permissions)
		}

		floored++
		floorOps = append(floorOps, call.Operation)
	}

	t.Logf("the console reaches %d operations: %d with a scoped token, %d needing a browser "+
		"session because they are in the capability floor (%s), %d authorised by TOD_SETUP_TOKEN",
		len(calls), reachable, floored, joinOps(floorOps), setupReachable)
}

// requireSetupParity drives one first-run route both ways.
//
// The PAT half is the one that matters. `checkSetupToken` answers `404` to everything it does not
// recognise, so without this the caller above would score a token-refused route as token-reachable
// and the gate would be green over a request that never got in.
func requireSetupParity(
	t *testing.T, h *harness, route api.Route, call consoleCall, strongest core.Secret,
) {
	t.Helper()
	refused := h.do(request{
		Method: route.Method, Path: pathFor(route, core.CircleID{}), Token: strongest,
		Body: bodyFor(route),
		Headers: map[string]string{
			api.IdempotencyKeyHeader: "parity-pat-" + string(call.Operation),
		},
	})
	require.Equalf(t, http.StatusNotFound, refused.Status,
		"%s:%d calls api.%s, and a personal access token was answered %d rather than the 404 an "+
			"unrecognised setup token gets. A PAT must not reach first-run setup",
		call.File, call.Line, call.Operation, refused.Status)

	reached := h.do(request{
		Method: route.Method, Path: pathFor(route, core.CircleID{}), Token: testSetupTok,
		Body: bodyFor(route),
		Headers: map[string]string{
			api.IdempotencyKeyHeader: "parity-setup-" + string(call.Operation),
		},
	})
	require.NotEqualf(t, http.StatusNotFound, reached.Status,
		"%s:%d calls api.%s, and the credential the document publishes for it was refused. "+
			"Body was: %s", call.File, call.Line, call.Operation, reached.Body)
	require.Lessf(t, reached.Status, http.StatusInternalServerError,
		"%s:%d calls api.%s and the setup token reached a %d. Body was: %s",
		call.File, call.Line, call.Operation, reached.Status, reached.Body)
}

// TestAPIParity_TheExtractor_ActuallyReadsTheConsole is the empty-search-space guard.
//
// A gate reporting success over nothing is exactly what this repository is built against, and this
// one reads a directory that a build could plausibly leave empty.
func TestAPIParity_TheExtractor_ActuallyReadsTheConsole(t *testing.T) {
	t.Parallel()
	calls := consoleCalls(t)
	require.Greater(t, len(calls), 20,
		"the extractor found almost no calls in web/src; the pattern or the root is wrong")

	// And it must have looked at more than one screen: a regex that only matched the first file
	// would satisfy the count above on a busy one.
	files := map[string]bool{}
	for _, call := range calls {
		files[call.File] = true
	}
	require.Greater(t, len(files), 4, "the extractor read only %d files", len(files))
}

// TestAPIParity_TheBoardIsReachableWithATodReadToken drives the plugin's own path end to end.
//
// It is the concrete half of the gate above and it is here because "the board is API-first" is the
// claim the whole console is built on: a token carrying `tod:read` and nothing else reads exactly
// what the board renders, ETag included.
func TestAPIParity_TheBoardIsReachableWithATodReadToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Parity")
	member := h.seedMember(circleID, authz.RoleMember)
	token := h.seedToken(member, authz.ScopeTodRead)

	got := h.do(request{
		Method: http.MethodGet,
		Path:   api.BasePath + "/circles/" + circleID.String() + "/tods",
		Token:  token,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	require.NotEmpty(t, got.Header.Get(api.ETagHeader),
		"the board answered no ETag, so the console's poll would fetch a full body every time")

	// And the revalidation the console actually performs.
	again := h.do(request{
		Method:  http.MethodGet,
		Path:    api.BasePath + "/circles/" + circleID.String() + "/tods",
		Token:   token,
		Headers: map[string]string{"If-None-Match": got.Header.Get(api.ETagHeader)},
	})
	require.Equal(t, http.StatusNotModified, again.Status, again.Body)
}

// consoleCalls reads every `api.<operationId>(` under web/src, skipping web/src/api itself — that
// directory is the client, and its own `send` is not a screen reaching the API.
func consoleCalls(t *testing.T) []consoleCall {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	src := filepath.Join(root, "web", "src")

	var out []consoleCall
	seen := map[api.OperationID]bool{}
	err = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "api" {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".ts" && ext != ".tsx" {
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // a path this walk produced
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		for _, match := range callPattern.FindAllSubmatchIndex(body, -1) {
			name := api.OperationID(body[match[2]:match[3]])
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, consoleCall{
				Operation: name,
				File:      filepath.ToSlash(rel),
				Line:      1 + strings.Count(string(body[:match[0]]), "\n"),
			})
		}
		return nil
	})
	require.NoError(t, err)

	sort.Slice(out, func(i, j int) bool { return out[i].Operation < out[j].Operation })
	return out
}

// pathFor renders a route's path for the parity drive: the caller's own circle, and a well-formed
// ULID for everything else.
//
// A path parameter naming a row that does not exist answers 404, which is not a credential failure
// and therefore still proves the token reached the handler. Seeding a real row for every id in the
// API would make this test a fixture rather than a gate.
func pathFor(route api.Route, circleID core.CircleID) string {
	path := strings.ReplaceAll(route.FullPath(), api.CirclePathParam, circleID.String())
	return fillRemainingPathParams(path)
}

// bodyFor returns a body for the methods that take one. It is deliberately empty: a 422 for a
// missing field is not a credential failure, and filling in a valid body for all 46 operations
// would be a second copy of the API's own validation rules.
func bodyFor(route api.Route) string {
	switch route.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return "{}"
	default:
		return ""
	}
}

func scopeStrings(scopes []authz.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, string(s))
	}
	return out
}

func joinOps(ops []api.OperationID) string {
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, string(op))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
