package api_test

import (
	"net/http"
	"sort"
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
// **It is PARTIALLY load-bearing, and the honest statement of that is [uncoveredCircleRoutes].**
// It walks the route registry rather than a hand-written list, so a circle-scoped route added
// without coverage is a red test the moment its handler lands. The circles, members and invites
// handlers are here; the ToD, quake, audit, event and timer-override handlers are not, and every
// one of them is named below with the milestone that owns it. A green tick over a partial route
// set, reported as "the tenancy gate passes", is exactly the failure this repository is built
// against — so the count is logged and the remainder is enumerated in BOTH directions.
//
// What is additionally enforced is the middleware the loop drives:
// TestTenancy_TheMiddleware_AnswersNotFoundAcrossCircles drives a real registry row with a stub
// handler and asserts the 404 even for a route with no handler at all.
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
	var uncovered []api.OperationID
	for _, route := range api.CircleScopedRoutes() {
		if !served[route.ID] {
			uncovered = append(uncovered, route.ID)
			continue
		}
		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			path := strings.ReplaceAll(route.FullPath(), api.CirclePathParam, theirs.String())
			path = fillRemainingPathParams(path)

			got := h.do(request{
				Method: route.Method, Path: path, Token: token,
				Headers: map[string]string{
					api.IdempotencyKeyHeader: "cross-circle",
					api.IfMatchHeader:        "*",
				},
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

	// Both directions, so the remainder cannot rot. A route that gains a handler and is still
	// listed here is as red as one that loses coverage and is not.
	owners := uncoveredCircleRoutes()
	for _, id := range uncovered {
		require.Contains(t, owners, id,
			"%s is circle-scoped, has no handler, and is not named in uncoveredCircleRoutes; "+
				"a gap nobody wrote down is a gap nobody is tracking", id)
	}
	for id := range owners {
		require.False(t, served[id],
			"%s now has a handler; remove it from uncoveredCircleRoutes so the count is honest",
			id)
	}
	require.Len(t, uncovered, len(owners))

	t.Logf("%d of %d circle-scoped operations have a handler and were driven. "+
		"The remaining %d are UNCOVERED: %s", covered, total, len(uncovered), owners.String())
}

// uncoveredCircleRoutes names every circle-scoped operation with no handler, and who owns it.
//
// It is a map rather than a comment because the test above compares it against the registry in
// both directions: an operation that gains a handler and stays listed here is red, and one that
// loses coverage without being listed is red. That is what stops "N of 27" drifting into a number
// nobody recomputed.
func uncoveredCircleRoutes() coverageGap {
	return coverageGap{
		// Phase 2 — reports, consensus, windows.
		api.OpCreateTodReport:  "Phase 2 (internal/tod)",
		api.OpListTodReports:   "Phase 2 (internal/tod)",
		api.OpGetTodReport:     "Phase 2 (internal/tod)",
		api.OpRetractTodReport: "Phase 2 (internal/tod)",
		api.OpListTargetStates: "Phase 2 (internal/projection)",
		api.OpGetTargetState:   "Phase 2 (internal/projection)",
		api.OpReportQuake:      "Phase 2 (internal/tod)",
		api.OpListQuakes:       "Phase 2 (internal/tod)",
		api.OpListCircleAudit:  "Phase 2 — the rows are already written; this is the read side",

		// Phase 3 — the raid-target catalogue and its per-circle overrides.
		api.OpListCircleTimerOverrides:  "Phase 3 (catalogue)",
		api.OpPutCircleTimerOverride:    "Phase 3 (catalogue)",
		api.OpDeleteCircleTimerOverride: "Phase 3 (catalogue)",

		// Phase 6 — realtime, moved out of Phase 4 deliberately (see ROADMAP.md).
		api.OpSubscribeCircleEvent: "Phase 6 (internal/events)",
		api.OpReplayCircleEvents:   "Phase 6 (internal/events)",
	}
}

// coverageGap maps an uncovered operation to the milestone that owns it.
type coverageGap map[api.OperationID]string

// String renders the gap for the log line, sorted so two runs read the same.
func (g coverageGap) String() string {
	ids := make([]string, 0, len(g))
	for id, owner := range g {
		ids = append(ids, string(id)+" -> "+owner)
	}
	sort.Strings(ids)
	return "\n  " + strings.Join(ids, "\n  ")
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
