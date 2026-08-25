package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

func reportsPath(id core.CircleID) string { return circlePath(id) + "/tod-reports" }
func todsPath(id core.CircleID) string    { return circlePath(id) + "/tods" }
func quakesPath(id core.CircleID) string  { return circlePath(id) + "/quakes" }

// seedTarget adds a raid target through the catalogue, which is the only thing that writes one.
func (h *harness) seedTarget(name, zone string) catalogue.Target {
	h.t.Helper()
	target, err := h.catalogue.Create(h.t.Context(), catalogue.CreateRequest{
		Name: name, Zone: zone,
		Expansion: schemaenum.RaidTargetExpansionVelious,
		Category:  schemaenum.RaidTargetCategoryNToV,
	})
	require.NoError(h.t, err)
	return target
}

// seedTimer gives a target a variance window on blue, which is the server every seeded circle is
// pinned to. Nothing calls it by default: an unseeded instance is the state this API has to be
// correct in first.
func (h *harness) seedTimer(target catalogue.Target, open, close time.Duration) {
	h.t.Helper()
	openSeconds, closeSeconds := int64(open.Seconds()), int64(close.Seconds())
	_, err := h.catalogue.PutTimer(h.t.Context(), target.ID, core.Server(schemaenum.ServerBlue),
		catalogue.WindowRequest{
			WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
			WindowOpenOffsetSeconds:  &openSeconds,
			WindowCloseOffsetSeconds: &closeSeconds,
			Source:                   "test",
		}, h.invalidator)
	require.NoError(h.t, err)
}

// reportBody is the payload the plugin sends, with the fields it actually parses out of a log line.
func reportBody(target catalogue.Target, diedAt core.Micros) string {
	return fmt.Sprintf(`{"target_id":%q,"server":"blue","died_at":%q,"source":"log_line",
	  "source_line":"[Mon Aug 18 02:14:07 2026] %s has been slain by Tankguy!",
	  "source_character":"Tankguy","log_character":"Tankgal","killed_by_guild":"Riot",
	  "client_clock_offset_seconds":-3}`,
		target.ID.String(), diedAt.String(), target.Name)
}

// TestCreateTodReport_TheOrdinaryPluginRequest_IsAppended is the common case end to end, over real
// SQLite: a parsed log line about a mob whose name carries a backtick.
func TestCreateTodReport_TheOrdinaryPluginRequest_IsAppended(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	token := h.seedToken(h.seedMember(circleID, authz.RoleMember), authz.ScopeTodReport)
	target := h.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	died := fixtureNow.Add(-3 * time.Hour)

	got := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "plugin-1"},
		Body:    reportBody(target, died),
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var body api.TodReportResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &body))
	require.Equal(t, target.ID, body.TargetID)
	require.Equal(t, died, body.DiedAt)
	require.Equal(t, fixtureNow, body.ReportedAt)
	require.Equal(t, fixtureNow, body.AsOf)
	require.Contains(t, body.SourceLine, "Vulak`Aerr", "the backtick survives the round trip")
	require.Equal(t, schemaenum.TodReportKindKill, body.Kind)
	require.Empty(t, got.Header.Get(api.IdempotencyReplayedHeader))
}

// TestCreateTodReport_ARetryWithTheSameKey_ReplaysRatherThanAppending. Idempotency is
// `(membership, key)` and the shared middleware owns it; this asserts the log did not grow.
func TestCreateTodReport_ARetryWithTheSameKey_ReplaysRatherThanAppending(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	token := h.seedToken(h.seedMember(circleID, authz.RoleMember),
		authz.ScopeTodReport, authz.ScopeTodRead)
	target := h.seedTarget("Lord Nagafen", "Nagafen's Lair")
	body := reportBody(target, fixtureNow.Add(-time.Hour))

	req := request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "retry-me"}, Body: body,
	}
	first := h.do(req)
	require.Equal(t, http.StatusOK, first.Status, first.Body)

	second := h.do(req)
	require.Equal(t, http.StatusOK, second.Status, second.Body)
	require.Equal(t, "true", second.Header.Get(api.IdempotencyReplayedHeader))
	require.JSONEq(t, first.Body, second.Body)

	page := listReports(t, h, circleID, token, "")
	require.Len(t, page.Items, 1)
}

// TestCreateTodReport_TheSameKillWithABotchedKey_IsAReplayNotAnError. The natural key is the second
// line of defence: `ux_tod_report_natural` catches what a different header let through, and the
// answer is 200 with the row that already exists.
func TestCreateTodReport_TheSameKillWithABotchedKey_IsAReplayNotAnError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	token := h.seedToken(h.seedMember(circleID, authz.RoleMember),
		authz.ScopeTodReport, authz.ScopeTodRead)
	target := h.seedTarget("Lady Vox", "Permafrost Keep")
	body := reportBody(target, fixtureNow.Add(-time.Hour))

	first := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "key-one"}, Body: body,
	})
	require.Equal(t, http.StatusOK, first.Status, first.Body)

	second := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "key-two"}, Body: body,
	})
	require.Equal(t, http.StatusOK, second.Status, second.Body)
	require.Equal(t, "true", second.Header.Get(api.IdempotencyReplayedHeader),
		"a duplicate is a replay, not an error")

	var a, b api.TodReportResponse
	require.NoError(t, json.Unmarshal([]byte(first.Body), &a))
	require.NoError(t, json.Unmarshal([]byte(second.Body), &b))
	require.Equal(t, a.ID, b.ID)

	page := listReports(t, h, circleID, token, "")
	require.Len(t, page.Items, 1)
}

// TestCreateTodReport_WithNoIdempotencyKey_IsRefused. The header is required on every POST that
// creates domain state, and the registry is what says so.
func TestCreateTodReport_WithNoIdempotencyKey_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	token := h.seedToken(h.seedMember(circleID, authz.RoleMember), authz.ScopeTodReport)
	target := h.seedTarget("Trakanon", "Old Sebilis")

	got := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
		Body: reportBody(target, fixtureNow.Add(-time.Hour)),
	})
	h.requireProblem(got, apierr.CodeIdempotencyKeyRequired)
}

// TestCreateTodReport_EveryDomainRejection_CarriesItsOwnCode, over the wire, because a client
// branches on `code` and the split only helps if each failure keeps its own.
func TestCreateTodReport_EveryDomainRejection_CarriesItsOwnCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	token := h.seedToken(h.seedMember(circleID, authz.RoleMember), authz.ScopeTodReport)
	target := h.seedTarget("Gorenaire", "The Dreadlands")

	cases := []struct {
		name string
		body string
		want apierr.Code
	}{
		{
			"the green destination ticked while playing blue",
			fmt.Sprintf(`{"target_id":%q,"server":"green","died_at":%q}`,
				target.ID.String(), fixtureNow.Add(-time.Hour).String()),
			apierr.CodeServerMismatch,
		},
		{
			"a clock an hour fast",
			fmt.Sprintf(`{"target_id":%q,"server":"blue","died_at":%q}`,
				target.ID.String(), fixtureNow.Add(time.Hour).String()),
			apierr.CodeDiedAtInFuture,
		},
		{
			"a timezone bug a year deep",
			fmt.Sprintf(`{"target_id":%q,"server":"blue","died_at":%q}`,
				target.ID.String(), fixtureNow.Add(-365*24*time.Hour).String()),
			apierr.CodeDiedAtTooOld,
		},
		{
			"a mob nobody has added",
			fmt.Sprintf(`{"target_name":"Kerafyrm","server":"blue","died_at":%q}`,
				fixtureNow.Add(-time.Hour).String()),
			apierr.CodeUnknownTarget,
		},
		{
			"both halves of the target reference",
			fmt.Sprintf(`{"target_id":%q,"target_name":"Gorenaire","server":"blue","died_at":%q}`,
				target.ID.String(), fixtureNow.Add(-time.Hour).String()),
			apierr.CodeValidationFailed,
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := h.do(request{
				Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
				Headers: map[string]string{
					api.IdempotencyKeyHeader: fmt.Sprintf("reject-%d", i),
				},
				Body: tc.body,
			})
			h.requireProblem(got, tc.want)
		})
	}
}

// TestCreateTodReport_ATokenWithoutTheScope_IsInsufficientScopeNotForbidden. The two halves have
// different fixes — ask an officer for the role, versus mint a token that carries the scope — and a
// client that cannot tell them apart retries the one that will never succeed.
func TestCreateTodReport_ATokenWithoutTheScope_IsInsufficientScopeNotForbidden(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	target := h.seedTarget("Severilous", "Emerald Jungle")
	body := reportBody(target, fixtureNow.Add(-time.Hour))
	headers := map[string]string{api.IdempotencyKeyHeader: "scope-check"}

	narrow := h.seedToken(h.seedMember(circleID, authz.RoleMember), authz.ScopeTodRead)
	h.requireProblem(h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: narrow,
		Headers: headers, Body: body,
	}), apierr.CodeInsufficientScope)

	observer := h.seedToken(h.seedMember(circleID, authz.RoleObserver), allScopes()...)
	h.requireProblem(h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: observer,
		Headers: headers, Body: body,
	}), apierr.CodeForbidden)
}

// TestRetractTodReport_AppendsAndTheOriginalStaysVisible, over the wire.
func TestRetractTodReport_AppendsAndTheOriginalStaysVisible(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	member := h.seedMember(circleID, authz.RoleMember)
	token := h.seedToken(member, authz.ScopeTodReport, authz.ScopeTodRead, authz.ScopeTodRetract)
	target := h.seedTarget("Klandicar", "Great Divide")

	created := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "kill"},
		Body:    reportBody(target, fixtureNow.Add(-2*time.Hour)),
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)
	var report api.TodReportResponse
	require.NoError(t, json.Unmarshal([]byte(created.Body), &report))

	path := reportsPath(circleID) + "/" + report.ID.String() + "/retract"
	got := h.do(request{
		Method: http.MethodPost, Path: path, Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "retract"},
		Body:    `{"reason":"wrong mob"}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var body api.RetractionResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &body))
	require.Equal(t, schemaenum.TodReportKindRetraction, body.Retraction.Kind)
	require.Equal(t, report.ID, *body.Retraction.RetractsReportID)
	require.True(t, body.Original.Retracted, "the original stays visible, marked")

	// A second retraction is 409, not a second row.
	second := h.do(request{
		Method: http.MethodPost, Path: path, Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "retract-again"},
		Body:    `{"reason":"again"}`,
	})
	h.requireProblem(second, apierr.CodeAlreadyRetracted)
}

// TestRetractTodReport_SomebodyElsesReport_IsForbiddenNotNotFound. Wrong tenant is 404; right
// tenant and insufficient permission is 403 — canonical §7's exact distinction, at the route.
func TestRetractTodReport_SomebodyElsesReport_IsForbiddenNotNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	author := h.seedToken(h.seedMember(circleID, authz.RoleMember), allScopes()...)
	target := h.seedTarget("Talendor", "Skyfire Mountains")

	created := h.do(request{
		Method: http.MethodPost, Path: reportsPath(circleID), Token: author,
		Headers: map[string]string{api.IdempotencyKeyHeader: "kill"},
		Body:    reportBody(target, fixtureNow.Add(-time.Hour)),
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)
	var report api.TodReportResponse
	require.NoError(t, json.Unmarshal([]byte(created.Body), &report))

	path := reportsPath(circleID) + "/" + report.ID.String() + "/retract"
	stranger := h.seedToken(h.seedMember(circleID, authz.RoleMember), authz.ScopeTodRetract)
	h.requireProblem(h.do(request{
		Method: http.MethodPost, Path: path, Token: stranger,
		Headers: map[string]string{api.IdempotencyKeyHeader: "nope"}, Body: `{}`,
	}), apierr.CodeRetractNotPermitted)

	// An officer holds `tod.retract.any` and the same request succeeds.
	officer := h.seedToken(h.seedMember(circleID, authz.RoleOfficer), authz.ScopeTodRetract)
	ok := h.do(request{
		Method: http.MethodPost, Path: path, Token: officer,
		Headers: map[string]string{api.IdempotencyKeyHeader: "officer"}, Body: `{}`,
	})
	require.Equal(t, http.StatusOK, ok.Status, ok.Body)
}

// listReports reads a page of the log.
func listReports(
	t *testing.T, h *harness, circleID core.CircleID, token core.Secret, query string,
) api.Page[map[string]any] {
	t.Helper()
	got := h.do(request{
		Method: http.MethodGet, Path: reportsPath(circleID) + query, Token: token,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	var page api.Page[map[string]any]
	require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
	return page
}
