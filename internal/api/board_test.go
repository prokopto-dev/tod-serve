package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// TestListTargetStates_AnUnseededInstance_RendersABoardWithNoWindows.
//
// This is the operator's VPS on day one — timer data does not ship — so it is the default case and
// not an edge one. The board still works: every target is on it, a reported one says `no_timer`
// with its `died_at`, and nothing renders a window.
func TestListTargetStates_AnUnseededInstance_RendersABoardWithNoWindows(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	token := h.seedToken(h.seedMember(circleID, authz.RoleMember),
		authz.ScopeTodReport, authz.ScopeTodRead)
	reported := h.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	h.seedTarget("Lord Nagafen", "Nagafen's Lair")

	died := fixtureNow.Add(-4 * time.Hour)
	created := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "kill"},
		Body:    reportBody(reported, died),
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)

	page := board(t, h, circleID, token, "")
	require.Len(t, page.Items, 2)
	require.Equal(t, fixtureNow, page.AsOf)

	entry := entryFor(t, page.Items, reported.Name)
	require.Equal(t, schemaenum.TargetStateStatusNoTimer, entry.Status)
	require.NotNil(t, entry.DiedAt)
	require.Equal(t, died, *entry.DiedAt, "so a client can say 'died 4 hours ago'")
	require.Nil(t, entry.Window.OpenAt, "and must not render a window")
	require.Equal(t, "none", entry.TimerSource)

	silent := entryFor(t, page.Items, "Lord Nagafen")
	require.Equal(t, schemaenum.TargetStateStatusUnknown, silent.Status,
		"no ToD at all is a different answer from no timer")
}

// TestListTargetStates_TheETagAnswers304 is the whole reason the board can poll: the console
// re-reads it every few seconds, and an unchanged board must cost a header exchange rather than a
// body.
func TestListTargetStates_TheETagAnswers304(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	token := h.seedToken(h.seedMember(circleID, authz.RoleMember),
		authz.ScopeTodReport, authz.ScopeTodRead)
	target := h.seedTarget("Lady Vox", "Permafrost Keep")
	h.seedTimer(target, 16*time.Hour, 24*time.Hour)

	first := h.do(request{Method: http.MethodGet, Path: todsPath(circleID), Token: token})
	require.Equal(t, http.StatusOK, first.Status, first.Body)
	etag := first.Header.Get(api.ETagHeader)
	require.NotEmpty(t, etag)

	// The clock moves and `as_of` with it, and the tag does not: it covers the items, not the
	// instant, or every read would produce a new tag and the 304 would be unreachable.
	h.advance(time.Minute)
	unchanged := h.do(request{
		Method: http.MethodGet, Path: todsPath(circleID), Token: token,
		Headers: map[string]string{api.IfNoneMatchHeader: etag},
	})
	require.Equal(t, http.StatusNotModified, unchanged.Status)
	require.Empty(t, unchanged.Body, "a 304 carries no body")
	require.Equal(t, etag, unchanged.Header.Get(api.ETagHeader))
	require.Empty(t, unchanged.Header.Get("Content-Type"),
		"and describes no representation")

	// A report changes the board, so the tag changes with it.
	created := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "kill"},
		Body:    reportBody(target, fixtureNow.Add(-time.Hour)),
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)

	after := h.do(request{
		Method: http.MethodGet, Path: todsPath(circleID), Token: token,
		Headers: map[string]string{api.IfNoneMatchHeader: etag},
	})
	require.Equal(t, http.StatusOK, after.Status, "the board moved; the cached copy is stale")
	require.NotEqual(t, etag, after.Header.Get(api.ETagHeader))
}

// TestGetTargetState_Reporters_AppearOnlyForAttribution. `tod.read.attribution` is what separates
// an observer from a member, and the evidence counts stay visible to both.
func TestGetTargetState_Reporters_AppearOnlyForAttribution(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	member := h.seedMember(circleID, authz.RoleMember)
	token := h.seedToken(member, authz.ScopeTodReport, authz.ScopeTodRead)
	target := h.seedTarget("Trakanon", "Old Sebilis")
	h.seedTimer(target, 3*24*time.Hour, 5*24*time.Hour)

	created := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "kill"},
		Body:    reportBody(target, fixtureNow.Add(-time.Hour)),
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)

	path := todsPath(circleID) + "/" + target.ID.String()
	withAttribution := h.do(request{Method: http.MethodGet, Path: path, Token: token})
	require.Equal(t, http.StatusOK, withAttribution.Status, withAttribution.Body)
	var mine api.TargetStateResponse
	require.NoError(t, json.Unmarshal([]byte(withAttribution.Body), &mine))
	require.True(t, mine.AttributionVisible)
	require.Len(t, mine.Reporters, 1)
	require.Equal(t, member, mine.Reporters[0].MembershipID)
	require.Equal(t, schemaenum.TargetStateStatusPreWindow, mine.Status)
	require.Equal(t, "catalogue", mine.TimerSource)
	require.Equal(t, fixtureNow, mine.AsOf)

	observer := h.seedToken(h.seedMember(circleID, authz.RoleObserver), authz.ScopeTodRead)
	got := h.do(request{Method: http.MethodGet, Path: path, Token: observer})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	// Checked on the raw body, so an omitted field is genuinely absent rather than zero-valued.
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(got.Body), &raw))
	require.NotContains(t, raw, "reporters",
		"an observer sees the state, not the identity of the trackers behind it")
	require.Equal(t, false, raw["attribution_visible"],
		"and the body SAYS it is a refusal rather than leaving it to be inferred")
	evidence, ok := raw["evidence"].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 1, evidence["report_count"],
		"a confidence figure with no denominator is worse than none")
}

// TestGetTargetState_AnOwnerOnATargetNobodyReported_IsNotToldTheyLackAPermission.
//
// The reported bug, at the edge that produced it. `reporters` is omitted for a principal WITHOUT
// `tod.read.attribution` and for a target with no reports, so the console — which read the
// permission off that field's absence — showed an owner on a fresh instance the observer's
// permission copy for a permission they hold. Issue #52.
//
// Both directions, because either one alone passes while the bug is open: the owner below is
// permitted with nothing to show, and the observer beside them is refused with something to hide.
// The assertions are on the RAW body, so an omitted field is genuinely absent rather than
// zero-valued — which is precisely the distinction that went wrong.
func TestGetTargetState_AnOwnerOnATargetNobodyReported_IsNotToldTheyLackAPermission(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	owner := h.seedToken(h.seedMember(circleID, authz.RoleOwner),
		authz.ScopeTodRead, authz.ScopeTodReport)
	observer := h.seedToken(h.seedMember(circleID, authz.RoleObserver), authz.ScopeTodRead)

	quiet := h.seedTarget("Lord Nagafen", "Nagafen's Lair")
	h.seedTimer(quiet, 5*time.Hour, 9*time.Hour)
	loud := h.seedTarget("Lady Vox", "Permafrost Keep")
	h.seedTimer(loud, 16*time.Hour, 24*time.Hour)

	created := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: owner,
		Headers: map[string]string{api.IdempotencyKeyHeader: "kill"},
		Body:    reportBody(loud, fixtureNow.Add(-time.Hour)),
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)

	tests := []struct {
		name        string
		token       core.Secret
		target      catalogue.Target
		wantVisible bool
		wantNamed   bool
	}{
		{"an owner on a target nobody reported is permitted, and empty", owner, quiet, true, false},
		{"an owner on a target with reports is permitted, and named", owner, loud, true, true},
		{"an observer on a target nobody reported is refused", observer, quiet, false, false},
		{"an observer on a target with reports is refused", observer, loud, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := h.do(request{
				Method: http.MethodGet,
				Path:   todsPath(circleID) + "/" + tc.target.ID.String(), Token: tc.token,
			})
			require.Equal(t, http.StatusOK, got.Status, got.Body)

			var raw map[string]any
			require.NoError(t, json.Unmarshal([]byte(got.Body), &raw))
			require.Equal(t, tc.wantVisible, raw["attribution_visible"],
				"the permission travels as its own field, not as the presence of data")
			require.Equal(t, tc.wantNamed, raw["reporters"] != nil,
				"and `reporters` says only how many there are to name")
		})
	}
}

// TestGetTargetState_AnUnknownTarget_IsNotFound, and a malformed id answers the same way: the shape
// of a guess must not tell a prober whether it existed.
func TestGetTargetState_AnUnknownTarget_IsNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	token := h.seedToken(h.seedMember(circleID, authz.RoleMember), authz.ScopeTodRead)

	for _, id := range []string{newID[core.RaidTarget](h).String(), "not-a-ulid"} {
		got := h.do(request{
			Method: http.MethodGet, Path: todsPath(circleID) + "/" + id, Token: token,
		})
		h.requireProblem(got, apierr.CodeNotFound)
	}
}

// TestReportQuake_IsOfficerOnlyAndReachesNoToken.
//
// Two separate rules, and both are load-bearing. A false quake wipes the whole board, which is why
// it is `tod.quake.report` and not `tod.report` — and the registry gives the operation NO scope at
// all, so no personal access token reaches it at any scope. A leaked bot token can append a report
// somebody can retract; it cannot clear a circle's entire board.
//
// It is not step-up, though: `tod.quake.report` is not in the capability floor, so an ordinary
// officer session does it without re-authenticating. A quake is time-critical.
func TestReportQuake_IsOfficerOnlyAndReachesNoToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	target := h.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	memberID := h.seedMember(circleID, authz.RoleMember)
	member := h.seedToken(memberID, allScopes()...)

	// A member has reported a kill, so there is a board to wipe.
	created := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: member,
		Headers: map[string]string{api.IdempotencyKeyHeader: "kill"},
		Body:    reportBody(target, fixtureNow.Add(-6*time.Hour)),
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)

	// An officer's TOKEN, carrying every scope in the catalogue, still does not reach it.
	officerID := h.seedMember(circleID, authz.RoleOfficer)
	h.requireProblem(h.do(request{
		Method: http.MethodPost, Path: quakesPath(circleID),
		Token:   h.seedToken(officerID, allScopes()...),
		Headers: map[string]string{api.IdempotencyKeyHeader: "quake"},
		Body:    `{"note":"felt it"}`,
	}), apierr.CodeSessionRequired)

	// A member's SESSION reaches the route and is refused on the permission, which is the other
	// half of the sentence and a different fix: ask an officer.
	h.requireProblem(h.do(request{
		Method: http.MethodPost, Path: quakesPath(circleID), Session: h.session(memberID, false),
		Headers: map[string]string{api.IdempotencyKeyHeader: "quake"},
		Body:    `{"note":"felt it"}`,
	}), apierr.CodeForbidden)

	got := h.do(request{
		Method: http.MethodPost, Path: quakesPath(circleID), Session: h.session(officerID, false),
		Headers: map[string]string{api.IdempotencyKeyHeader: "quake"},
		Body:    `{"note":"felt it"}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	var quake api.QuakeResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &quake))
	require.Equal(t, fixtureNow, quake.OccurredAt)

	// The quake log reads back, and the board did not grow a phantom kill report.
	log := h.do(request{
		Method: http.MethodGet, Path: quakesPath(circleID), Token: member,
	})
	require.Equal(t, http.StatusOK, log.Status, log.Body)
	var page api.Page[map[string]any]
	require.NoError(t, json.Unmarshal([]byte(log.Body), &page))
	require.Len(t, page.Items, 1)

	reports := listReports(t, h, circleID, member, "")
	require.Len(t, reports.Items, 1, "an earthquake is one event, not sixty kills")
}

// TestListCircleAudit_ReadsTheChainedLog. The rows are written by every state change already; this
// is the read side, and it publishes the chain so a reader can verify it without a second endpoint.
func TestListCircleAudit_ReadsTheChainedLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	owner := h.seedMember(circleID, authz.RoleOwner)
	session := h.session(owner, true)

	// A revocation is a state change that writes an audit row.
	victim := h.seedMember(circleID, authz.RoleMember)
	member := h.do(request{
		Method: http.MethodGet, Path: circlePath(circleID) + "/members/" + victim.String(),
		Session: session,
	})
	require.Equal(t, http.StatusOK, member.Status, member.Body)
	revoked := h.do(request{
		Method:  http.MethodPost,
		Path:    circlePath(circleID) + "/members/" + victim.String() + "/revoke",
		Session: session,
		Headers: map[string]string{api.IfMatchHeader: member.Header.Get(api.ETagHeader)},
		Body:    `{"reason":"left the guild"}`,
	})
	require.Equal(t, http.StatusOK, revoked.Status, revoked.Body)

	got := h.do(request{
		Method: http.MethodGet, Path: circlePath(circleID) + "/audit", Session: session,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var page api.Page[map[string]any]
	require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
	require.NotEmpty(t, page.Items)
	entry := page.Items[0]
	require.Equal(t, "member.revoked", entry["action"])
	require.Equal(t, "membership", entry["entity_type"])
	require.NotEmpty(t, entry["hash"], "the chain is published, so a reader can verify it")
	detail, ok := entry["detail"].(map[string]any)
	require.True(t, ok, "detail is an object, not a string a client has to parse")
	require.Contains(t, detail, "revocation_strength")
}

// TestListCircleAudit_IsSessionAndStepUpOnly. `audit.read` is in the capability floor: no token
// reaches it at any scope, because a bulk export of who did what is exactly what a leaked token
// must not buy.
func TestListCircleAudit_IsSessionAndStepUpOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	owner := h.seedMember(circleID, authz.RoleOwner)
	path := circlePath(circleID) + "/audit"

	h.requireProblem(h.do(request{
		Method: http.MethodGet, Path: path, Token: h.seedToken(owner, allScopes()...),
	}), apierr.CodeSessionRequired)

	h.requireProblem(h.do(request{
		Method: http.MethodGet, Path: path, Session: h.session(owner, false),
	}), apierr.CodeStepUpRequired)

	ok := h.do(request{Method: http.MethodGet, Path: path, Session: h.session(owner, true)})
	require.Equal(t, http.StatusOK, ok.Status, ok.Body)
}

// board reads a page of the board.
func board(
	t *testing.T, h *harness, circleID core.CircleID, token core.Secret, query string,
) api.Page[projection.BoardEntry] {
	t.Helper()
	got := h.do(request{
		Method: http.MethodGet, Path: todsPath(circleID) + query, Token: token,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	var page api.Page[projection.BoardEntry]
	require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
	return page
}

// entryFor finds one target on the board by name.
func entryFor(
	t *testing.T, entries []projection.BoardEntry, name string,
) projection.BoardEntry {
	t.Helper()
	for _, e := range entries {
		if e.Target.Name == name {
			return e
		}
	}
	require.FailNow(t, "target is not on the board", name)
	return projection.BoardEntry{}
}

// TestListTargetStates_TheETag_CoversPaginationNotJustItems.
//
// `next_cursor` and `has_more` are part of what the response asserts, and a page whose items are
// unchanged while its pagination has moved is a different answer. Hashing only the items answered
// `304` to a caller holding `has_more: false` when a second page had appeared — and that caller
// never asks for it, so the new rows stay invisible until something happens to change an item.
//
// The scenario is the day-one one, which is what makes it worth a test: an unseeded instance has no
// windows, so nothing in an item moves with the clock, and a second ToD is exactly the kind of
// event that adds a page without touching the first one.
func TestListTargetStates_TheETag_CoversPaginationNotJustItems(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	token := h.seedToken(h.seedMember(circleID, authz.RoleMember),
		authz.ScopeTodReport, authz.ScopeTodRead)
	// Seeded in this order so the first target's ULID sorts first: with no timers anywhere, the
	// board falls back to target id, and the assertion below is about the FIRST item staying put.
	first := h.seedTarget("Aaa Wyrm", "Temple of Veeshan")
	second := h.seedTarget("Bbb Wyrm", "Temple of Veeshan")

	report := func(target catalogue.Target, key string) {
		t.Helper()
		got := h.do(request{
			Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
			Headers: map[string]string{api.IdempotencyKeyHeader: key},
			Body:    reportBody(target, fixtureNow.Add(-time.Hour)),
		})
		require.Equal(t, http.StatusOK, got.Status, got.Body)
	}

	const query = "?status=no_timer&limit=1"
	report(first, "first")
	before := h.do(request{
		Method: http.MethodGet, Path: todsPath(circleID) + query, Token: token,
	})
	require.Equal(t, http.StatusOK, before.Status, before.Body)
	etag := before.Header.Get(api.ETagHeader)

	var page api.Page[projection.BoardEntry]
	require.NoError(t, json.Unmarshal([]byte(before.Body), &page))
	require.Len(t, page.Items, 1)
	require.False(t, page.HasMore)
	require.Empty(t, page.NextCursor)

	// A second ToD puts a second target into the filtered set. The first page's ONE item is
	// untouched; only `has_more` and `next_cursor` move.
	report(second, "second")
	after := h.do(request{
		Method: http.MethodGet, Path: todsPath(circleID) + query, Token: token,
	})
	require.Equal(t, http.StatusOK, after.Status, after.Body)

	var moved api.Page[projection.BoardEntry]
	require.NoError(t, json.Unmarshal([]byte(after.Body), &moved))
	require.True(t, moved.HasMore, "there is a second page now")
	require.Equal(t, first.ID.String(), moved.NextCursor, "and a cursor that reaches it")
	require.Equal(t, page.Items, moved.Items,
		"the items really are identical, which is what makes the tag load-bearing here")

	require.NotEqual(t, etag, after.Header.Get(api.ETagHeader),
		"the pagination moved, so the representation did")

	conditional := h.do(request{
		Method: http.MethodGet, Path: todsPath(circleID) + query, Token: token,
		Headers: map[string]string{api.IfNoneMatchHeader: etag},
	})
	require.Equal(t, http.StatusOK, conditional.Status,
		"a 304 here would leave the client believing there is no second page, forever")
}
