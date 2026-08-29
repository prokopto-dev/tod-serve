package api_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// apiDesignPath is the normative document the registry is compared against.
const apiDesignPath = "docs/design/02-api-design.md"

// docRoute is one row of an operation table in the API design.
type docRoute struct {
	Method      string
	Path        string
	OperationID string
	Permission  string
	Scope       string
	StepUp      bool
	Line        int
}

// operationTables reads every operation table in the API design.
//
// Tables are found by their HEADER SHAPE rather than by a list of headings, so a new section of
// operations is covered the moment somebody writes it rather than the moment somebody remembers to
// add its heading here. A gate with a hand-maintained list of places to look is a gate that stops
// looking at the newest one.
func operationTables(t *testing.T) []docRoute {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	doc, err := canondoc.Load(root + "/" + apiDesignPath)
	require.NoError(t, err)

	var out []docRoute
	tables := 0
	for _, table := range doc.Tables() {
		if len(table.Header) < 5 ||
			table.Header[0] != "Method" || table.Header[1] != "Path" ||
			table.Header[2] != "OperationID" || table.Header[3] != "Permission" ||
			table.Header[4] != "Scope" {
			continue
		}
		tables++
		for _, row := range table.Rows {
			scope := row[4]
			stepUp := strings.Contains(scope, "step-up")
			out = append(out, docRoute{
				Method:      strings.TrimSpace(row[0]),
				Path:        canondoc.Unquote(row[1]),
				OperationID: canondoc.Unquote(row[2]),
				Permission:  row[3],
				Scope:       strings.TrimSpace(strings.ReplaceAll(scope, "step-up", "")),
				StepUp:      stepUp,
				Line:        table.Line,
			})
		}
	}
	require.NotZero(t, tables, "no operation tables were found in %s; the parser is wrong",
		apiDesignPath)
	require.Greater(t, len(out), 40,
		"only %d operations were parsed out of %s; the parser is wrong", len(out), apiDesignPath)
	return out
}

// This is the gate that makes the registry the API surface rather than a description of it. The
// document is normative; the registry is what the middleware, the tenancy test and the OpenAPI
// document are all derived from. Compared in BOTH directions, so neither can grow an operation the
// other has never heard of.
func TestRouteRegistry_MatchesTheAPIDesign(t *testing.T) {
	t.Parallel()
	documented := operationTables(t)

	registered := map[string]api.Route{}
	for _, r := range api.Routes() {
		registered[string(r.ID)] = r
	}

	seen := map[string]bool{}
	for _, want := range documented {
		seen[want.OperationID] = true
		got, ok := registered[want.OperationID]
		if !ok {
			t.Errorf("%s line %d documents %s, which is not in the route registry",
				apiDesignPath, want.Line, want.OperationID)
			continue
		}
		require.Equal(t, want.Method, got.Method, "method of %s", want.OperationID)
		require.Equal(t, want.Path, got.Path, "path of %s", want.OperationID)
		require.Equal(t, want.Permission, permissionCell(got), "permission of %s", want.OperationID)
		require.Equal(t, want.Scope, scopeCell(got), "scope of %s", want.OperationID)
	}
	for id := range registered {
		if !seen[id] {
			t.Errorf("the route registry declares %s, which %s does not document", id, apiDesignPath)
		}
	}
}

// permissionCell renders a route the way the API design's Permission column writes it.
func permissionCell(r api.Route) string {
	switch r.Auth {
	case api.AuthPublic:
		return "public"
	case api.AuthSelf:
		return "self"
	case api.AuthMetricsToken:
		return "`TOD_METRICS_TOKEN`"
	case api.AuthSetupToken:
		return "`TOD_SETUP_TOKEN`"
	default:
		names := make([]string, 0, len(r.Permissions))
		for _, p := range r.Permissions {
			names = append(names, "`"+string(p)+"`")
		}
		return strings.Join(names, " / ")
	}
}

// scopeCell renders a route the way the API design's Scope column writes it, with the `step-up`
// annotation removed — that half is compared against the authz catalogue instead.
func scopeCell(r api.Route) string {
	switch {
	case r.AnyScope:
		return "any"
	case len(r.Scopes) == 0:
		return "—"
	default:
		names := make([]string, 0, len(r.Scopes))
		for _, s := range r.Scopes {
			names = append(names, "`"+string(s)+"`")
		}
		return strings.Join(names, " ")
	}
}

// The `step-up` annotation in the document is compared against internal/authz rather than against
// the registry, because the capability floor has exactly one definition and it is not in either of
// these files. The annotation was inconsistent before this gate existed, which is what an
// unenforced annotation always eventually is.
func TestRouteRegistry_StepUp_MatchesTheCapabilityFloor(t *testing.T) {
	t.Parallel()
	for _, want := range operationTables(t) {
		got, ok := api.Lookup(api.OperationID(want.OperationID))
		require.True(t, ok, "%s is documented and not registered", want.OperationID)
		require.Equal(t, want.StepUp, got.RequiresStepUp(),
			"%s: the document says step-up=%v and authz.RequiresStepUp says %v",
			want.OperationID, want.StepUp, got.RequiresStepUp())
	}
}

// A capability-floor operation that carried a scope would be reachable by a token, which is the one
// thing the floor exists to prevent.
func TestRouteRegistry_EveryStepUpRoute_ReachesNoToken(t *testing.T) {
	t.Parallel()
	for _, r := range api.Routes() {
		if !r.RequiresStepUp() {
			continue
		}
		require.Empty(t, r.Scopes, "%s is in the capability floor and declares scopes", r.ID)
		require.False(t, r.AnyScope, "%s is in the capability floor and accepts any scope", r.ID)
		require.True(t, r.SessionOnly(), "%s is in the capability floor and is not session-only", r.ID)
	}
}

// Every route carrying an instance-realm permission is session-only, and this is derived from the
// registry rather than from a list of the seven that are.
//
// ADR-0012 rests on it: an instance grant belongs to an IDENTITY and a personal access token is
// bound to a membership, so a leaked token must reach none of them. That is true today because no
// scope grants an instance-realm key — which makes `SessionOnly()` true by arithmetic — and this
// is what fails if somebody widens a scope or hangs a scope off one of these routes.
//
// `ops.read` is instance-realm and NOT in the capability floor, so this covers a route
// TestRouteRegistry_EveryStepUpRoute_ReachesNoToken does not: reading diagnostics needs a session
// but not a re-authenticated one.
func TestRouteRegistry_EveryInstanceRealmRoute_IsSessionOnly(t *testing.T) {
	t.Parallel()
	seen := 0
	for _, r := range api.Routes() {
		instanceRealm := false
		for _, p := range r.Permissions {
			if authz.IsInstanceRealm(p) {
				instanceRealm = true
			}
		}
		if !instanceRealm {
			continue
		}
		seen++
		for _, p := range r.Permissions {
			if !authz.IsInstanceRealm(p) {
				continue
			}
			// The catalogue side. `SessionOnly()` reads the ROUTE's declared scopes, so without
			// this a scope widened to grant an instance-realm permission would leave every
			// assertion below green while a token reached the permission.
			require.Empty(t, authz.ScopesFor(p),
				"%s carries %q, which is instance-realm and reachable by a PAT scope", r.ID, p)
		}
		require.Empty(t, r.Scopes, "%s carries an instance-realm permission and declares scopes", r.ID)
		require.False(t, r.AnyScope,
			"%s carries an instance-realm permission and accepts any scope", r.ID)
		require.True(t, r.SessionOnly(),
			"%s carries an instance-realm permission and a token reaches it", r.ID)
		require.False(t, r.CircleScoped,
			"%s carries an instance-realm permission and is circle-scoped; a grant is about the "+
				"whole instance, so a per-circle answer would be two authorization models", r.ID)
	}
	require.Positive(t, seen, "no route carries an instance-realm permission; the filter is wrong")
}

// The other direction — every permission in the catalogue is required by some route, consulted by
// some handler, or expands to ones that are — is
// TestPermissions_EveryPermission_IsRequiredByARouteOrExpandsToOnesThatAre in test/repo. It
// replaces the known-gap list that used to sit here, which held `instance.owner` for as long as
// `instance.owner` granted nothing.

// Every POST that creates domain state requires `Idempotency-Key`. This is the architectural test
// docs/concepts/invariants.md names, over the registry rather than over a list.
func TestRoutes_EveryStateCreatingPost_RequiresIdempotencyKey(t *testing.T) {
	t.Parallel()
	for _, r := range api.Routes() {
		if r.CreatesState {
			require.Equal(t, http.MethodPost, r.Method,
				"%s creates domain state with %s; only POST does that here", r.ID, r.Method)
			require.True(t, r.RequiresIdempotencyKey(),
				"%s creates domain state and does not require Idempotency-Key", r.ID)
			continue
		}
		require.False(t, r.RequiresIdempotencyKey(),
			"%s requires Idempotency-Key and creates no domain state", r.ID)
	}
}

// `If-Match` is required on exactly the operations that overwrite state a previous read supplied.
// A PATCH or PUT without it is a silent lost update, which is discovered by a user rather than by
// the server.
func TestRoutes_EveryOverwritingOperation_RequiresIfMatch(t *testing.T) {
	t.Parallel()
	for _, r := range api.Routes() {
		switch r.Method {
		case http.MethodPatch, http.MethodPut:
			require.True(t, r.IfMatch, "%s overwrites a resource and does not require If-Match", r.ID)
		case http.MethodGet:
			require.False(t, r.IfMatch, "%s is a read and requires If-Match", r.ID)
		}
		if r.IfMatch {
			require.False(t, r.CreatesState,
				"%s both creates state and requires If-Match; a create has nothing to match", r.ID)
		}
	}
}

// `Hidden: true` is permitted only on the operational endpoints and the OAuth callback — canonical
// §7. Everything else is an operation an SDK should generate a method for, and hiding one is how a
// route quietly stops being part of the reviewed contract.
func TestRouteRegistry_Hidden_OnlyTheOperationalEndpointsAndTheCallback(t *testing.T) {
	t.Parallel()
	permitted := []api.OperationID{
		api.OpGetLiveness, api.OpGetReadiness, api.OpGetMetrics, api.OpCompleteAuthorization,
	}
	var hidden []api.OperationID
	for _, r := range api.Routes() {
		if r.Hidden {
			hidden = append(hidden, r.ID)
		}
	}
	slices.Sort(hidden)
	slices.Sort(permitted)
	if diff := cmp.Diff(permitted, hidden); diff != "" {
		t.Errorf("the set of hidden operations is not the permitted one (-permitted +hidden):\n%s", diff)
	}
}

// The declared flag and the path cannot disagree: a route whose path names a circle and whose row
// says it is not circle-scoped would skip the tenancy middleware entirely, which is the one
// mistake the whole registry exists to make impossible.
func TestRouteRegistry_CircleScoped_MatchesThePath(t *testing.T) {
	t.Parallel()
	for _, r := range api.Routes() {
		inPath := strings.Contains(r.Path, api.CirclePathParam)
		require.Equal(t, inPath, r.CircleScoped,
			"%s: path %q names a circle=%v and the registry says circle-scoped=%v",
			r.ID, r.Path, inPath, r.CircleScoped)
	}
}

// No public route resolves a caller-supplied `circle_id`. A pre-authentication route that answers
// differently for a real circle than an unknown one confirms a circle's existence — which is what
// canonical §7 hides, and it would sit outside the invite rate limit besides.
func TestPublicRoutes_ResolveNoCircleFromCallerSuppliedId(t *testing.T) {
	t.Parallel()
	public := api.PublicRoutes()
	require.NotEmpty(t, public, "no public routes; the filter is wrong")

	for _, r := range public {
		require.False(t, r.CircleScoped, "%s is public and circle-scoped", r.ID)
		require.NotContains(t, r.PathParams(), "circle_id",
			"%s is public and takes a circle id in its path", r.ID)
	}
}

// An invite code never reaches the server in a URL path or query: the link carries it in the
// fragment, which no browser transmits, and the two operations that read one take it in a body.
func TestRouteRegistry_NoOperation_TakesAnInviteCodeInTheURL(t *testing.T) {
	t.Parallel()
	for _, r := range api.Routes() {
		for _, param := range r.PathParams() {
			require.NotContains(t, param, "invite_code",
				"%s takes an invite code in its path", r.ID)
			require.NotContains(t, param, "code",
				"%s takes something called %q in its path; an invite code is a bearer credential",
				r.ID, param)
		}
	}
}

// Two operations with the same id, or two rows for one method and path, would make the registry
// ambiguous — and every test that looks a route up by id would silently pick one of them.
func TestRouteRegistry_EveryRoute_IsUnique(t *testing.T) {
	t.Parallel()
	byID := map[api.OperationID]bool{}
	byPath := map[string]api.OperationID{}
	for _, r := range api.Routes() {
		require.False(t, byID[r.ID], "%s appears twice", r.ID)
		byID[r.ID] = true

		key := r.Method + " " + r.FullPath()
		previous, clash := byPath[key]
		require.False(t, clash, "%s and %s both serve %s", previous, r.ID, key)
		byPath[key] = r.ID
	}
}

// `operationId` is lowerCamelCase, explicit, and never renamed: a generated SDK's method names come
// from it, so a rename breaks clients even when the HTTP surface is unchanged.
func TestRouteRegistry_EveryOperationID_IsLowerCamelCase(t *testing.T) {
	t.Parallel()
	for _, r := range api.Routes() {
		id := string(r.ID)
		require.NotEmpty(t, id)
		require.True(t, id[0] >= 'a' && id[0] <= 'z', "%s does not start with a lowercase letter", id)
		for _, c := range id {
			require.True(t,
				(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'),
				"%s contains %q; operation ids are lowerCamelCase and nothing else", id, c)
		}
	}
}

// Every permission a route names is in the catalogue. A route naming a permission that does not
// exist would be unreachable by every role, which is a silent 403 rather than a wiring error.
func TestRouteRegistry_EveryPermissionAndScope_IsInTheCatalogue(t *testing.T) {
	t.Parallel()
	for _, r := range api.Routes() {
		for _, p := range r.Permissions {
			_, ok := authz.LookupPermission(p)
			require.True(t, ok, "%s names permission %s, which is not in the catalogue", r.ID, p)
		}
		for _, s := range r.Scopes {
			_, ok := authz.LookupScope(s)
			require.True(t, ok, "%s names scope %s, which is not in the catalogue", r.ID, s)
		}
		if r.Auth == api.AuthPermission {
			require.NotEmpty(t, r.Permissions,
				"%s is permission-gated and names no permission", r.ID)
		} else {
			require.Empty(t, r.Permissions,
				"%s is %s and names permissions anyway", r.ID, r.Auth)
		}
	}
}

// A scope on a route must be one that actually reaches the route's permission, or the middleware
// would demand a scope that grants nothing.
func TestRouteRegistry_EveryScope_ReachesTheRoutePermission(t *testing.T) {
	t.Parallel()
	for _, r := range api.Routes() {
		for _, s := range r.Scopes {
			granted := authz.GrantedByScopes([]authz.Scope{s})
			if r.Auth == api.AuthSelf {
				continue
			}
			reaches := false
			for _, p := range r.Permissions {
				if granted.Has(p) {
					reaches = true
				}
			}
			require.True(t, reaches,
				"%s declares scope %s, which reaches none of its permissions %v", r.ID, s, r.Permissions)
		}
	}
}

// Only the operational endpoints sit outside `/api/v1`. A container health check and a scrape
// config are configured once and must not need editing when the API version moves.
func TestRouteRegistry_OnlyOperationalEndpoints_AreUnversioned(t *testing.T) {
	t.Parallel()
	var unversioned []api.OperationID
	for _, r := range api.Routes() {
		if !r.Versioned {
			unversioned = append(unversioned, r.ID)
			require.False(t, strings.HasPrefix(r.FullPath(), api.BasePath))
			continue
		}
		require.True(t, strings.HasPrefix(r.FullPath(), api.BasePath+"/"),
			"%s is versioned and does not sit under %s", r.ID, api.BasePath)
	}
	slices.Sort(unversioned)
	require.Equal(t,
		[]api.OperationID{api.OpGetLiveness, api.OpGetMetrics, api.OpGetReadiness},
		unversioned)
}
