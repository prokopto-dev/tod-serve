package api_test

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// TestTenancy_CrossCircle_ASubResourceOfAnotherCircle_Is404 drives the OTHER direction of law 5,
// and it is the direction TestTenancy_CrossCircle_EveryOperationDenies cannot reach.
//
// That test puts the OTHER circle's id in the path, so `checkTenancy` refuses the request in the
// middleware before a handler runs, before a query is issued, and before any `WHERE circle_id = ?`
// matters. Every one of its 24 assertions is an assertion about ten lines of middleware. It
// substitutes a fixed placeholder ULID for every other path parameter, so a sub-resource id is
// never a REAL row belonging to anybody.
//
// This drives `/circles/{MINE}/…/{THEIRS}`: the caller's own circle in the path — so the
// middleware passes, correctly — and another circle's real member, report or invite in the
// segment after it. The only thing standing between the caller and that row is the `circle_id` in
// the query's `WHERE`, which is what ADR-0002 says the second gate exists for and what no test in
// this package exercised.
//
// It is derived from the route registry like its sibling, and [foreignSubResources] closes the
// other direction: a circle-scoped route that grows a path parameter naming neither a classified
// circle-scoped resource nor a classified instance-scoped one is a red test, so the next
// sub-resource cannot arrive uncovered.
func TestTenancy_CrossCircle_ASubResourceOfAnotherCircle_Is404(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	world := h.seedTwoCircles(t)

	foreign := world.foreign
	covered := map[string]int{}
	driven := 0

	for _, route := range api.CircleScopedRoutes() {
		if !world.served[route.ID] {
			continue
		}
		params := subResourceParams(route)

		// Both directions. A path parameter nobody classified is a parameter this test would
		// silently skip, which is the shape of "N of 27" that nobody recomputed.
		for _, param := range params {
			_, isForeign := foreign[param]
			require.True(t, isForeign || instanceScopedPathParams()[param],
				"%s takes path parameter %q on a circle-scoped route and it is classified "+
					"neither as a circle-scoped resource in foreignSubResources nor as an "+
					"instance-scoped one in instanceScopedPathParams; classify it, or this test "+
					"skips the very route it was added for", route.ID, param)
		}

		probed := probedParam(params, foreign)
		if probed == "" {
			// Nothing on this path names another circle's row. Its tenancy is the middleware's,
			// and the sibling test is what drives it.
			continue
		}
		covered[probed]++
		driven++

		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			path := crossCirclePath(route, world.mine, foreign, world.targetID)

			// Both credential kinds, because they resolve a circle by two different routes: a PAT
			// through `api_token.membership_id`, a session through the signed cookie's membership.
			// A hole in either is a hole, and a route reachable by only one of them is driven with
			// the one that reaches it.
			for _, cred := range world.credentials(route) {
				got := h.do(request{
					Method: route.Method, Path: path,
					Token: cred.token, Session: cred.session, Body: bodyFor(route),
					Headers: map[string]string{
						api.IdempotencyKeyHeader: string(route.ID) + "-" + cred.name,
						api.IfMatchHeader:        "*",
					},
				})
				require.Equal(t, http.StatusNotFound, got.Status,
					"%s answered %d for another circle's %s presented through the caller's own "+
						"circle, as a %s. The middleware cannot catch this one: the circle in "+
						"the path IS the caller's. Body: %s",
					route.ID, got.Status, probed, cred.name, got.Body)
				require.Equal(t, apierr.CodeNotFound, got.Problem.Code,
					"%s must answer not_found; anything else confirms the row exists", route.ID)
			}
		})
	}

	require.Positive(t, driven,
		"no circle-scoped route was driven with another circle's sub-resource; the filter is "+
			"wrong and this test is asserting nothing")
	// Every classified resource is actually reached by some route. Without this a parameter
	// rename would leave the map populated and the test quietly probing nothing.
	for param := range foreign {
		require.Positive(t, covered[param],
			"%q is classified as another circle's resource and no route was driven with it", param)
	}
	t.Logf("%d circle-scoped operations were driven with another circle's real row: %s",
		driven, renderCoverage(covered))
}

// foreignSubResources maps a path parameter that names a CIRCLE-SCOPED row to the real row this
// test seeds in the other circle.
//
// Membership, report and invite are the three today. Each is a row whose only protection at this
// point in the request is the `circle_id` in its query's `WHERE`: the middleware has already
// passed, because the circle in the path is the caller's own.
type foreignSubResources map[string]string

// instanceScopedPathParams are the path parameters on a circle-scoped route that name something
// EVERY circle shares, so there is no other circle's copy to probe for.
//
// `target_id` is a raid target: a mob's existence is a fact about the game, instance-wide by
// design (canonical §9). `server` is one of three enum values. Substituting a real one keeps the
// answer a tenancy answer rather than a parse failure that happens to look like one.
//
// A circle's OVERRIDE of a target's timer is circle-scoped even though the target is not, and that
// row is probed by TestTenancy_ATimerOverride_OfAnotherCircle_IsInvisible below rather than here,
// because the id in the path names the target and not the override.
func instanceScopedPathParams() map[string]bool {
	return map[string]bool{
		"target_id": true,
		"server":    true,
		// A Discord channel id is DISCORD's identifier, not a row on this instance -- the same
		// kind of thing `server` is. The row it addresses, `circle_discord_channel`, is very much
		// circle-scoped, and it is covered: the two verbs answer DIFFERENTLY and neither answer
		// is the one this loop asserts, so they get a test each rather than a seeded fixture that
		// would have to expect one of them to be wrong.
		//
		// `unbindCircleDiscordChannel` answers `404` for another circle's binding, because its
		// DELETE names `circle_id` in the WHERE and matches nothing --
		// TestUnbindCircleDiscordChannel_AnotherCirclesBinding_Is404.
		// `bindCircleDiscordChannel` answers `409` and NAMES NO CIRCLE, which is what
		// [04-identity section 9] rule 4 requires: silently redirecting a bound channel would move
		// a disclosure decision that a different circle's officer made, and the members reading
		// that channel would be the last to know --
		// TestBindCircleDiscordChannel_AChannelBoundElsewhere_IsRefusedAndNamesNoCircle.
		"discord_channel_id": true,
	}
}

// subResourceParams returns a route's path parameters other than the circle.
func subResourceParams(r api.Route) []string {
	var out []string
	for _, p := range r.PathParams() {
		if p != strings.Trim(api.CirclePathParam, "{}") {
			out = append(out, p)
		}
	}
	return out
}

// probedParam returns the first parameter on the route that names another circle's row, or "".
func probedParam(params []string, foreign foreignSubResources) string {
	for _, p := range params {
		if _, ok := foreign[p]; ok {
			return p
		}
	}
	return ""
}

// crossCirclePath renders the caller's OWN circle with another circle's row after it.
func crossCirclePath(
	r api.Route, mine core.CircleID, foreign foreignSubResources, targetID string,
) string {
	path := strings.ReplaceAll(r.FullPath(), api.CirclePathParam, mine.String())
	segments := strings.Split(path, "/")
	for i, s := range segments {
		if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
			continue
		}
		name := strings.Trim(s, "{}")
		switch {
		case foreign[name] != "":
			segments[i] = foreign[name]
		case name == "target_id":
			segments[i] = targetID
		case name == "server":
			segments[i] = "blue"
		}
	}
	return strings.Join(segments, "/")
}

func renderCoverage(covered map[string]int) string {
	out := make([]string, 0, len(covered))
	for param, n := range covered {
		out = append(out, param)
		_ = n
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// credential is one way of presenting the caller, named so a failure says which one leaked.
type credential struct {
	name    string
	token   core.Secret
	session string
}

// twoCircles is the fixture both cross-circle sub-resource tests run against: the caller's circle,
// another circle with real rows in it, and the instance-wide target both share.
type twoCircles struct {
	mine     core.CircleID
	theirs   core.CircleID
	token    core.Secret
	session  string
	targetID string
	foreign  foreignSubResources
	served   map[api.OperationID]bool
}

// credentials returns the credential kinds that reach this route. A capability-floor route reaches
// no token at any scope, so driving it with one would assert `session_required` rather than
// tenancy — which is a green test that proves nothing about the query.
func (w twoCircles) credentials(r api.Route) []credential {
	out := []credential{{name: "session", session: w.session}}
	if !r.SessionOnly() {
		out = append(out, credential{name: "pat", token: w.token})
	}
	return out
}

// seedTwoCircles builds the caller's circle and another circle holding a real member, a real
// report and a real invite.
//
// The other circle's rows are created THROUGH THE API by that circle's own owner, not inserted
// behind it. A row written by hand is a row that might not be the shape the product writes, and
// the whole question here is whether the product's own reads can be made to return it.
func (h *harness) seedTwoCircles(t *testing.T) twoCircles {
	t.Helper()

	mine := h.seedCircle("Mine")
	mineOwner := h.seedMember(mine, authz.RoleOwner)

	theirs := h.seedCircle("Theirs")
	theirsOwner := h.seedMember(theirs, authz.RoleOwner)
	theirsSession := h.session(theirsOwner, true)
	theirsToken := h.seedToken(theirsOwner, allScopes()...)

	// Instance-wide: one mob, shared by every circle on the instance. It is what makes a report
	// possible in the other circle at all.
	target := h.seedTarget("Vulak`Aerr", "Temple of Veeshan")

	theirsMember := h.seedMember(theirs, authz.RoleMember)

	report := h.do(request{
		Method: http.MethodPost, Path: reportsPath(theirs), Token: theirsToken,
		Headers: map[string]string{api.IdempotencyKeyHeader: "theirs-report"},
		Body:    reportBody(target, fixtureNow.Add(-3*60*60*1_000_000)),
	})
	require.Equal(t, http.StatusOK, report.Status, report.Body)
	var reported api.TodReportResponse
	require.NoError(t, json.Unmarshal([]byte(report.Body), &reported))

	invited := h.do(request{
		Method: http.MethodPost, Path: invitesPath(theirs), Session: theirsSession,
		Headers: map[string]string{api.IdempotencyKeyHeader: "theirs-invite"},
		Body:    `{"role":"member"}`,
	})
	require.Equal(t, http.StatusOK, invited.Status, invited.Body)
	var minted api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(invited.Body), &minted))

	served := map[api.OperationID]bool{}
	for _, id := range h.server.Registered() {
		served[id] = true
	}

	return twoCircles{
		mine: mine, theirs: theirs,
		token:    h.seedToken(mineOwner, allScopes()...),
		session:  h.session(mineOwner, true),
		targetID: target.ID.String(),
		served:   served,
		foreign: foreignSubResources{
			"member_id": theirsMember.String(),
			"report_id": reported.ID.String(),
			"invite_id": minted.ID.String(),
		},
	}
}

// A circle's timer override is circle-scoped and the target it names is not, so the id in the path
// is a legitimate instance-wide one and the middleware has nothing to refuse. What keeps one
// circle's disagreement with the catalogue out of another's is the `circle_id` in
// `circle_timer_override`'s queries, and nothing else.
//
// It is a separate test rather than a row in the loop above because the probe is a different
// shape: the same instance-wide target id has to be reached from BOTH circles, and only one of
// them has a row.
func TestTenancy_ATimerOverride_OfAnotherCircle_IsInvisible(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	mine := h.seedCircle("Mine")
	mineSession := h.session(h.seedMember(mine, authz.RoleOwner), true)
	theirs := h.seedCircle("Theirs")
	theirsSession := h.session(h.seedMember(theirs, authz.RoleOwner), true)

	target := h.seedTarget("Lord Nagafen", "Nagafen's Lair")
	overridePath := func(id core.CircleID) string {
		return circlePath(id) + "/timer-overrides/" + target.ID.String()
	}

	// The other circle disagrees with the catalogue about this mob. That disagreement is
	// competitive intelligence: it says what they believe the window is.
	written := h.do(request{
		Method: http.MethodPut, Path: overridePath(theirs), Session: theirsSession,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body: `{"window_kind":"variance","window_open_offset_seconds":100,
		        "window_close_offset_seconds":200,"note":"ours"}`,
	})
	require.Equal(t, http.StatusOK, written.Status, written.Body)

	listed := h.do(request{
		Method: http.MethodGet, Path: circlePath(mine) + "/timer-overrides",
		Session: mineSession,
	})
	require.Equal(t, http.StatusOK, listed.Status, listed.Body)
	var page api.Page[map[string]any]
	require.NoError(t, json.Unmarshal([]byte(listed.Body), &page))
	require.Empty(t, page.Items,
		"circle Mine listed %d timer overrides and wrote none; the other circle's disagreement "+
			"with the catalogue is theirs", len(page.Items))

	// Deleting through my own circle must not reach their row. A 404 here is the tenancy answer;
	// a 200 would mean one circle silently moved another circle's window.
	deleted := h.do(request{Method: http.MethodDelete, Path: overridePath(mine), Session: mineSession})
	require.Equal(t, http.StatusNotFound, deleted.Status, deleted.Body)
	require.Equal(t, apierr.CodeNotFound, deleted.Problem.Code)

	// And theirs is still there, which is what makes the 404 above a refusal rather than a
	// delete that answered badly.
	stillTheirs := h.do(request{
		Method: http.MethodGet, Path: circlePath(theirs) + "/timer-overrides",
		Session: theirsSession,
	})
	require.Equal(t, http.StatusOK, stillTheirs.Status, stillTheirs.Body)
	var theirPage api.Page[map[string]any]
	require.NoError(t, json.Unmarshal([]byte(stillTheirs.Body), &theirPage))
	require.Len(t, theirPage.Items, 1, "the other circle's override was removed by a 404")
}

// A cross-circle id does not have to arrive in the PATH. It can arrive in a body or a query
// string, where the tenancy middleware never looks: `checkTenancy` reads `circle_id` out of the
// path and compares it, and every other identifier in the request is the handler's problem.
//
// These are the three the API accepts today. Each is a caller-supplied reference to a
// circle-scoped row, on a route whose own circle is the caller's — so the middleware passes and
// the only thing left is whether the read behind it names the tenant.
func TestTenancy_ACrossCircleIDInABodyOrQuery_ReachesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	session := h.session(owner, true)
	token := h.seedToken(owner, allScopes()...)

	theirs := h.seedCircle("Theirs")
	theirsOwner := h.seedMember(theirs, authz.RoleOwner)
	theirsToken := h.seedToken(theirsOwner, allScopes()...)
	target := h.seedTarget("Lady Vox", "Permafrost")

	// A real report in the other circle, so the reporter filter below has something it could
	// wrongly return.
	report := h.do(request{
		Method: http.MethodPost, Path: reportsPath(theirs), Token: theirsToken,
		Headers: map[string]string{api.IdempotencyKeyHeader: "theirs"},
		Body:    reportBody(target, fixtureNow.Add(-2*60*60*1_000_000)),
	})
	require.Equal(t, http.StatusOK, report.Status, report.Body)

	t.Run("createServiceMember owned by another circle's human", func(t *testing.T) {
		t.Parallel()
		// `owner_membership_id` is NOT NULL for a service membership so the audit always names a
		// responsible human. Naming somebody else's human would put a bot in my circle whose
		// answerable person is in a circle I cannot see — and whose revocation, in a circle whose
		// officers have never heard of this bot, would silently kill my token.
		got := h.do(request{
			Method: http.MethodPost, Path: circlePath(mine) + "/service-members",
			Session: session,
			Headers: map[string]string{api.IdempotencyKeyHeader: "cross-owner"},
			Body: `{"display_name":"parser","owner_membership_id":"` +
				theirsOwner.String() + `"}`,
		})
		require.Equal(t, http.StatusUnprocessableEntity, got.Status, got.Body)
		require.Equal(t, apierr.CodeValidationFailed, got.Problem.Code,
			"a bot was accepted with an owner from another circle")
	})

	t.Run("listTodReports filtered by another circle's reporter", func(t *testing.T) {
		t.Parallel()
		// A filter, not a path segment, so a 404 would be the wrong answer: the request is
		// well-formed and the honest reply is that my circle holds no such reports. What must not
		// happen is the other circle's report coming back.
		got := h.do(request{
			Method: http.MethodGet, Token: token,
			Path: reportsPath(mine) + "?reporter_membership_id=" + theirsOwner.String(),
		})
		require.Equal(t, http.StatusOK, got.Status, got.Body)
		var page api.Page[map[string]any]
		require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
		require.Empty(t, page.Items,
			"filtering my circle's report log by another circle's reporter returned %d rows",
			len(page.Items))
	})

	t.Run("retractTodReport of another circle's report", func(t *testing.T) {
		t.Parallel()
		// The report id is in the path here, and it is covered by the loop above. What this adds
		// is the CONSEQUENCE: the other circle's log is unchanged afterwards. A retraction is an
		// append, so a leak here would be a write into somebody else's append-only log.
		before := h.do(request{
			Method: http.MethodGet, Path: reportsPath(theirs), Token: theirsToken,
		})
		require.Equal(t, http.StatusOK, before.Status, before.Body)

		var reported api.TodReportResponse
		require.NoError(t, json.Unmarshal([]byte(report.Body), &reported))
		got := h.do(request{
			Method: http.MethodPost, Token: token,
			Path:    reportsPath(mine) + "/" + reported.ID.String() + "/retract",
			Headers: map[string]string{api.IdempotencyKeyHeader: "cross-retract"},
			Body:    `{"reason":"not mine to retract"}`,
		})
		require.Equal(t, http.StatusNotFound, got.Status, got.Body)

		after := h.do(request{
			Method: http.MethodGet, Path: reportsPath(theirs), Token: theirsToken,
		})
		require.Equal(t, before.Body, after.Body,
			"the other circle's append-only report log changed after a cross-circle retraction")
	})
}
