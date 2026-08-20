package projection_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// TestGet_Reporters_AppearOnlyWithAttribution.
//
// That separation IS the `observer` role: a circle can share a board with an allied guild without
// handing over the identity of its trackers. **The evidence counts stay visible either way**,
// because a confidence figure with no denominator is worse than no confidence figure.
func TestGet_Reporters_AppearOnlyWithAttribution(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)
	f.seedCatalogueTimer(target, 5*24*time.Hour, 7*24*time.Hour)
	sneak := f.seedMember(f.circle, "Sneakco")

	died := fixtureNow.Add(-2 * time.Hour)
	f.report(target, died, schemaenum.TodReportSourceLogLine)
	f.reportAs(sneak, target, died.Add(90*time.Second), schemaenum.TodReportSourceManual)

	observer, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Empty(t, observer.Reporters, "an observer sees the state and not who reported it")
	require.Equal(t, 2, observer.Evidence.ReportCount, "and the counts are still there")
	require.Equal(t, 2, observer.Evidence.DistinctReporterCount)
	require.Equal(t, 1, observer.Evidence.LogLineCount)
	require.Len(t, observer.Evidence.ReportIDs, 2)

	officer, err := f.states.Get(t.Context(), f.circle, target.ID, true)
	require.NoError(t, err)
	require.Len(t, officer.Reporters, 2)
	names := []string{officer.Reporters[0].DisplayName, officer.Reporters[1].DisplayName}
	require.ElementsMatch(t, []string{"Tankguy", "Sneakco"}, names)
	for _, r := range officer.Reporters {
		require.False(t, r.Revoked)
	}
}

// TestGet_ARevokedReporter_RendersRevokedAndStillCounts. Revocation controls access, never
// history — the revocation rule, made visible.
func TestGet_ARevokedReporter_RendersRevokedAndStillCounts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lord Nagafen", "Nagafen's Lair", false)
	f.seedCatalogueTimer(target, 5*time.Hour, 9*time.Hour)
	sneak := f.seedMember(f.circle, "Sneakco")

	died := fixtureNow.Add(-time.Hour)
	f.report(target, died, schemaenum.TodReportSourceLogLine)
	f.reportAs(sneak, target, died.Add(time.Minute), schemaenum.TodReportSourceLogLine)

	before, err := f.states.Get(t.Context(), f.circle, target.ID, true)
	require.NoError(t, err)

	f.revoke(sneak)
	after, err := f.states.Get(t.Context(), f.circle, target.ID, true)
	require.NoError(t, err)

	require.Equal(t, before.DiedAt, after.DiedAt, "their report still moves the estimate")
	require.Equal(t, before.Confidence, after.Confidence, "and still counts toward confidence")
	require.Equal(t, before.Evidence.ReportCount, after.Evidence.ReportCount)
	require.Equal(t, 1, after.Evidence.RevokedReporterCount, "and the fact is visible")

	var found bool
	for _, r := range after.Reporters {
		if r.MembershipID == sneak {
			found, _ = true, r
			require.True(t, r.Revoked, "the reporter renders as revoked")
		}
	}
	require.True(t, found, "a revoked reporter is still named, not dropped")
}

// TestGet_AllTheCurrentClustersReportsRetracted_FallsBackToThePreviousCluster, and says why.
func TestGet_AllTheCurrentClustersReportsRetracted_FallsBackToThePreviousCluster(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lady Vox", "Permafrost Keep", false)
	f.seedCatalogueTimer(target, 16*time.Hour, 24*time.Hour)

	earlier := fixtureNow.Add(-30 * time.Hour)
	later := fixtureNow.Add(-2 * time.Hour)
	f.report(target, earlier, schemaenum.TodReportSourceLogLine)
	newest := f.report(target, later, schemaenum.TodReportSourceLogLine)

	current, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Equal(t, later, *current.DiedAt, "the latest died_at wins")

	_, err = f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: newest.Report.ID, Actor: f.reporter,
	})
	require.NoError(t, err)

	after, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Equal(t, earlier, *after.DiedAt, "the previous cluster becomes current")
	require.NotNil(t, after.ChangeReason)
	require.Equal(t, schemaenum.TargetStateChangeReasonRetraction, *after.ChangeReason)
}

// TestGet_ChangeReason_SaysWhyTheAnswerMoved.
//
// The answer changing with no new kill is correct and expected — a backfilled corroboration shifts
// the median — and this is what makes it visible rather than mysterious.
func TestGet_ChangeReason_SaysWhyTheAnswerMoved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Trakanon", "Old Sebilis", true)
	f.seedCatalogueTimer(target, 3*24*time.Hour, 5*24*time.Hour)
	sneak := f.seedMember(f.circle, "Sneakco")

	died := fixtureNow.Add(-4 * time.Hour)
	f.report(target, died, schemaenum.TodReportSourceManual)
	first, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Equal(t, schemaenum.TargetStateChangeReasonNewKill, *first.ChangeReason)
	firstEstimate := *first.DiedAt

	// A second reporter, well inside ε: same kill, more evidence, and the median moves.
	f.reportAs(sneak, target, died.Add(-6*time.Minute), schemaenum.TodReportSourceManual)
	second, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Equal(t, schemaenum.TargetStateChangeReasonCorroboration, *second.ChangeReason)
	require.NotEqual(t, firstEstimate, *second.DiedAt,
		"the answer moved with no new kill, which is exactly what change_reason is for")

	_, err = f.tods.ReportQuake(t.Context(), todQuake(f, fixtureNow.Add(-time.Minute)))
	require.NoError(t, err)
	third, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Equal(t, schemaenum.TargetStateChangeReasonQuake, *third.ChangeReason)
	require.Equal(t, schemaenum.TargetStateStatusUp, third.Status)
}

// TestOnTimerChange_RecordsTheOneReasonTheLogCannotShow. A window moving with no row appended
// changes every answer derived from it, and a board that changed for no visible reason is the thing
// consensus §8 says must not happen.
func TestOnTimerChange_RecordsTheOneReasonTheLogCannotShow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Gorenaire", "The Dreadlands", false)
	f.seedCatalogueTimer(target, 24*time.Hour, 36*time.Hour)
	f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceLogLine)

	before, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Equal(t, schemaenum.TargetStateChangeReasonNewKill, *before.ChangeReason)

	f.seedOverride(target, 6*time.Hour, 10*time.Hour)
	require.NoError(t, f.states.OnTimerChange(t.Context(), f.circle, target.ID))

	row, ok := f.cached(target)
	require.True(t, ok)
	require.NotNil(t, row.ChangeReason)
	require.Equal(t, schemaenum.TargetStateChangeReasonTimerChange, *row.ChangeReason)

	after := f.entryFor(f.board(), target)
	require.Equal(t, schemaenum.TargetStateChangeReasonTimerChange, *after.ChangeReason)
	require.NotEqual(t, *before.Window.OpenAt, *after.Window.OpenAt, "and the window moved")
}

// TestGet_AnImplausibleDiedAt_IsFlaggedAndNeverRejected. Derived state must never veto an
// observation: the report is stored, it is named in `implausible_report_ids`, and the disagreement
// is surfaced as `contested` rather than resolved by throwing the report away.
func TestGet_AnImplausibleDiedAt_IsFlaggedAndNeverRejected(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Severilous", "Emerald Jungle", false)
	f.seedCatalogueTimer(target, 5*24*time.Hour, 7*24*time.Hour)
	sneak := f.seedMember(f.circle, "Sneakco")

	// A kill two days ago, then a report of a kill three days ago — before the first one's window
	// could have opened, so the two cannot both be true.
	f.report(target, fixtureNow.Add(-2*24*time.Hour), schemaenum.TodReportSourceLogLine)
	older := f.reportAs(sneak, target, fixtureNow.Add(-3*24*time.Hour),
		schemaenum.TodReportSourceLogLine)

	derived, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Contains(t, derived.ImplausibleReportIDs, older.Report.ID,
		"the observation is flagged, and kept")
	require.True(t, derived.Contested)
	require.NotNil(t, derived.ContestReason)
	require.Equal(t, schemaenum.TargetStateContestReasonImplausibleOrdering, *derived.ContestReason)

	// And it is still in the log, which is the whole point.
	stored, err := f.tods.Get(t.Context(), f.circle, older.Report.ID)
	require.NoError(t, err)
	require.False(t, stored.Retracted)
}

// TestGet_AnUnknownTarget_IsNotFound: the catalogue answers, and its problems come back unchanged.
func TestGet_AnUnknownTarget_IsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	missing := newID[core.RaidTarget](f)
	_, err := f.states.Get(t.Context(), f.circle, missing, false)
	require.Error(t, err)
	got, ok := apierr.From(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeNotFound, got.Code())
}

// TestGet_RefreshesTheCacheAsASideEffect, which is what makes a read-miss self-healing: a dropped
// table costs latency, never correctness.
func TestGet_RefreshesTheCacheAsASideEffect(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Klandicar", "Great Divide", false)
	f.seedCatalogueTimer(target, 5*time.Hour, 9*time.Hour)
	f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceLogLine)

	_, ok := f.cached(target)
	require.False(t, ok, "the append invalidated it in the same transaction")

	derived, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)

	row, ok := f.cached(target)
	require.True(t, ok)
	require.Equal(t, derived.Status, row.Status)
	require.Equal(t, int64(derived.Evidence.ReportCount), row.ReportCount)
}
