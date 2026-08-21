package tod_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// TestReportQuake_IsOneEventAndNotSixtyKills. An earthquake repops every raid target on the server
// at once; modelling it as N reports would be a lie nobody observed, and it would corrupt every
// confidence figure on the board for a week.
func TestReportQuake_IsOneEventAndNotSixtyKills(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	quake, err := f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
		CircleID: f.circle, Reporter: f.reporter, Note: "server-wide, 02:14",
	})
	require.NoError(t, err)
	require.Equal(t, fixtureNow, quake.OccurredAt, "an omitted occurred_at is now")
	require.Equal(t, fixtureNow, quake.ReportedAt)
	require.Equal(t, schemaenum.TodReportSourceManual, quake.Source)
	require.Equal(t, "server-wide, 02:14", quake.Note)

	reports, err := f.db.Queries().ListTodReportsForCircle(t.Context(), f.circle.String())
	require.NoError(t, err)
	require.Empty(t, reports, "a quake writes no ToD reports at all")
}

// TestReportQuake_InvalidatesTheWholeCircle. The invalidation is circle-wide because the event is:
// one row changes every target's answer, so a cache cleared per target would leave every other
// board stale with nothing to trigger a rebuild.
func TestReportQuake_InvalidatesTheWholeCircle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	second := f.seedTarget("Lord Nagafen", "Nagafen's Lair")
	for _, target := range []string{first.ID.String(), second.ID.String()} {
		_, err := f.db.Queries().PutTargetState(t.Context(), sqlitegen.PutTargetStateParams{
			CircleID: f.circle.String(), TargetID: target,
			ComputedAt: int64(fixtureNow), Status: schemaenum.TargetStateStatusPreWindow,
			Confidence: schemaenum.TargetStateConfidenceHigh,
			CreatedAt:  int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
		require.NoError(t, err)
	}

	_, err := f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
		CircleID: f.circle, Reporter: f.reporter,
	})
	require.NoError(t, err)

	rows, err := f.db.Queries().ListTargetStates(t.Context(), f.circle.String())
	require.NoError(t, err)
	require.Empty(t, rows, "every cached state in the circle is gone, not just one")
}

// TestReportQuake_OccurredAt_CarriesTheSameTwoRejectionsAsAToD. `occurred_at` is game truth and may
// be backdated — somebody reports the quake an hour later — but a quake in the future is impossible
// independent of any derivation, and the table carries the same CHECK.
func TestReportQuake_OccurredAt_CarriesTheSameTwoRejectionsAsAToD(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	backdated, err := f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
		CircleID: f.circle, Reporter: f.reporter, OccurredAt: fixtureNow.Add(-time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, fixtureNow.Add(-time.Hour), backdated.OccurredAt)
	require.Equal(t, fixtureNow, backdated.ReportedAt, "system truth is never backdated")

	_, err = f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
		CircleID: f.circle, Reporter: f.reporter,
		OccurredAt: fixtureNow.Add(tod.FutureTolerance) + 1,
	})
	requireCode(t, err, apierr.CodeDiedAtInFuture)

	_, err = f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
		CircleID: f.circle, Reporter: f.reporter,
		OccurredAt: fixtureNow.Add(-tod.MaxBackdate) - 1,
	})
	requireCode(t, err, apierr.CodeDiedAtTooOld)
}

// TestLatestQuake_IsTheGreatestOccurredAtNotTheNewestRow. `occurred_at` is game truth and may be
// backdated, so the truncation point is a question about the game's clock and not about arrival
// order — a quake reported later about an earlier moment must not move the boundary.
func TestLatestQuake_IsTheGreatestOccurredAtNotTheNewestRow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	recent, err := f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
		CircleID: f.circle, Reporter: f.reporter, OccurredAt: fixtureNow.Add(-time.Hour),
	})
	require.NoError(t, err)

	f.clock.Advance(time.Minute)
	_, err = f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
		CircleID: f.circle, Reporter: f.reporter, OccurredAt: fixtureNow.Add(-8 * time.Hour),
	})
	require.NoError(t, err)

	latest, err := f.tods.LatestQuake(t.Context(), f.circle)
	require.NoError(t, err)
	require.Len(t, latest, 1)
	require.Equal(t, recent.OccurredAt, latest[0].OccurredAt)
}

// TestLatestQuake_ACircleWithNoQuakes_IsNoneRatherThanAnError: an instance that has never had one
// is the ordinary state, and the derivation reads "no quakes" as "nothing to truncate".
func TestLatestQuake_ACircleWithNoQuakes_IsNoneRatherThanAnError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	latest, err := f.tods.LatestQuake(t.Context(), f.circle)
	require.NoError(t, err)
	require.Empty(t, latest)
}

// TestListQuakes_PagesNewestFirstAndSaysWhenThereIsMore.
func TestListQuakes_PagesNewestFirstAndSaysWhenThereIsMore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var ids []string
	for i := range 3 {
		f.clock.Advance(time.Duration(i+1) * time.Minute)
		quake, err := f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
			CircleID: f.circle, Reporter: f.reporter,
		})
		require.NoError(t, err)
		ids = append(ids, quake.ID.String())
	}

	page, hasMore, err := f.tods.ListQuakes(t.Context(), f.circle, "", 2)
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Len(t, page, 2)
	require.Equal(t, ids[2], page[0].ID.String(), "newest first")
	require.Equal(t, ids[1], page[1].ID.String())

	next, hasMore, err := f.tods.ListQuakes(t.Context(), f.circle, page[1].ID.String(), 2)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Len(t, next, 1)
	require.Equal(t, ids[0], next[0].ID.String())
}

// TestListQuakes_AnotherCirclesQuakes_AreInvisible. Every circle-scoped query names `circle_id` in
// its WHERE; this is that rule, driven.
func TestListQuakes_AnotherCirclesQuakes_AreInvisible(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
		CircleID: f.circle, Reporter: f.reporter,
	})
	require.NoError(t, err)

	theirs := f.seedCircle("Rival", schemaenum.ServerBlue)
	page, hasMore, err := f.tods.ListQuakes(t.Context(), theirs, "", 50)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Empty(t, page)
}
