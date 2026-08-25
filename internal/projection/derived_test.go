package projection_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
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
	require.NoError(t, f.states.OnTimerChange(t.Context(), f.db.Queries(), f.circle, target.ID))

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

// TestGet_ANeverReportedTarget_LeavesNoCacheRowForTheVerifyJobToAlertOn.
//
// A cache row exists for exactly the targets with at least one row in `tod_report`. `getTargetState`
// on a mob nobody has reported is an ordinary thing for a person to do — it is most of the board on
// a fresh instance — and it used to leave a row behind that `deriveAll` never visits, which the
// nightly job then deleted as an orphan **and alerted about**. Opening a detail page made
// `verify-states` fail that night for no reason, which is the cry-wolf failure the design is built
// against.
func TestGet_ANeverReportedTarget_LeavesNoCacheRowForTheVerifyJobToAlertOn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lord Nagafen", "Nagafen's Lair", false)

	derived, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Equal(t, schemaenum.TargetStateStatusUnknown, derived.Status)
	require.Nil(t, derived.DiedAt)
	require.Nil(t, derived.ComputedAt,
		"nothing was derived from anything, and claiming an instant would say otherwise")

	_, ok := f.cached(target)
	require.False(t, ok, "there is nothing to cache, so there is no row")

	report, err := f.states.Verify(t.Context())
	require.NoError(t, err)
	require.True(t, report.Healthy(), "reading a detail page is not drift")
	require.Zero(t, report.Orphans)
	require.Empty(t, f.log.errorLines())
}

// TestGet_AStaleRowForANowReportlessTarget_IsDroppedRatherThanRewritten. The same predicate from
// the other side: a row that should not be there is removed by the read that notices, rather than
// left for the nightly job to alert about.
func TestGet_AStaleRowForANowReportlessTarget_IsDroppedRatherThanRewritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lady Vox", "Permafrost Keep", false)
	_, err := f.db.Queries().PutTargetState(t.Context(), sqlitegen.PutTargetStateParams{
		CircleID: f.circle.String(), TargetID: target.ID.String(),
		ComputedAt: int64(fixtureNow), Status: schemaenum.TargetStateStatusInWindow,
		Confidence: schemaenum.TargetStateConfidenceHigh,
		CreatedAt:  int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(t, err)

	_, err = f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)

	_, ok := f.cached(target)
	require.False(t, ok)
}

// TestOnCatalogueTimerChange_FansOutToEveryCircleOnThatServerButNotTheOverriddenOnes.
//
// `raid_target_timer` is instance-wide and PER SERVER, so one write moves the window for every
// circle pinned to that server that has not overridden it. This is the whole reason the port has
// a second method rather than a nullable circle id: the fan-out is real work and the caller knows
// which kind it did.
//
// Three things are asserted at once because they are one rule: a plain circle on that server
// moves, a circle that overrode the catalogue does NOT, and a circle on another server is not
// touched at all.
func TestOnCatalogueTimerChange_FansOutToEveryCircleOnThatServerButNotTheOverriddenOnes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)

	const catalogueOpen = 5 * 24 * time.Hour
	const overrideOpen = 3 * 24 * time.Hour
	const movedOpen = 6 * 24 * time.Hour
	f.seedCatalogueTimer(target, catalogueOpen, 7*24*time.Hour)
	f.seedCatalogueTimerOn(target, schemaenum.ServerGreen, catalogueOpen, 7*24*time.Hour)

	// Two plain circles on blue, not one. The failure worth guarding against is a fan-out that
	// reaches SOME circle without an override and stops — a single plain circle would pass that
	// just as well as a correct loop does.
	plain := f.circle
	alsoPlain := f.seedCircle("Second Blue", schemaenum.ServerBlue)
	overriding := f.seedCircle("Overrider", schemaenum.ServerBlue)
	elsewhere := f.seedCircle("Green Guild", schemaenum.ServerGreen)
	all := []core.CircleID{plain, alsoPlain, overriding, elsewhere}

	died := fixtureNow.Add(-time.Hour)
	f.report(target, died, schemaenum.TodReportSourceLogLine)
	for _, circleID := range []core.CircleID{alsoPlain, overriding, elsewhere} {
		f.reportIn(circleID, target, died, schemaenum.TodReportSourceLogLine)
	}

	f.seedOverrideIn(overriding, target, overrideOpen, 4*24*time.Hour)
	for _, circleID := range all {
		_, err := f.states.Rebuild(t.Context(), circleID)
		require.NoError(t, err)
	}
	before := map[core.CircleID]sqlitegen.TargetStateCache{}
	for _, circleID := range all {
		row, ok := f.cachedIn(circleID, target)
		require.True(t, ok)
		before[circleID] = row
	}
	require.Equal(t, int64(died.Add(catalogueOpen)), *before[plain].WindowOpenAt)
	require.Equal(t, int64(died.Add(catalogueOpen)), *before[alsoPlain].WindowOpenAt)
	require.Equal(t, int64(died.Add(overrideOpen)), *before[overriding].WindowOpenAt)

	// The catalogue's blue window moves. Nothing is reported anywhere.
	f.seedCatalogueTimer(target, movedOpen, 8*24*time.Hour)
	require.NoError(t, f.states.OnCatalogueTimerChange(
		t.Context(), f.db.Queries(), core.Server(schemaenum.ServerBlue), target.ID))

	// EVERY circle without an override, not just the first one the loop reached. A missed circle
	// is a silently stale board, which is the failure this fan-out exists to prevent.
	for _, circleID := range []core.CircleID{plain, alsoPlain} {
		moved, ok := f.cachedIn(circleID, target)
		require.True(t, ok)
		require.Equal(t, int64(died.Add(movedOpen)), *moved.WindowOpenAt,
			"a circle taking the catalogue's word for it moved with it")
		require.Equal(t, schemaenum.TargetStateChangeReasonTimerChange, *moved.ChangeReason,
			"and says why, since nothing was reported")
	}

	held, ok := f.cachedIn(overriding, target)
	require.True(t, ok)
	require.Equal(t, *before[overriding].WindowOpenAt, *held.WindowOpenAt,
		"the override still wins, which is what circle_timer_override is for")
	require.Equal(t, before[overriding].ChangeReason, held.ChangeReason,
		"and its board did not change, so it does not claim a timer_change it did not have")

	green, ok := f.cachedIn(elsewhere, target)
	require.True(t, ok)
	require.Equal(t, *before[elsewhere].WindowOpenAt, *green.WindowOpenAt,
		"a circle on another server is not on this fan-out at all")
	require.Equal(t, before[elsewhere].ChangeReason, green.ChangeReason)
}

// TestOnCatalogueTimerChange_ABadServer_IsRefusedRatherThanFanningOutToNothing. Silently matching
// no circles would report success while every board stayed stale.
func TestOnCatalogueTimerChange_ABadServer_IsRefusedRatherThanFanningOutToNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Trakanon", "Old Sebilis", false)
	err := f.states.OnCatalogueTimerChange(t.Context(), f.db.Queries(), core.Server("purple"), target.ID)
	require.Error(t, err)
	got, ok := apierr.From(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeValidationFailed, got.Code())
}

// TestPutOverride_TheRecomputation_ReadsTheWindowItJustWrote is the half of ADR-0013 that a
// rollback test cannot reach.
//
// The push joining the transaction is only half the fix. [Service.recompute] asks
// `catalogue.ResolveTimer` what the effective window IS, and that read has to join the SAME
// transaction: on a pooled connection it reads the snapshot from before the transaction opened, so
// it would recompute every board from the window that was there BEFORE and cache the answer. The
// write would be perfectly atomic and the board perfectly wrong — worse than the gap it replaced,
// because the stale row now carries `timer_change` and reads as freshly derived.
//
// So this asserts the CONTENT, not the push: nothing here calls OnTimerChange. The override is
// written the way a route writes one — through the catalogue, with this projection as its
// invalidator — and the cached row must hold the override's window rather than the catalogue's.
func TestPutOverride_TheRecomputation_ReadsTheWindowItJustWrote(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Gorenaire", "The Dreadlands", false)
	const catalogueOpen, overrideOpen = 24 * time.Hour, 6 * time.Hour
	f.seedCatalogueTimer(target, catalogueOpen, 36*time.Hour)

	died := fixtureNow.Add(-time.Hour)
	f.report(target, died, schemaenum.TodReportSourceLogLine)
	_, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	before, ok := f.cached(target)
	require.True(t, ok)
	require.NotNil(t, before.WindowOpenAt)
	require.Equal(t, int64(died.Add(catalogueOpen)), *before.WindowOpenAt,
		"the board is not standing on the catalogue timer, so moving off it proves nothing")

	// The write, and nothing else. Its own transaction recomputes the board.
	f.seedOverride(target, overrideOpen, 10*time.Hour)

	after, ok := f.cached(target)
	require.True(t, ok)
	require.NotNil(t, after.WindowOpenAt)
	require.Equal(t, int64(died.Add(overrideOpen)), *after.WindowOpenAt,
		"the recomputation resolved the timer on a connection that could not see the override "+
			"the same transaction had just written, so it re-derived the board from the "+
			"catalogue window and cached that as `timer_change`")
	require.NotNil(t, after.ChangeReason)
	require.Equal(t, schemaenum.TargetStateChangeReasonTimerChange, *after.ChangeReason)
}
