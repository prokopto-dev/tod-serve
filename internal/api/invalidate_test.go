package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// timerSubject is one window-moving route, prepared for driving: the world it needs, the request
// that moves the window, and what it moves.
//
// Both registry gates below drive the SAME set, so a new flagged route needs exactly one entry and
// gets both properties from it — that it pushes the invalidation, and that a failed invalidation
// leaves nothing behind. Two driver lists would be two chances to cover a route in one gate and
// not the other.
type timerSubject struct {
	req    request
	target string
	circle core.CircleID
}

func timerWrites() map[api.OperationID]func(*testing.T, *harness) timerSubject {
	return map[api.OperationID]func(*testing.T, *harness) timerSubject{
		api.OpPutCircleTimerOverride: func(t *testing.T, h *harness) timerSubject {
			t.Helper()
			reader, circleID := h.catalogueReader()
			owner := h.seedMember(circleID, authz.RoleOwner)
			id := h.resolveTargetID(reader, "Venril Sathir")
			return timerSubject{
				req: request{
					Method: http.MethodPut,
					Path: api.BasePath + "/circles/" + circleID.String() +
						"/timer-overrides/" + id,
					Session: h.session(owner, true),
					Headers: map[string]string{api.IfMatchHeader: "*"},
					Body: `{"window_kind": "variance", "window_open_offset_seconds": 300,
					        "window_close_offset_seconds": 400}`,
				},
				target: id, circle: circleID,
			}
		},
		api.OpDeleteCircleTimerOverride: func(t *testing.T, h *harness) timerSubject {
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
			return timerSubject{
				req:    request{Method: http.MethodDelete, Path: base, Session: session},
				target: id, circle: circleID,
			}
		},
		api.OpPutRaidTargetTimer: func(t *testing.T, h *harness) timerSubject {
			t.Helper()
			// Driven through the edge like every other route here. It could not be until
			// ADR-0012: `catalogue.manage` is instance-realm and nothing granted it, so this
			// driver used to return early and the assertion below never ran for this route.
			reader, circleID := h.catalogueReader()
			owner := h.seedMember(circleID, authz.RoleOwner)
			h.grantInstance(owner, authz.PermissionCatalogueManage)
			id := h.resolveTargetID(reader, "Venril Sathir")
			return timerSubject{
				req: request{
					Method:  http.MethodPut,
					Path:    api.BasePath + "/raid-targets/" + id + "/timers/blue",
					Session: h.session(owner, true),
					Headers: map[string]string{api.IfMatchHeader: "*"},
					Body: `{"window_kind": "variance", "window_open_offset_seconds": 300,
					        "window_close_offset_seconds": 400}`,
				},
				target: id, circle: circleID,
			}
		},
	}
}

// eachTimerWritingRoute drives fn once per route carrying `InvalidatesTimer`, and fails for a
// flagged route with no driver — in both directions, so neither list can quietly shrink.
func eachTimerWritingRoute(
	t *testing.T, fn func(t *testing.T, h *harness, subject timerSubject, id api.OperationID),
) {
	t.Helper()
	writes := timerWrites()
	flagged := 0
	for _, route := range api.Routes() {
		if !route.InvalidatesTimer {
			continue
		}
		flagged++
		setup, ok := writes[route.ID]
		require.True(t, ok,
			"%s moves a respawn window and this test has no driver for it. A flagged route with "+
				"no driver is a route nobody is checking", route.ID)

		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.seedCatalogue()
			subject := setup(t, h)
			require.NotEmpty(t, subject.target,
				"%s has a driver that drove nothing, so the assertions below would pass over an "+
					"unexercised route", route.ID)
			fn(t, h, subject, route.ID)
		})
	}
	require.Positive(t, flagged, "no route carries InvalidatesTimer; the filter is wrong")
	for id := range writes {
		require.True(t, routeByID(t, id).InvalidatesTimer,
			"%s has a driver here and no longer carries InvalidatesTimer", id)
	}
}

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
	eachTimerWritingRoute(t, func(
		t *testing.T, h *harness, subject timerSubject, id api.OperationID,
	) {
		h.invalidator.reset()
		got := h.do(subject.req)
		require.Equal(t, http.StatusOK, got.Status, got.Body)
		pushed := h.invalidator.recorded()
		require.Len(t, pushed, 1,
			"%s wrote a window and pushed %d invalidations; the board will go on serving the "+
				"old one until the nightly verify job notices", id, len(pushed))
		require.Equal(t, subject.target, pushed[0].Target.String())
	})
}

// TestRouteRegistry_EveryTimerWritingRoute_RollsBackWhenTheInvalidationFails is the mechanism
// behind `TimerPushIsNotTransactional` being closed, and it is the gate ADR-0013 exists for.
//
// Failing the request was never the hard half — that was true before, and it left the crashed
// process uncovered, because a crash produces no response for anybody to act on. What closes it
// is that the window write and its recomputation are ONE transaction, so a push that does not
// happen takes the write with it and there is no state in which the row moved and the projection
// was not told.
//
// So the assertion is about the DATABASE and not about the status code: every row that existed
// before the failed write still exists, unchanged, and nothing new was left behind. It is derived
// from the route registry, so a fourth window-writing route inherits it.
func TestRouteRegistry_EveryTimerWritingRoute_RollsBackWhenTheInvalidationFails(t *testing.T) {
	t.Parallel()
	eachTimerWritingRoute(t, func(
		t *testing.T, h *harness, subject timerSubject, id api.OperationID,
	) {
		before := h.timerFingerprint(t, subject)

		h.invalidator.failWith(errors.New("the projection is unreachable"))
		h.invalidator.reset()
		got := h.do(subject.req)
		require.GreaterOrEqual(t, got.Status, http.StatusInternalServerError,
			"%s reported success while the projection was never told: %s", id, got.Body)
		require.Len(t, h.invalidator.recorded(), 1,
			"%s never reached the push at all, so this ran the wrong experiment: it would pass "+
				"for a route that failed before writing anything", id)

		require.Equal(t, before, h.timerFingerprint(t, subject),
			"%s answered %d and kept what it wrote. The push failed, so nothing recomputed the "+
				"boards behind that window, and nothing ever will until the nightly job", id,
			got.Status)
	})
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
// fan-out from one circle's override, which is why [catalogue.TimerInvalidator] has two methods rather
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
	var invalidator catalogue.TimerInvalidator = h.invalidator
	require.NoError(t, invalidator.OnCatalogueTimerChange(
		t.Context(), h.store.Queries(), "blue", mustTargetID(t, id)))

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

// TestDeleteCircleTimerOverride_AFailedInvalidation_LeavesTheOverrideAndConverges is the property
// that used to distinguish this route from the two PUTs beside it, and no longer does.
//
// A PUT converges on its own: the retry re-writes the same row and re-pushes. A DELETE had nothing
// left to re-delete, so a push that failed once could never be attempted again — the retry
// answered 404 before reaching it, and the board kept serving an override an officer removed until
// the nightly verify job noticed. The handler compensated by pushing on the 404 as well.
//
// Since ADR-0013 the compensation is gone because the asymmetry is: the push runs inside the
// delete's own transaction, so a push that fails takes the DELETE down with it and the row is
// still there for the retry to remove. The invariant is unchanged and is now held by construction
// — **after any non-5xx answer from this route, the projection has been told** — and this asserts
// the mechanism it now rests on rather than the compensation it used to.
func TestDeleteCircleTimerOverride_AFailedInvalidation_LeavesTheOverrideAndConverges(t *testing.T) {
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
		Body:    `{"window_kind": "unknown"}`,
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)

	// The projection is unreachable. Neither half happens, and the caller is told.
	h.invalidator.failWith(errors.New("the projection is unreachable"))
	h.invalidator.reset()
	failed := h.do(request{Method: http.MethodDelete, Path: path, Session: session})
	require.GreaterOrEqual(t, failed.Status, http.StatusInternalServerError,
		"the delete reported success while the board kept the override: %s", failed.Body)

	// **The override is still there.** That is the whole difference: a DELETE that had committed
	// would leave the retry nothing to act on, which is why this route needed a compensating push
	// before the write became transactional. Asserted against the row rather than through a route,
	// because a single override has no GET of its own.
	_, err := h.store.Queries().GetCircleTimerOverride(t.Context(),
		sqlitegen.GetCircleTimerOverrideParams{
			CircleID: circleID.String(), TargetID: id,
		})
	require.NoError(t, err,
		"the row went and the push did not; nothing can now tell the projection")

	// The retry does both, and answers 200 rather than the 404 the old shape gave it.
	h.invalidator.failWith(nil)
	h.invalidator.reset()
	retried := h.do(request{Method: http.MethodDelete, Path: path, Session: session})
	require.Equal(t, http.StatusOK, retried.Status, retried.Body)

	pushed := h.invalidator.recorded()
	require.Len(t, pushed, 1)
	require.Equal(t, id, pushed[0].Target.String())
	require.Equal(t, circleID, pushed[0].Circle)
}

// TestDeleteCircleTimerOverride_ANonExistentOverride_TouchesTheProjectionNotAtAll.
//
// The 404 used to push, deliberately, so that a retry after a failed push could still reach one.
// Inside a transaction that case cannot arise, and the compensation became a recompute for a
// window that did not move — which writes `timer_change` onto a board nothing changed, a small lie
// in the one field that exists to explain why an answer changed.
//
// The invariant still holds, by a different route: a 404 here means the override is not there, and
// whoever removed it recomputed the boards inside the transaction that removed it.
func TestDeleteCircleTimerOverride_ANonExistentOverride_TouchesTheProjectionNotAtAll(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, circleID := h.catalogueReader()
	owner := h.seedMember(circleID, authz.RoleOwner)
	session := h.session(owner, true)
	id := h.resolveTargetID(reader, "Venril Sathir")
	path := api.BasePath + "/circles/" + circleID.String() + "/timer-overrides/" + id

	h.invalidator.reset()
	got := h.do(request{Method: http.MethodDelete, Path: path, Session: session})
	h.requireProblem(got, apierr.CodeNotFound)
	require.Empty(t, h.invalidator.recorded(),
		"nothing moved, so nothing was recomputed and no board was told a window changed")
}

// TestTimerWritingRoutes_EveryTerminalOutcome_LeavesTheProjectionCurrent walks every terminal
// outcome of the circle-scoped pair, which is the sequence the compensating push existed for.
//
// The invariant is unchanged: after any non-5xx answer, no board is serving a window this route
// replaced. What changed is how each outcome satisfies it — a write pushes inside its own
// transaction, and a 404 wrote nothing to push about.
func TestTimerWritingRoutes_EveryTerminalOutcome_LeavesTheProjectionCurrent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, circleID := h.catalogueReader()
	owner := h.seedMember(circleID, authz.RoleOwner)
	session := h.session(owner, true)
	id := h.resolveTargetID(reader, "Venril Sathir")
	base := api.BasePath + "/circles/" + circleID.String() + "/timer-overrides/" + id

	steps := []struct {
		name    string
		method  string
		headers map[string]string
		body    string
		pushes  int
	}{
		{
			name: "create", method: http.MethodPut,
			headers: map[string]string{api.IfMatchHeader: "*"},
			body:    `{"window_kind": "unknown"}`,
			pushes:  1,
		},
		{name: "delete", method: http.MethodDelete, pushes: 1},
		// Nothing to remove and nothing to recompute. Before ADR-0013 this pushed, because the
		// push was the only thing that could rescue a failed one.
		{name: "delete again", method: http.MethodDelete, pushes: 0},
	}
	for _, step := range steps {
		h.invalidator.reset()
		got := h.do(request{
			Method: step.method, Path: base, Session: session,
			Headers: step.headers, Body: step.body,
		})
		require.Less(t, got.Status, http.StatusInternalServerError,
			"%s answered %d: %s", step.name, got.Status, got.Body)
		require.Len(t, h.invalidator.recorded(), step.pushes,
			"%s answered %d and pushed %d invalidations, not %d",
			step.name, got.Status, len(h.invalidator.recorded()), step.pushes)
	}
}

// TestPutCircleTimerOverride_WhenTheProjectionsOwnWriteIsRefused_TheOverrideIsNotThere.
//
// The registry gate above fails the PORT, which proves the rollback for any error the invalidator
// can return. This one fails the DATABASE: the hook issues the write the projection issues — a row
// into `target_state_cache` — with a `status` the enum CHECK refuses, so the error comes back
// through the driver from inside the transaction rather than from a fake.
//
// It is here because those two failures are not the same experiment. A sentinel returned by a fake
// proves the Go control flow; a constraint violation proves that SQLite's own refusal, arriving
// mid-transaction, still takes the timer write with it.
func TestPutCircleTimerOverride_WhenTheProjectionsOwnWriteIsRefused_TheOverrideIsNotThere(
	t *testing.T,
) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, circleID := h.catalogueReader()
	owner := h.seedMember(circleID, authz.RoleOwner)
	id := h.resolveTargetID(reader, "Venril Sathir")

	h.invalidator.observeInside(func(ctx context.Context, q *sqlitegen.Queries) error {
		_, err := q.PutTargetState(ctx, sqlitegen.PutTargetStateParams{
			CircleID: circleID.String(), TargetID: id,
			ComputedAt: int64(fixtureNow),
			// Not one of the six the enum catalogue defines, so `ck_target_state_cache_status`
			// refuses it. This is the projection's own write, refused by the database.
			Status:     "confidently-wrong",
			Confidence: schemaenum.TargetStateConfidenceUnknown,
			CreatedAt:  int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
		return err
	})

	got := h.do(request{
		Method:  http.MethodPut,
		Path:    api.BasePath + "/circles/" + circleID.String() + "/timer-overrides/" + id,
		Session: h.session(owner, true),
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"window_kind": "unknown"}`,
	})
	require.GreaterOrEqual(t, got.Status, http.StatusInternalServerError,
		"the override was reported as written while the projection's write was refused: %s",
		got.Body)

	_, err := h.store.Queries().GetCircleTimerOverride(t.Context(),
		sqlitegen.GetCircleTimerOverrideParams{
			CircleID: circleID.String(), TargetID: id,
		})
	require.True(t, store.IsNotFound(err),
		"the override survived a transaction that could not write the board derived from it")
}

// TestUpdateRaidTarget_OnlyAQuakeFlagFlip_PushesTheInvalidation is the route half of issue #21.
//
// `updateRaidTarget` moves no window, so it carries no `InvalidatesTimer` and the two registry
// gates above do not reach it. It moves something else: `is_quake_target` is copied onto every
// [consensus.Timer] the resolve ladder returns, so flipping it decides whether a kill before the
// latest quake still counts — in every circle on the instance, with nothing appended to the report
// log to say so. Before this the route pushed nothing, and the board and `getTargetState`
// disagreed about the same mob until something unrelated recomputed the row.
//
// Both directions are here for the reason the service-level table cannot cover from the edge: a
// push on every update would fan a recomputation out over the whole instance whenever somebody
// fixed a typo in a mob's name, under the write lock, writing `timer_change` onto boards that did
// not move.
func TestUpdateRaidTarget_OnlyAQuakeFlagFlip_PushesTheInvalidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, circleID := h.catalogueReader()
	owner := h.seedMember(circleID, authz.RoleOwner)
	h.grantInstance(owner, authz.PermissionCatalogueManage)
	session := h.session(owner, true)
	id := h.resolveTargetID(reader, "Lord Nagafen")
	path := api.BasePath + "/raid-targets/" + id

	patch := func(body string) response {
		return h.do(request{
			Method: http.MethodPatch, Path: path, Session: session,
			Headers: map[string]string{api.IfMatchHeader: "*"}, Body: body,
		})
	}
	flagOf := func() int64 {
		row, err := h.store.Queries().GetRaidTarget(t.Context(), id)
		require.NoError(t, err)
		return row.IsQuakeTarget
	}

	// Identity, not derivation. A rename moves no board.
	h.invalidator.reset()
	renamed := patch(`{"zone": "Somewhere Else"}`)
	require.Equal(t, http.StatusOK, renamed.Status, renamed.Body)
	require.Empty(t, h.invalidator.recorded(),
		"a re-zoning recomputed every board on the instance, under the write lock, and wrote "+
			"timer_change onto answers that did not change")

	flipped := flagOf() == 0
	h.invalidator.reset()
	got := patch(fmt.Sprintf(`{"is_quake_target": %t}`, flipped))
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	pushed := h.invalidator.recorded()
	require.Len(t, pushed, 1,
		"the flag moved and the projection was not told, so every circle that has reported this "+
			"target keeps a board derived under the old flag")
	require.Equal(t, "quake_target", pushed[0].Scope,
		"a raid_target is instance-wide and carries no server, so this is neither of the two "+
			"timer fan-outs")
	require.Equal(t, id, pushed[0].Target.String())

	// And the write is the invalidation's to lose. This is the edge's copy of ADR-0013's
	// property: a push that fails takes the flag with it, so the retry finds the old value and
	// does both again — rather than leaving a flipped flag whose boards nothing will recompute.
	was := flagOf()
	h.invalidator.failWith(errors.New("the projection is unreachable"))
	h.invalidator.reset()
	failed := patch(fmt.Sprintf(`{"is_quake_target": %t}`, !flipped))
	require.GreaterOrEqual(t, failed.Status, http.StatusInternalServerError,
		"the flip was reported as written while no board behind it was recomputed: %s", failed.Body)
	require.Len(t, h.invalidator.recorded(), 1,
		"the request failed before it reached the push, so this ran the wrong experiment")
	require.Equal(t, was, flagOf(),
		"the flag kept its new value after the invalidation failed; nothing recomputed the "+
			"boards derived from it and nothing ever will until the nightly job")
}
