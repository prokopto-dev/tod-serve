package projection_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// TestVerify_ACorruptedCacheRow_IsRepairedAndAlerted is the load-bearing test for
// `CacheIsNotAuthority`, and the reason the nightly job exists at all.
//
// It corrupts a cached row the way real drift looks — a plausible status, a plausible confidence,
// a `died_at` that is simply wrong — runs the job, and asserts three things: the recomputation
// WON, the discrepancy was reported field by field, and an ERROR was logged. The third is not
// decoration: a repair nobody hears about is a cache that goes on drifting, and the job is the
// only thing standing between a stale cache and a wrong board.
func TestVerify_ACorruptedCacheRow_IsRepairedAndAlerted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)
	f.seedCatalogueTimer(target, 5*24*time.Hour, 7*24*time.Hour)

	died := fixtureNow.Add(-4 * time.Hour)
	f.report(target, died, schemaenum.TodReportSourceLogLine)

	// A read builds the row honestly, so what follows is a corruption of a row that WAS right.
	_, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	honest, ok := f.cached(target)
	require.True(t, ok)
	require.Equal(t, schemaenum.TargetStateStatusPreWindow, honest.Status)

	corrupt := honest
	corrupt.Status = schemaenum.TargetStateStatusOverdue
	corrupt.Confidence = schemaenum.TargetStateConfidenceHigh
	wrongDied := int64(died.Add(-9 * time.Hour))
	corrupt.DiedAt = &wrongDied
	corrupt.ReportCount = 7
	corrupt.DistinctReporterCount = 4
	_, err = f.db.Queries().PutTargetState(t.Context(), putParamsFrom(corrupt))
	require.NoError(t, err)

	report, err := f.states.Verify(t.Context())
	require.NoError(t, err)

	require.False(t, report.Healthy(), "a run that repaired something is not healthy")
	require.Equal(t, 1, report.CirclesChecked)
	require.Equal(t, 1, report.TargetsChecked)
	require.Equal(t, 1, report.Repaired)
	require.Zero(t, report.Orphans)

	fields := map[string]projection.Discrepancy{}
	for _, d := range report.Discrepancies {
		fields[d.Field] = d
	}
	require.Contains(t, fields, "status")
	require.Contains(t, fields, "confidence")
	require.Contains(t, fields, "died_at")
	require.Contains(t, fields, "report_count")
	require.Contains(t, fields, "distinct_reporter_count")
	require.Equal(t, schemaenum.TargetStateStatusOverdue, fields["status"].Cached)
	require.Equal(t, schemaenum.TargetStateStatusPreWindow, fields["status"].Computed,
		"the recomputation is what the row should say")

	// THE RECOMPUTATION WINS: the row on disk is the derivation's answer, not the corrupted one.
	repaired, ok := f.cached(target)
	require.True(t, ok)
	require.Equal(t, honest.Status, repaired.Status)
	require.Equal(t, honest.Confidence, repaired.Confidence)
	require.Equal(t, honest.DiedAt, repaired.DiedAt)
	require.Equal(t, honest.ReportCount, repaired.ReportCount)
	require.Equal(t, honest.DistinctReporterCount, repaired.DistinctReporterCount)

	// AND AN ALERT FIRES.
	alerts := f.log.errorLines()
	require.NotEmpty(t, alerts, "a silent repair is a cache that goes on drifting")
	joined := strings.Join(alerts, "\n")
	require.Contains(t, joined, projection.AlertMessage)
	require.Contains(t, joined, target.ID.String())
	require.Contains(t, joined, "field=status")
	require.Contains(t, joined, "repaired=1", "and a summary, so one line names the whole run")

	// A second run has nothing left to say: the repair is idempotent, and a job that alerted every
	// night on a healthy instance is a job somebody turns off.
	second, err := f.states.Verify(t.Context())
	require.NoError(t, err)
	require.True(t, second.Healthy())
	require.Empty(t, second.Discrepancies)
}

// TestVerify_AnOrphanedCacheRow_IsRemovedAndAlerted. A row naming a target with no reports left is
// one nothing will ever recompute again — every report retracted, or a row written by something
// that no longer has evidence behind it.
func TestVerify_AnOrphanedCacheRow_IsRemovedAndAlerted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lord Nagafen", "Nagafen's Lair", false)

	_, err := f.db.Queries().PutTargetState(t.Context(), sqlitegen.PutTargetStateParams{
		CircleID: f.circle.String(), TargetID: target.ID.String(),
		ComputedAt: int64(fixtureNow),
		Status:     schemaenum.TargetStateStatusInWindow,
		Confidence: schemaenum.TargetStateConfidenceHigh,
		CreatedAt:  int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(t, err)

	report, err := f.states.Verify(t.Context())
	require.NoError(t, err)
	require.False(t, report.Healthy())
	require.Equal(t, 1, report.Orphans)
	require.Zero(t, report.TargetsChecked, "there was nothing to derive")

	_, ok := f.cached(target)
	require.False(t, ok, "the row is removed rather than left to be believed")
	require.Contains(t, strings.Join(f.log.errorLines(), "\n"), projection.AlertMessage)
}

// TestVerify_AHealthyInstance_IsSilent. The other half of the alert being useful: a job that fired
// on every ordinary night would be turned off, and an absent row is the ORDINARY state between an
// invalidating write and the next read — not drift.
func TestVerify_AHealthyInstance_IsSilent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lady Vox", "Permafrost Keep", false)
	f.seedCatalogueTimer(target, 16*time.Hour, 24*time.Hour)
	f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceLogLine)

	// The report invalidated nothing to begin with and left no cached row behind it.
	_, ok := f.cached(target)
	require.False(t, ok)

	report, err := f.states.Verify(t.Context())
	require.NoError(t, err)
	require.True(t, report.Healthy())
	require.Equal(t, 1, report.TargetsChecked)
	require.Empty(t, f.log.errorLines(), "an absent row is not drift")
}

// TestVerify_ADroppedCache_IsRebuiltInFull. `target_state_cache` is droppable by construction, and
// this is the test that says so: delete every row and the job puts them all back.
func TestVerify_ADroppedCache_IsRebuiltInFull(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)
	second := f.seedTarget("Trakanon", "Old Sebilis", false)
	f.seedCatalogueTimer(first, 5*24*time.Hour, 7*24*time.Hour)
	f.report(first, fixtureNow.Add(-2*time.Hour), schemaenum.TodReportSourceLogLine)
	f.report(second, fixtureNow.Add(-3*time.Hour), schemaenum.TodReportSourceManual)

	written, err := f.states.Rebuild(t.Context(), f.circle)
	require.NoError(t, err)
	require.Equal(t, 2, written)

	_, err = f.db.Queries().InvalidateCircleTargetStates(t.Context(), f.circle.String())
	require.NoError(t, err)
	_, ok := f.cached(first)
	require.False(t, ok)

	report, err := f.states.Verify(t.Context())
	require.NoError(t, err)
	require.True(t, report.Healthy(), "an empty cache is not drift; it is a cache")
	require.Equal(t, 2, report.TargetsChecked)

	restored, ok := f.cached(first)
	require.True(t, ok)
	require.Equal(t, schemaenum.TargetStateStatusPreWindow, restored.Status)
}

// TestRebuildAll_SweepsEveryLiveCircle. The job has no caller and no tenant: a maintenance sweep
// that could only see one circle would leave every other circle's cache unverified.
func TestRebuildAll_SweepsEveryLiveCircle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedTarget("Gorenaire", "The Dreadlands", false)
	f.report(mine, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceManual)

	written, err := f.states.RebuildAll(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, written)

	report, err := f.states.Verify(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, report.CirclesChecked)
	require.True(t, report.Healthy())
}

// putParamsFrom turns a cache row back into the parameters that write it, so a test can corrupt one
// field and leave the rest as they were.
//
// The two structs have the same fields in the same order, so this is a conversion rather than a
// literal: a column added to one and not the other becomes a build failure here instead of a field
// this test quietly stopped copying.
func putParamsFrom(row sqlitegen.TargetStateCache) sqlitegen.PutTargetStateParams {
	return sqlitegen.PutTargetStateParams(row)
}

// TestVerify_ATargetWhoseKillsAreAllRetracted_KeepsItsRowAndIsNotAnOrphan.
//
// This is the case that looks like an orphan and is not. The log is append-only, so a retraction
// ADDS: the target still has rows, folding them is real work, and the answer — "there is no current
// ToD" — is a real derivation of them. Caching that is exactly what a cache is for; dropping it
// would mean re-clustering the log on every board render forever, and it would make an ordinary
// retraction fire the drift alert.
//
// An orphan is a row for a target with NO rows at all, which no ordinary path produces — which is
// what makes alerting on one honest.
func TestVerify_ATargetWhoseKillsAreAllRetracted_KeepsItsRowAndIsNotAnOrphan(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)
	f.seedCatalogueTimer(target, 5*24*time.Hour, 7*24*time.Hour)

	created := f.report(target, fixtureNow.Add(-2*time.Hour), schemaenum.TodReportSourceLogLine)
	_, err := f.tods.Retract(t.Context(), tod.RetractRequest{
		CircleID: f.circle, ReportID: created.Report.ID, Actor: f.reporter,
	})
	require.NoError(t, err)

	written, err := f.states.Rebuild(t.Context(), f.circle)
	require.NoError(t, err)
	require.Equal(t, 1, written, "the target still has a log, so it is still derived")

	row, ok := f.cached(target)
	require.True(t, ok, "and it still has a row")
	require.Equal(t, schemaenum.TargetStateStatusUnknown, row.Status,
		"saying there is no current ToD, which is what folding the retraction produced")
	require.Zero(t, row.ReportCount)
	require.Nil(t, row.DiedAt)

	report, err := f.states.Verify(t.Context())
	require.NoError(t, err)
	require.True(t, report.Healthy(), "a retraction is not drift")
	require.Zero(t, report.Orphans, "and the row it left behind is not an orphan")
	require.Equal(t, 1, report.TargetsChecked)
	require.Empty(t, f.log.errorLines())

	// The board renders it exactly as the derivation does, so the cached row and an absent one
	// cannot disagree about what the caller sees.
	entry := f.entryFor(f.board(), target)
	require.Equal(t, schemaenum.TargetStateStatusUnknown, entry.Status)
	require.Nil(t, entry.DiedAt)
}
