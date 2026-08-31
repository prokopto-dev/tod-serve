package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/authz"
)

// TestRouteRegistry_ConditionalRead_MatchesWhatTheRouteActuallyDoes is the gate that makes
// `Route.ConditionalRead` mean something.
//
// **The spec tests beside this one cannot do it, and the reason is worth stating.** The document's
// `304` is GENERATED from the flag, so comparing the flag against the document compares a
// derivation against itself: setting the flag on a route that does not revalidate makes both sides
// agree and the check pass. That mutation is exactly how this test came to exist — the spec-only
// version was green for a route flagged at random.
//
// So the second derivation has to be the behaviour. Every ETag-returning GET is driven twice, and
// the flag has to predict which of them answers `304`. A flag set on a route that does not
// revalidate is red here, and so is a route that revalidates without declaring it.
//
// # Every ETag-returning GET now revalidates, and by construction rather than by habit
//
// `getCircle` and `getMember` were recorded here as routes that returned an `ETag`, declared
// `If-None-Match` and never read it — so a client implementing conditional requests against either
// paid for a full body on every read while believing it did not. They are closed, along with the
// two the projection added, by [withConditionalGet]: one middleware turns any `200` whose ETag the
// caller already holds into a `304`, so the behaviour is not something each handler has to
// remember. `getRaidTarget` still branches in its own handler and is left alone — a handler that
// answers `304` itself passes straight through, because the middleware only rewrites a `200`.
//
// That makes the pairing this test asserts total rather than per-route, which
// TestRouteRegistry_EveryETagReturningGET_DeclaresItsConditionalRead states directly: with the
// middleware in the chain, an ETag-returning GET whose row omits `ConditionalRead` emits a `304`
// the document does not describe.
func TestRouteRegistry_ConditionalRead_MatchesWhatTheRouteActuallyDoes(t *testing.T) {
	t.Parallel()

	// Every ETag-returning GET needs a driver, and the map is compared against the registry below
	// so a new one cannot be silently skipped.
	drivers := map[api.OperationID]func(*testing.T, *harness) (string, request){
		api.OpGetRaidTarget: func(t *testing.T, h *harness) (string, request) {
			t.Helper()
			h.seedCatalogue()
			token, _ := h.catalogueReader()
			id := h.resolveTargetID(token, "Vulak`Aerr")
			return api.BasePath + "/raid-targets/" + id, request{Token: token}
		},
		api.OpGetCircle: func(t *testing.T, h *harness) (string, request) {
			t.Helper()
			circleID := h.seedCircle("Riot")
			member := h.seedMember(circleID, authz.RoleMember)
			token := h.seedToken(member, allScopes()...)
			return api.BasePath + "/circles/" + circleID.String(), request{Token: token}
		},
		api.OpGetMember: func(t *testing.T, h *harness) (string, request) {
			t.Helper()
			circleID := h.seedCircle("Riot")
			member := h.seedMember(circleID, authz.RoleMember)
			token := h.seedToken(member, allScopes()...)
			return api.BasePath + "/circles/" + circleID.String() +
				"/members/" + member.String(), request{Token: token}
		},
		api.OpListTargetStates: func(t *testing.T, h *harness) (string, request) {
			t.Helper()
			circleID := h.seedCircle("Riot")
			token := h.seedToken(h.seedMember(circleID, authz.RoleMember), allScopes()...)
			// Driven with a target on the board, so the tag covers something: an empty board is
			// the one shape where two different pages could hash alike for the wrong reason.
			h.seedTarget("Vulak`Aerr", "Temple of Veeshan")
			return api.BasePath + "/circles/" + circleID.String() + "/tods",
				request{Token: token}
		},
		api.OpGetInstanceSettings: func(t *testing.T, h *harness) (string, request) {
			t.Helper()
			// Driven against a REAL instance row and a real grant. Without the row the operation
			// answers 409 and the revalidation below would compare two refusals, which is the
			// "green over nothing" this file exists to refuse.
			h.seedInstance(false)
			session, owner := h.adminSession(t)
			h.grantInstance(owner, authz.PermissionInstanceSecurityManage)
			return api.BasePath + "/admin/instance", request{Session: session}
		},
		api.OpGetTargetState: func(t *testing.T, h *harness) (string, request) {
			t.Helper()
			circleID := h.seedCircle("Riot")
			token := h.seedToken(h.seedMember(circleID, authz.RoleMember), allScopes()...)
			target := h.seedTarget("Lord Nagafen", "Nagafen's Lair")
			return api.BasePath + "/circles/" + circleID.String() + "/tods/" +
				target.ID.String(), request{Token: token}
		},
	}

	driven := 0
	for _, route := range api.Routes() {
		if route.Method != http.MethodGet || !route.ETag || route.Hidden {
			continue
		}
		if !servedByHarness(t, route.ID) {
			continue
		}
		drive, ok := drivers[route.ID]
		require.True(t, ok,
			"%s is an ETag-returning GET with no driver here, so nothing checks whether its "+
				"ConditionalRead flag tells the truth", route.ID)
		driven++

		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			path, base := drive(t, h)

			base.Method, base.Path = http.MethodGet, path
			first := h.do(base)
			require.Equal(t, http.StatusOK, first.Status, first.Body)
			etag := first.Header.Get(api.ETagHeader)
			require.NotEmpty(t, etag, "%s declares ETag: true and returned none", route.ID)

			revalidate := base
			revalidate.Headers = map[string]string{api.IfNoneMatchHeader: etag}
			got := h.do(revalidate)

			if route.ConditionalRead {
				require.Equal(t, http.StatusNotModified, got.Status,
					"%s carries ConditionalRead and answered %d to its own ETag. The document "+
						"declares a 304 nothing emits, and a client waits for a response that "+
						"cannot arrive", route.ID, got.Status)
				require.Empty(t, got.Body, "%s answered 304 with a body", route.ID)

				// ONE SHAPE, for every route. RFC 9110 §15.4.5: a 304 carries the validators and
				// not the representation headers, because there is no representation to describe.
				// This is asserted rather than assumed because there used to be two shapes — a
				// hand-rolled branch in `getRaidTarget` that sent `Content-Type: application/json`
				// with no body, beside the middleware's. Collapsing them is only worth doing if
				// something notices them diverging again, and a caching proxy notices before we do.
				require.Empty(t, got.Header.Get("Content-Type"),
					"%s answered 304 with a Content-Type, which describes a body it did not send",
					route.ID)
				require.Empty(t, got.Header.Get("Content-Length"),
					"%s answered 304 with a Content-Length", route.ID)
				require.Equal(t, etag, got.Header.Get(api.ETagHeader),
					"%s answered 304 without repeating the ETag, so the next revalidation has "+
						"nothing to send", route.ID)
				return
			}
			require.Equal(t, http.StatusOK, got.Status,
				"%s revalidates and does not carry ConditionalRead, so the document describes no "+
					"304 and a generated client treats a real one as an undocumented error",
				route.ID)
		})
	}
	require.Positive(t, driven, "no ETag-returning GET was driven; the filter is wrong")

	for id := range drivers {
		route := routeByID(t, id)
		require.True(t, route.ETag && route.Method == http.MethodGet,
			"%s has a driver here and is no longer an ETag-returning GET", id)
	}
}

// servedByHarness reports whether an operation has a handler in this binary. A registry row with no
// handler cannot be driven, and skipping it here is honest rather than a gap: the tenancy test
// carries the enumerated list of what is unimplemented.
func servedByHarness(t *testing.T, id api.OperationID) bool {
	t.Helper()
	h := newHarness(t)
	for _, served := range h.server.Registered() {
		if served == id {
			return true
		}
	}
	return false
}

// TestRouteRegistry_EveryETagReturningGET_DeclaresItsConditionalRead is the registry-level half of
// the gate above, and it exists because [withConditionalGet] is a MIDDLEWARE rather than a habit.
//
// Revalidation is not opt-in per handler: one middleware turns any `200` whose entity tag the
// caller already holds into a `304`, for every ETag-returning GET in the chain. So the day somebody
// adds such a route and leaves `ConditionalRead` off, the route emits a `304` the document does not
// describe — and a generated client treats it as an undocumented error. The behavioural test above
// only sees routes with a driver; this one sees every row.
//
// The converse is deliberately NOT asserted here: a `ConditionalRead` on something that is not an
// ETag-returning GET is caught by the behavioural test, which drives the route and finds no 304.
func TestRouteRegistry_EveryETagReturningGET_DeclaresItsConditionalRead(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, route := range api.Routes() {
		if route.Method != http.MethodGet || !route.ETag {
			continue
		}
		checked++
		require.True(t, route.ConditionalRead,
			"%s returns an ETag on a GET, so withConditionalGet answers 304 to a caller holding "+
				"it. Without ConditionalRead the document describes no 304 and a generated client "+
				"treats a real one as an undocumented error", route.ID)
	}
	require.Positive(t, checked, "no ETag-returning GET was checked; the filter is wrong")
}
