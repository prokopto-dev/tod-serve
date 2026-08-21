package tod_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// TestRetract_WritesANewRowAndLeavesTheOriginalUntouched is the invariant, stated as a byte
// comparison: the original report is read before and after and must be identical apart from the
// `retracted` flag, which is computed from the NEW row rather than stored on the old one.
func TestRetract_WritesANewRowAndLeavesTheOriginalUntouched(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	created := f.report(target, fixtureNow.Add(-2*time.Hour), schemaenum.TodReportSourceManual)

	f.clock.Advance(7 * time.Minute)
	retracted, err := f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: created.Report.ID, Actor: f.reporter,
		Reason: "wrong mob",
	})
	require.NoError(t, err)

	require.Equal(t, schemaenum.TodReportKindRetraction, retracted.Retraction.Kind)
	require.Equal(t, created.Report.ID, *retracted.Retraction.RetractsReportID)
	require.Equal(t, fixtureNow.Add(7*time.Minute), retracted.Retraction.ReportedAt)
	require.Equal(t, created.Report.DiedAt, retracted.Retraction.DiedAt,
		"a retraction is an act, not an observation: it carries the original's died_at rather "+
			"than inventing a time nothing witnessed")

	// The original, read back from the database rather than from the return value.
	after, err := f.tods.Get(t.Context(), f.circle, created.Report.ID)
	require.NoError(t, err)
	want := created.Report
	want.Retracted = true
	require.Empty(t, cmp.Diff(want, after),
		"the only difference is the computed `retracted` flag; the row itself is untouched")

	rows, err := f.db.Queries().ListTodReportsForCircle(t.Context(), f.circle.String())
	require.NoError(t, err)
	require.Len(t, rows, 2, "the log grew by one; nothing was edited")
}

// TestRetract_ASecondTime_IsAlreadyRetracted, from both directions: the check in Go, and the
// partial unique index behind it. Two officers retracting at once meet the index; one officer
// clicking twice meets the check. Both answer the same code.
func TestRetract_ASecondTime_IsAlreadyRetracted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lord Nagafen", "Nagafen's Lair")
	created := f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceManual)

	req := tod.RetractRequest{
		CircleID: f.circle, ReportID: created.Report.ID, Actor: f.reporter,
	}
	_, err := f.tods.Retract(t.Context(), req)
	require.NoError(t, err)

	_, err = f.tods.Retract(t.Context(), req)
	requireCode(t, err, apierr.CodeAlreadyRetracted)
}

// TestRetract_ARetraction_IsRefusedRatherThanTreatedAsAnUndo.
//
// Reading it as an undo would resurrect a report somebody deliberately withdrew. The way to say
// "the original was right after all" is a fresh kill report, which is a row anybody can see.
func TestRetract_ARetraction_IsRefusedRatherThanTreatedAsAnUndo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lady Vox", "Permafrost Keep")
	created := f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceManual)

	retracted, err := f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: created.Report.ID, Actor: f.reporter,
	})
	require.NoError(t, err)

	_, err = f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: retracted.Retraction.ID, Actor: f.reporter,
	})
	requireCode(t, err, apierr.CodeAlreadyRetracted)
}

// TestRetract_SomebodyElsesReport_NeedsRetractAny. `tod.retract` covers your own; taking somebody
// else's off the board is a different key. It is 403 and not 404 because the tenant is right and
// only the permission is missing — canonical §7's exact distinction.
func TestRetract_SomebodyElsesReport_NeedsRetractAny(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Trakanon", "Old Sebilis")
	stranger := f.seedMember(f.circle, "Sneakco")
	created := f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceManual)

	_, err := f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: created.Report.ID, Actor: stranger,
	})
	requireCode(t, err, apierr.CodeRetractNotPermitted)

	officer, err := f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: created.Report.ID, Actor: stranger,
		CanRetractAny: true,
	})
	require.NoError(t, err)
	require.Equal(t, stranger, officer.Retraction.Reporter)
}

// TestRetract_ARevokedMembersRetraction_StillApplies. The revocation rule cuts both ways: their
// reports still count and their retractions still apply. Anything else means revocation silently
// rewrites history.
func TestRetract_ARevokedMembersRetraction_StillApplies(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Gorenaire", "The Dreadlands")
	created := f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceManual)

	f.revoke(f.circle, f.reporter)
	retracted, err := f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: created.Report.ID, Actor: f.reporter,
	})
	require.NoError(t, err)
	require.True(t, retracted.Retraction.ReporterRevoked, "the reporter renders as revoked")
	require.True(t, retracted.Original.Retracted, "and the retraction applies all the same")
}

// TestRetract_AReportInAnotherCircle_IsNotFound. The tenancy middleware answers this before a
// handler runs; the service is checked anyway, because "unreachable" is a claim about the
// middleware that this layer cannot verify.
func TestRetract_AReportInAnotherCircle_IsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Severilous", "Emerald Jungle")
	created := f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceManual)

	theirs := f.seedCircle("Rival", schemaenum.ServerBlue)
	_, err := f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: theirs, ReportID: created.Report.ID,
		Actor: f.seedMember(theirs, "Someone"), CanRetractAny: true,
	})
	requireCode(t, err, apierr.CodeNotFound)
}
