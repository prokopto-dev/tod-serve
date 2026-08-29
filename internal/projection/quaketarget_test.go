package projection_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
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
	})
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
