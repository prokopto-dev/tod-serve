package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
)

// TestTenancy_CrossCircle_EveryOperationDenies is the load-bearing gate AGENTS.md law 5 names: a
// principal of circle A gets 404 — never 403 — on every circle-scoped operation against circle B.
//
// **It is not load-bearing yet, and this is the honest statement of why.** It walks the route
// registry rather than a hand-written list, so a circle-scoped route added without coverage is a
// red test the moment its handler lands. Today NO circle-scoped route has a handler — they belong
// to milestones that have not landed — so the loop below runs zero times and the test logs that
// rather than reporting a green tick it has not earned.
//
// What IS enforced today is the middleware the loop would exercise:
// TestTenancy_TheMiddleware_AnswersNotFoundAcrossCircles drives a real registry row with a stub
// handler and asserts the 404. When the circle routes land, this test starts covering them without
// anybody having to remember to add them.
func TestTenancy_CrossCircle_EveryOperationDenies(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	member := h.seedMember(mine, authz.RoleOwner)
	token := h.seedToken(member, allScopes()...)

	theirs := h.seedCircle("Theirs")
	h.seedMember(theirs, authz.RoleOwner)

	served := map[api.OperationID]bool{}
	for _, id := range h.server.Registered() {
		served[id] = true
	}

	covered := 0
	for _, route := range api.CircleScopedRoutes() {
		if !served[route.ID] {
			continue
		}
		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			path := strings.ReplaceAll(route.FullPath(), api.CirclePathParam, theirs.String())
			path = fillRemainingPathParams(path)

			got := h.do(request{
				Method: route.Method, Path: path, Token: token,
				Headers: map[string]string{api.IdempotencyKeyHeader: "cross-circle"},
			})
			require.Equal(t, http.StatusNotFound, got.Status,
				"%s answered %d for another circle; a 403 confirms the circle exists",
				route.ID, got.Status)
			require.Equal(t, apierr.CodeNotFound, got.Problem.Code)
		})
		covered++
	}

	total := len(api.CircleScopedRoutes())
	require.Positive(t, total, "the registry holds no circle-scoped routes; the filter is wrong")
	t.Logf("%d of %d circle-scoped operations have a handler and were driven; "+
		"this test is not yet load-bearing coverage of the circle routes", covered, total)
}

// The registry's own promise: the moment a circle-scoped handler is registered, the test above
// covers it. That is only true if every circle-scoped route's path actually carries the parameter
// the test substitutes into.
func TestTenancy_EveryCircleScopedRoute_IsDrivable(t *testing.T) {
	t.Parallel()
	routes := api.CircleScopedRoutes()
	require.Positive(t, len(routes))
	for _, r := range routes {
		require.Contains(t, r.FullPath(), api.CirclePathParam,
			"%s is circle-scoped and its path carries no %s to substitute",
			r.ID, api.CirclePathParam)
		require.NotEqual(t, api.AuthPublic, r.Auth,
			"%s is circle-scoped and public; there would be no principal to compare against", r.ID)
	}
}

// fillRemainingPathParams substitutes a well-formed ULID for every other path parameter, so that a
// 404 is the tenancy answer rather than a parse failure that happens to look like one.
func fillRemainingPathParams(path string) string {
	const placeholder = "01K3TGT8N9M4X0Q7R2VB6C5D1E"
	segments := strings.Split(path, "/")
	for i, s := range segments {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segments[i] = placeholder
		}
	}
	return strings.Join(segments, "/")
}

// allScopes returns every scope in the catalogue, so the tenancy test's principal fails on tenancy
// and never on a scope it happened not to carry.
func allScopes() []authz.Scope {
	out := make([]authz.Scope, 0, len(authz.Scopes()))
	for _, def := range authz.Scopes() {
		out = append(out, def.Key)
	}
	return out
}
