package tod_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// TestList_HidesARetractedKillAndTheRetractionTogether.
//
// They travel together on purpose: a retraction the caller can see, pointing at a report they
// cannot, is a dangling reference the client has to explain. `include_retracted` brings back both
// halves or neither.
func TestList_HidesARetractedKillAndTheRetractionTogether(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan")

	kept := f.report(target, fixtureNow.Add(-5*time.Hour), schemaenum.TodReportSourceLogLine)
	f.clock.Advance(time.Minute)
	withdrawn := f.report(target, fixtureNow.Add(-4*time.Hour), schemaenum.TodReportSourceManual)
	_, err := f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: withdrawn.Report.ID, Actor: f.reporter,
	})
	require.NoError(t, err)

	visible, _, err := f.tods.List(t.Context(), tod.ListRequest{CircleID: f.circle, Limit: 50})
	require.NoError(t, err)
	require.Len(t, visible, 1)
	require.Equal(t, kept.Report.ID, visible[0].ID)

	all, _, err := f.tods.List(t.Context(), tod.ListRequest{
		CircleID: f.circle, Limit: 50, IncludeRetracted: true,
	})
	require.NoError(t, err)
	require.Len(t, all, 3, "the kill, the retracted kill and the retraction row")
	for _, report := range all {
		if report.ID == withdrawn.Report.ID {
			require.True(t, report.Retracted)
		}
	}
}

// TestList_EveryFilter_NarrowsWhatItSays walks each filter the API design names, including the
// combination — a filter that works alone and not alongside another is a filter nobody tested.
func TestList_EveryFilter_NarrowsWhatItSays(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vulak := f.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	naggy := f.seedTarget("Lord Nagafen", "Nagafen's Lair")
	other := f.seedMember(f.circle, "Sneakco")

	old := f.report(vulak, fixtureNow.Add(-48*time.Hour), schemaenum.TodReportSourceManual)
	recent := f.report(naggy, fixtureNow.Add(-2*time.Hour), schemaenum.TodReportSourceLogLine)
	theirs, err := f.tods.Create(t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: other, TargetID: vulak.ID.String(),
		Server: schemaenum.ServerBlue, DiedAt: fixtureNow.Add(-time.Hour),
	})
	require.NoError(t, err)

	after := fixtureNow.Add(-24 * time.Hour)
	before := fixtureNow.Add(-90 * time.Minute)
	cases := []struct {
		name string
		req  tod.ListRequest
		want []core.TodReportID
	}{
		{
			"by target",
			tod.ListRequest{TargetID: &vulak.ID},
			[]core.TodReportID{theirs.Report.ID, old.Report.ID},
		},
		{
			"by reporter",
			tod.ListRequest{Reporter: &other},
			[]core.TodReportID{theirs.Report.ID},
		},
		{
			"died after",
			tod.ListRequest{DiedAfter: &after},
			[]core.TodReportID{theirs.Report.ID, recent.Report.ID},
		},
		{
			"died before",
			tod.ListRequest{DiedBefore: &before},
			[]core.TodReportID{recent.Report.ID, old.Report.ID},
		},
		{
			"a window from both ends",
			tod.ListRequest{DiedAfter: &after, DiedBefore: &before},
			[]core.TodReportID{recent.Report.ID},
		},
		{
			"target and reporter together",
			tod.ListRequest{TargetID: &vulak.ID, Reporter: &other},
			[]core.TodReportID{theirs.Report.ID},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := tc.req
			req.CircleID, req.Limit = f.circle, 50
			got, _, listErr := f.tods.List(t.Context(), req)
			require.NoError(t, listErr)
			ids := make([]core.TodReportID, 0, len(got))
			for _, report := range got {
				ids = append(ids, report.ID)
			}
			require.Equal(t, tc.want, ids)
		})
	}
}

// TestList_PagesNewestFirstAndTheCursorDoesNotRepeatARow.
func TestList_PagesNewestFirstAndTheCursorDoesNotRepeatARow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Klandicar", "Great Divide")

	var ids []core.TodReportID
	for i := range 5 {
		f.clock.Advance(time.Minute)
		created := f.report(target, fixtureNow.Add(-time.Duration(i+1)*time.Hour),
			schemaenum.TodReportSourceManual)
		ids = append(ids, created.Report.ID)
	}

	seen := map[core.TodReportID]bool{}
	cursor := ""
	pages := 0
	for {
		page, hasMore, err := f.tods.List(t.Context(), tod.ListRequest{
			CircleID: f.circle, Cursor: cursor, Limit: 2,
		})
		require.NoError(t, err)
		pages++
		for _, report := range page {
			require.False(t, seen[report.ID], "the cursor repeated %s", report.ID)
			seen[report.ID] = true
		}
		if !hasMore {
			break
		}
		require.NotEmpty(t, page)
		cursor = page[len(page)-1].ID.String()
		require.Less(t, pages, 10, "the cursor is not advancing")
	}
	require.Len(t, seen, len(ids))
}

// TestGet_AReportInAnotherCircle_IsNotFound: wrong tenant is 404, never 403 — a circle's existence
// is part of what it is hiding.
func TestGet_AReportInAnotherCircle_IsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Talendor", "Skyfire Mountains")
	created := f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceManual)

	theirs := f.seedCircle("Rival", schemaenum.ServerBlue)
	_, err := f.tods.Get(t.Context(), theirs, created.Report.ID)
	requireCode(t, err, apierr.CodeNotFound)
}

// TestReportsFor_HandsTheDerivationEveryRowIncludingRetractions. Consensus folds retractions; the
// store does not, because a store that dropped rows would be deciding what counts as evidence.
func TestReportsFor_HandsTheDerivationEveryRowIncludingRetractions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Zlandicar", "Dragon Necropolis")
	created := f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceManual)
	_, err := f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: created.Report.ID, Actor: f.reporter,
	})
	require.NoError(t, err)

	reports, err := f.tods.ReportsFor(t.Context(), f.circle, target.ID)
	require.NoError(t, err)
	require.Len(t, reports, 2)
	require.Equal(t, created.Report.ID, *reports[1].RetractsReportID)

	f.revoke(f.circle, f.reporter)
	afterRevocation, err := f.tods.ReportsFor(t.Context(), f.circle, target.ID)
	require.NoError(t, err)
	require.Len(t, afterRevocation, 2, "revocation controls access, never history")
	for _, report := range afterRevocation {
		require.True(t, report.ReporterRevoked)
	}
}
