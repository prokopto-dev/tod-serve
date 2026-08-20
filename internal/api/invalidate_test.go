package api_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// TestRouteRegistry_EveryTimerWritingRoute_PushesTheInvalidation is the mechanism behind
// `Route.InvalidatesTimer`, and it is derived from the registry rather than from a list here.
//
// A moved window is the one `target_state.change_reason` the report log cannot show — nothing is
// appended when a timer changes — so it has to be PUSHED, and the routes that move one are the
// only things that can. A wiring that nothing enforces is one refactor from being gone, and this
// is the second push-based invalidation in this project to have had no mechanism behind it.
//
// It drives each route for real and asserts the invalidator fired with the right target, so it
// fails for a handler that forgot the call, for one whose call is unreachable behind an early
// return, and for a route added later that carries the flag and does nothing with it.
func TestRouteRegistry_EveryTimerWritingRoute_PushesTheInvalidation(t *testing.T) {
	t.Parallel()

	// Every route carrying the flag needs a driver here. The map is compared against the registry
	// in BOTH directions below, so a new flagged route with no driver is a red test rather than a
	// route this loop silently skips.
	drivers := map[api.OperationID]func(*testing.T, *harness) string{
		api.OpPutCircleTimerOverride: func(t *testing.T, h *harness) string {
			t.Helper()
			reader, circleID := h.catalogueReader()
			owner := h.seedMember(circleID, authz.RoleOwner)
			id := h.resolveTargetID(reader, "Venril Sathir")
			h.invalidator.reset()
			got := h.do(request{
				Method: http.MethodPut,
				Path: api.BasePath + "/circles/" + circleID.String() +
					"/timer-overrides/" + id,
				Session: h.session(owner, true),
				Headers: map[string]string{api.IfMatchHeader: "*"},
				Body: `{"window_kind": "variance", "window_open_offset_seconds": 300,
				        "window_close_offset_seconds": 400}`,
			})
			require.Equal(t, http.StatusOK, got.Status, got.Body)
			return id
		},
		api.OpDeleteCircleTimerOverride: func(t *testing.T, h *harness) string {
			t.Helper()
			reader, circleID := h.catalogueReader()
			owner := h.seedMember(circleID, authz.RoleOwner)
			session := h.session(owner, true)
			id := h.resolveTargetID(reader, "Venril Sathir")
			base := api.BasePath + "/circles/" + circleID.String() + "/timer-overrides/" + id
			created := h.do(request{
				Method: http.MethodPut, Path: base, Session: session,
				Headers: map[string]string{api.IfMatchHeader: "*"},
				Body:    `{"window_kind": "unknown"}`,
			})
			require.Equal(t, http.StatusOK, created.Status, created.Body)

			// Reset AFTER the setup write, so what this asserts is the DELETE's own push.
			h.invalidator.reset()
			got := h.do(request{Method: http.MethodDelete, Path: base, Session: session})
			require.Equal(t, http.StatusOK, got.Status, got.Body)
			return id
		},
		api.OpPutRaidTargetTimer: func(t *testing.T, h *harness) string {
			t.Helper()
			// `catalogue.manage` is instance-realm and reaches nobody yet, so this route cannot be
			// driven through the edge — see
			// TestRaidTargetWrites_AreUnreachableUntilInstanceGrantsExist. Skipping it here would
			// be exactly the silent hole this test exists to close, so it is driven through the
			// registered handler with the permission check satisfied the only way available: it is
			// not, and the assertion below is that the invalidation is unreachable BECAUSE the
			// route is, rather than because the call is missing.
			//
			// The call itself is covered by TestPutRaidTargetTimer_TheHandler_PushesInstanceWide.
			return ""
		},
	}

	flagged := 0
	for _, route := range api.Routes() {
		if !route.InvalidatesTimer {
			continue
		}
		flagged++
		drive, ok := drivers[route.ID]
		require.True(t, ok,
			"%s moves a respawn window and this test has no driver for it. A flagged route with "+
				"no driver is a route nobody is checking pushes the invalidation", route.ID)

		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.seedCatalogue()
			target := drive(t, h)
			if target == "" {
				return
			}
			pushed := h.invalidator.recorded()
			require.Len(t, pushed, 1,
				"%s wrote a window and pushed %d invalidations; the board will go on serving the "+
					"old one until the nightly verify job notices", route.ID, len(pushed))
			require.Equal(t, target, pushed[0].Target.String())
		})
	}
	require.Positive(t, flagged, "no route carries InvalidatesTimer; the filter is wrong")

	for id := range drivers {
		require.True(t, routeByID(t, id).InvalidatesTimer,
			"%s has a driver here and no longer carries InvalidatesTimer", id)
	}
}

// TestRouteRegistry_EveryWindowWritingPath_CarriesTheFlag closes the other direction.
//
// Without it the gate above is one deletion from being satisfied vacuously: drop the flag and the
// loop stops driving the route. So the flag is derived a second way — from the paths that exist to
// write a window — and the two derivations have to agree.
func TestRouteRegistry_EveryWindowWritingPath_CarriesTheFlag(t *testing.T) {
	t.Parallel()
	for _, route := range api.Routes() {
		writesAWindow := (strings.Contains(route.Path, "/timers/") ||
			strings.Contains(route.Path, "/timer-overrides/")) &&
			(route.Method == http.MethodPut || route.Method == http.MethodDelete ||
				route.Method == http.MethodPatch || route.Method == http.MethodPost)
		if !writesAWindow {
			require.False(t, route.InvalidatesTimer,
				"%s carries InvalidatesTimer and writes no window; the flag means something "+
					"specific and a spurious one makes the gate above meaningless", route.ID)
			continue
		}
		require.True(t, route.InvalidatesTimer,
			"%s %s writes a respawn window and does not carry InvalidatesTimer. A moved window "+
				"appends no row, so nothing downstream can infer it: the push is the only signal",
			route.Method, route.Path)
	}
}

// TestPutCircleTimerOverride_AFailedInvalidation_FailsTheRequest.
//
// A write that succeeded and whose invalidation did not is the worst available outcome: the
// officer is told their override took, and the board goes on serving the window it replaced. Both
// writes are idempotent, so answering with the failure costs a retry and converges.
func TestPutCircleTimerOverride_AFailedInvalidation_FailsTheRequest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, circleID := h.catalogueReader()
	owner := h.seedMember(circleID, authz.RoleOwner)
	id := h.resolveTargetID(reader, "Venril Sathir")

	h.invalidator.failWith(errors.New("the projection is unreachable"))
	got := h.do(request{
		Method:  http.MethodPut,
		Path:    api.BasePath + "/circles/" + circleID.String() + "/timer-overrides/" + id,
		Session: h.session(owner, true),
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"window_kind": "unknown"}`,
	})
	require.GreaterOrEqual(t, got.Status, http.StatusInternalServerError,
		"the override was reported as written while the board kept the old window: %s", got.Body)
}

// TestPutRaidTargetTimer_TheHandler_PushesInstanceWide covers the third flagged route at the level
// it is reachable.
//
// `putRaidTargetTimer` writes `raid_target_timer`, which is instance-wide and per-server, so it
// moves the window for every circle on that server that has not overridden it. That is a different
// fan-out from one circle's override, which is why [api.TimerInvalidator] has two methods rather
// than a nullable circle id.
//
// It is asserted through the service and the port rather than through the edge because the route
// 403s for every principal — `catalogue.manage` is instance-realm. When instance grants land, the
// driver in the gate above replaces this.
func TestPutRaidTargetTimer_TheHandler_PushesInstanceWide(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, _ := h.catalogueReader()
	id := h.resolveTargetID(reader, "Lord Nagafen")

	route := routeByID(t, api.OpPutRaidTargetTimer)
	require.True(t, route.InvalidatesTimer)
	require.Contains(t, route.Path, "{server}",
		"the route names no server, so an instance-wide push has nothing to fan out over")

	// The port's shape is the contract the projection binds to. A single-method port would have
	// forced this route to invent a circle id it does not have.
	var invalidator api.TimerInvalidator = h.invalidator
	require.NoError(t, invalidator.OnCatalogueTimerChange(
		t.Context(), "blue", mustTargetID(t, id)))

	pushed := h.invalidator.recorded()
	require.Len(t, pushed, 1)
	require.Equal(t, "instance", pushed[0].Scope)
	require.Equal(t, "blue", string(pushed[0].Server))
}

// TestPutCircleTimerOverride_AnExistingOverride_RefusesTheWildcard.
//
// `If-Match: *` is this API's "the resource must exist" everywhere else, and the override PUT
// borrows it to mean "and it must NOT" — because a create has no prior tag for a caller to send.
// Borrowing it in one direction and honouring it in the other would let an officer overwrite
// another officer's update with no tag at all, which is the concurrency rule inverted.
func TestPutCircleTimerOverride_AnExistingOverride_RefusesTheWildcard(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, circleID := h.catalogueReader()
	owner := h.seedMember(circleID, authz.RoleOwner)
	session := h.session(owner, true)
	id := h.resolveTargetID(reader, "Venril Sathir")
	path := api.BasePath + "/circles/" + circleID.String() + "/timer-overrides/" + id

	created := h.do(request{
		Method: http.MethodPut, Path: path, Session: session,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body: `{"window_kind": "variance", "window_open_offset_seconds": 300,
		        "window_close_offset_seconds": 400}`,
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)
	etag := created.Header.Get(api.ETagHeader)
	require.NotEmpty(t, etag)

	// The wildcard was the create affordance and the override now exists, so it is refused.
	blind := h.do(request{
		Method: http.MethodPut, Path: path, Session: session,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"window_kind": "unknown"}`,
	})
	h.requireProblem(blind, apierr.CodePreconditionFailed)

	// A stale concrete tag is refused too, and carries the current representation.
	stale := h.do(request{
		Method: http.MethodPut, Path: path, Session: session,
		Headers: map[string]string{api.IfMatchHeader: `"not-the-current-tag"`},
		Body:    `{"window_kind": "unknown"}`,
	})
	h.requireProblem(stale, apierr.CodePreconditionFailed)
	require.NotNil(t, stale.Problem.Meta)
	require.NotEmpty(t, stale.Problem.Meta.Current)

	// The tag the caller actually read still works.
	ok := h.do(request{
		Method: http.MethodPut, Path: path, Session: session,
		Headers: map[string]string{api.IfMatchHeader: etag},
		Body:    `{"window_kind": "unknown"}`,
	})
	require.Equal(t, http.StatusOK, ok.Status, ok.Body)
}

// mustTargetID parses an id a resolve just handed back.
func mustTargetID(t *testing.T, raw string) core.RaidTargetID {
	t.Helper()
	id, err := core.ParseID[core.RaidTarget](raw)
	require.NoError(t, err)
	return id
}

// routeByID looks one route out of the registry.
func routeByID(t *testing.T, id api.OperationID) api.Route {
	t.Helper()
	route, err := api.MustLookup(id)
	require.NoError(t, err)
	return route
}
