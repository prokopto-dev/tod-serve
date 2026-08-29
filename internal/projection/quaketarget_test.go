package projection_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// TestQuakeTargetFlip_TheBoardAndTheDetailPage_Agree is the gate for issue #21.
//
// `is_quake_target` is a DERIVATION INPUT: `catalogue.timerOf` copies it onto the
// [consensus.Timer] every path resolves, and `consensus.Derive` truncates every kill before the
// latest quake when it is set. So flipping it moves the answer for every circle that has reported
// the target, with no row appended anywhere to say so — the same shape as a moved window, and the
// same reason it has to be PUSHED.
//
// Before the fix the flip recomputed nothing, and the board could not recover on its own:
// `boardEntry` passes `up_since` only when the CACHED status is already `up`, which is correct as
// a rule — `up` is reachable only by a quake, and a quake clears the whole circle's cache — and is
// exactly why a stale `overdue` row stayed `overdue` however current the timer beside it was.
//
// The assertion is behavioural rather than about the row, because the row is droppable and the
// disagreement is what an officer sees: the board hands them a ToD for a mob the detail page says
// a quake repopped.
func TestQuakeTargetFlip_TheBoardAndTheDetailPage_Agree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)
	f.seedCatalogueTimer(target, 5*24*time.Hour, 7*24*time.Hour)
	f.report(target, fixtureNow.Add(-8*24*time.Hour), schemaenum.TodReportSourceLogLine)

	quake, err := f.tods.ReportQuake(t.Context(), todQuake(f, fixtureNow.Add(-4*time.Hour)))
	require.NoError(t, err)

	// The board caches the answer derived under `is_quake_target = false`: the quake is not this
	// target's business, so the kill still stands and its window has closed.
	before := f.entryFor(f.board(), target)
	require.Equal(t, schemaenum.TargetStateStatusOverdue, before.Status,
		"the fixture did not reach the state this test is about")

	quakeTarget := true
	_, err = f.catalogue.Update(t.Context(), target.ID, catalogue.UpdateRequest{
		IsQuakeTarget: &quakeTarget,
	}, f.states)
	require.NoError(t, err)

	// **The board is read FIRST, deliberately.** `getTargetState` re-derives and refreshes the
	// cached row as a side effect, so asking it first would repair the very row this is about and
	// the disagreement would vanish between the two calls that are meant to expose it.
	entry := f.entryFor(f.board(), target)
	derived, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)

	require.Equal(t, derived.Status, entry.Status,
		"the board and getTargetState disagree about the same target: the board hands an officer "+
			"a ToD for a mob the detail page says a quake repopped")
	require.Equal(t, derived.DiedAt, entry.DiedAt, "the board's ToD is not the derivation's")
	require.Equal(t, derived.UpSince, entry.UpSince, "the board's up_since is not the derivation's")

	// And both say what the report log plus the flag actually imply, so this cannot pass by the
	// two answers agreeing on something wrong.
	require.Equal(t, schemaenum.TargetStateStatusUp, entry.Status)
	require.Nil(t, entry.DiedAt,
		"the ToD before the quake describes a life the target no longer has")
	require.NotNil(t, entry.UpSince)
	require.Equal(t, quake.OccurredAt, *entry.UpSince)
}

// TestOnQuakeTargetChange_EveryLiveCircle_IsRecomputed_IncludingTheOverriddenOnes is the fan-out,
// and it is the half the agreement gate above cannot see: that test has one circle.
//
// `raid_target` is instance-wide and carries no server, so a flip moves the derivation for every
// circle on the instance at once — a wider fan-out than a catalogue timer's, which stops at one
// server. And it must skip nobody: [catalogue.Service.PutOverride] replaces a circle's WINDOW and
// never this flag, so `catalogue.timerOf` stamps the new value onto an overridden timer exactly as
// it does onto a catalogue one. Skipping the overridden circles — which is the right thing for
// `OnCatalogueTimerChange` and would be an easy thing to share — would leave the boards of every
// guild that had tracked a mob themselves holding the stale answer.
func TestOnQuakeTargetChange_EveryLiveCircle_IsRecomputed_IncludingTheOverriddenOnes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)

	// One circle per server, plus a second on the fixture's own, plus the fixture's. The servers
	// matter because a catalogue timer's fan-out is per-server and this one is not: a fix that
	// looped over `core.Servers()` calling the timer fan-out would still skip the overridden
	// circle below, and only these two dimensions together say so.
	circles := map[string]core.CircleID{
		"blue (the fixture's own)": f.circle,
		"blue (a second guild)":    f.seedCircle("Sanctuary", schemaenum.ServerBlue),
		"green":                    f.seedCircle("Bloodline", schemaenum.ServerGreen),
		"red":                      f.seedCircle("Ruin", schemaenum.ServerRed),
	}
	overridden := circles["green"]

	for _, server := range []string{
		schemaenum.ServerBlue, schemaenum.ServerGreen, schemaenum.ServerRed,
	} {
		f.seedCatalogueTimerOn(target, server, 5*24*time.Hour, 7*24*time.Hour)
	}
	// This circle disagrees with the catalogue about the WINDOW. It agrees, because it has no
	// choice, about whether a quake repops the mob.
	f.seedOverrideIn(overridden, target, 6*24*time.Hour, 8*24*time.Hour)

	died := fixtureNow.Add(-9 * 24 * time.Hour)
	quakeAt := fixtureNow.Add(-4 * time.Hour)
	for _, circleID := range circles {
		f.reportIn(circleID, target, died, schemaenum.TodReportSourceLogLine)
		_, err := f.tods.ReportQuake(t.Context(), tod.ReportQuakeRequest{
			CircleID: circleID, Reporter: f.seedMember(circleID, "Quaker"), OccurredAt: quakeAt,
		})
		require.NoError(t, err)
		// A quake clears the circle's whole cache, so the row is written back here rather than
		// left to a read: what this test is about is a row that EXISTS and is stale.
		_, err = f.states.Rebuild(t.Context(), circleID)
		require.NoError(t, err)
	}

	for name, circleID := range circles {
		row, ok := f.cachedIn(circleID, target)
		require.True(t, ok, "%s has no cached row, so it cannot go stale and proves nothing", name)
		require.Equal(t, schemaenum.TargetStateStatusOverdue, row.Status,
			"%s did not reach the state this test is about", name)
	}

	quakeTarget := true
	_, err := f.catalogue.Update(t.Context(), target.ID, catalogue.UpdateRequest{
		IsQuakeTarget: &quakeTarget,
	}, f.states)
	require.NoError(t, err)

	for name, circleID := range circles {
		row, ok := f.cachedIn(circleID, target)
		require.True(t, ok, "%s: the row went away rather than being recomputed", name)
		require.Equal(t, schemaenum.TargetStateStatusUp, row.Status,
			"%s still holds the answer derived under the old flag: its board hands an officer a "+
				"ToD for a mob a quake repopped", name)
		require.Nil(t, row.DiedAt,
			"%s kept a ToD describing a life the target no longer has", name)
		require.NotNil(t, row.ChangeReason)
		require.Equal(t, schemaenum.TargetStateChangeReasonTimerChange, *row.ChangeReason,
			"%s: the answer moved with nothing appended to the report log, and change_reason is "+
				"the only thing that can say why", name)
	}
}

// TestOnQuakeTargetChange_ACircleWithNoReports_GetsNoCacheRow.
//
// The fan-out walks every live circle, which is wider than the set that can hold a stale row: a
// cache row exists for exactly the targets with at least one row in `tod_report`. A recomputation
// that wrote a row of nulls for a circle that has never reported the mob would be an ORPHAN by
// [projection.Service.Verify]'s definition — removed, and ALERTED about, at ERROR, that night. An
// instance-wide fan-out is precisely the write that could produce a few hundred of them.
func TestOnQuakeTargetChange_ACircleWithNoReports_GetsNoCacheRow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)
	quiet := f.seedCircle("Sanctuary", schemaenum.ServerBlue)

	quakeTarget := true
	_, err := f.catalogue.Update(t.Context(), target.ID, catalogue.UpdateRequest{
		IsQuakeTarget: &quakeTarget,
	}, f.states)
	require.NoError(t, err)

	_, ok := f.cachedIn(quiet, target)
	require.False(t, ok,
		"the fan-out wrote a cache row for a target this circle has never reported; the nightly "+
			"verify job removes that row and alerts on it, which is the cry-wolf failure")

	report, err := f.states.Verify(t.Context())
	require.NoError(t, err)
	require.Zero(t, report.Orphans)
	require.True(t, report.Healthy(), "the flip left the nightly job something to alert about")
}
