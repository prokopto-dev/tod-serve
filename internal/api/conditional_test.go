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
// # The routes that do not revalidate, pinned rather than noted
//
// `getCircle` and `getMember` both declare `If-None-Match`, return an `ETag`, and never read the
// header — so a client implementing conditional requests against either pays for a full body on
// every read while believing it does not. Both are pre-existing and belong to routes this
// milestone does not own, so they are recorded here as facts instead of being fixed in a PR that
// would then carry a second concern.
//
// TO CLOSE EITHER: give its output a `Status int`, branch on
// `MatchesIfNoneMatch(in.IfNoneMatch, etag)` exactly as `getRaidTarget` does, and set
// `ConditionalRead: true` on its registry row. This test then flips to asserting the 304 for it,
// with no edit here at all — which is the point of driving the registry rather than a list.
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
