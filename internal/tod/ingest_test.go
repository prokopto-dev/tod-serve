package tod_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// requireCode asserts a failure is the problem the closed enum names, rather than any old error.
func requireCode(t *testing.T, err error, want apierr.Code) {
	t.Helper()
	require.Error(t, err)
	got, ok := apierr.From(err)
	require.True(t, ok, "not an *apierr.Error: %v", err)
	require.Equal(t, want, got.Code(), "detail was: %s", got.Error())
}

// TestCreate_TheOrdinaryLogLine_IsStoredVerbatim is the common case, and it is here first because
// the suite's failure mode is testing only the boundaries: the inputs come from a log parser, and
// the shipped defect this project keeps in mind was one nobody met because the tests only covered
// absent and exactly-what-we-serve.
//
// The backtick is not decoration. `Vulak`+"`"+`Aerr` is a real raid target, the parser sends the line
// verbatim, and every layer between the log file and the column has to leave it alone.
func TestCreate_TheOrdinaryLogLine_IsStoredVerbatim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", "VA")

	const line = "[Mon Aug 18 02:14:07 2026] Vulak`Aerr has been slain by Tankguy!"
	// Odd precision, because a parser produces one: a log line has second resolution and a plugin
	// that computed an offset produces microseconds that are not a round number.
	died := fixtureNow.Add(-3*time.Hour) + 417_003
	offset := int64(-3)

	created, err := f.tods.Create(t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: f.reporter, TargetID: target.ID.String(),
		Server: schemaenum.ServerBlue, DiedAt: died,
		Source: schemaenum.TodReportSourceLogLine, SelfConfidence: schemaenum.TodReportSelfConfidenceCertain,
		SourceLine: line, SourceCharacter: "Tankguy", LogCharacter: "Tankgal",
		KilledByGuild: "Riot", ClientClockOffsetSeconds: &offset,
	})
	require.NoError(t, err)
	require.False(t, created.Replayed)

	got := created.Report
	require.Equal(t, target.ID, got.TargetID)
	require.Equal(t, schemaenum.TodReportKindKill, got.Kind)
	require.Equal(t, died, got.DiedAt, "died_at is game truth and is stored exactly as sent")
	require.Equal(t, fixtureNow, got.ReportedAt, "reported_at is system truth, from the clock")
	require.Equal(t, line, got.SourceLine)
	require.Equal(t, "Tankguy", got.SourceCharacter)
	require.Equal(t, "Tankgal", got.LogCharacter)
	require.Equal(t, "Riot", got.KilledByGuild)
	require.Equal(t, &offset, got.ClientClockOffsetSeconds)
	require.False(t, got.Retracted)
	require.Nil(t, got.RetractsReportID)

	// Read back through a second path, so the assertion is about the row and not about the return
	// value the writer happened to build.
	reread, err := f.tods.Get(t.Context(), f.circle, got.ID)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(got, reread))
}

// TestCreate_AWrongClientClockOffset_IsRecordedAndNeverApplied is the design's own residual risk,
// pinned: `client_clock_offset_seconds` is the plugin's ESTIMATE of its own skew, and applying it
// server-side would be the systematic per-reporter correction that is explicitly on the roadmap and
// explicitly not built. A wrong one must therefore change nothing at all.
func TestCreate_AWrongClientClockOffset_IsRecordedAndNeverApplied(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lord Nagafen", "Nagafen's Lair", "Naggy")
	died := fixtureNow.Add(-2 * time.Hour)

	wrong := int64(4 * 60 * 60)
	created, err := f.tods.Create(t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: f.reporter, TargetID: target.ID.String(),
		Server: schemaenum.ServerBlue, DiedAt: died,
		Source: schemaenum.TodReportSourceLogLine, ClientClockOffsetSeconds: &wrong,
	})
	require.NoError(t, err)
	require.Equal(t, died, created.Report.DiedAt,
		"the offset is the reporter's own estimate; the derivation does not read it and neither "+
			"does ingest")
	require.Equal(t, &wrong, created.Report.ClientClockOffsetSeconds)
}

// TestCreate_AReportThreeDaysLate_IsAccepted holds the line canonical §1 draws: `died_at` is game
// truth and is ROUTINELY backdated. A late report is the ordinary case, not an anomaly.
func TestCreate_AReportThreeDaysLate_IsAccepted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lady Vox", "Permafrost Keep", "Vox")

	died := fixtureNow.Add(-72 * time.Hour)
	created := f.report(target, died, schemaenum.TodReportSourceManual)
	require.Equal(t, died, created.Report.DiedAt)
	require.Equal(t, fixtureNow, created.Report.ReportedAt)
}

// TestCreate_DiedAt_TheTwoHardRejectionsAndTheirBoundaries walks each side of both limits.
//
// Exactly at the tolerance is ACCEPTED and one microsecond past it is refused, because a boundary
// tested only from the outside is a boundary nobody knows the sign of.
func TestCreate_DiedAt_TheTwoHardRejectionsAndTheirBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		diedAt core.Micros
		want   apierr.Code
	}{
		{"exactly at the skew tolerance", fixtureNow.Add(tod.FutureTolerance), ""},
		{
			"one microsecond past it", fixtureNow.Add(tod.FutureTolerance) + 1,
			apierr.CodeDiedAtInFuture,
		},
		{"an hour into the future", fixtureNow.Add(time.Hour), apierr.CodeDiedAtInFuture},
		{"exactly ninety days old", fixtureNow.Add(-tod.MaxBackdate), ""},
		{"one microsecond older", fixtureNow.Add(-tod.MaxBackdate) - 1, apierr.CodeDiedAtTooOld},
		{
			"a year old, the classic timezone bug", fixtureNow.Add(-365 * 24 * time.Hour),
			apierr.CodeDiedAtTooOld,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			target := f.seedTarget("Trakanon", "Old Sebilis", "Trak")
			_, err := f.tods.Create(t.Context(), tod.CreateRequest{
				CircleID: f.circle, Reporter: f.reporter, TargetID: target.ID.String(),
				Server: schemaenum.ServerBlue, DiedAt: tc.diedAt,
			})
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			requireCode(t, err, tc.want)
		})
	}
}

// TestCreate_AServerMismatch_IsRefused is the guard against the real fan-out failure: a user
// playing Blue with the Green destination ticked. There is no combined view anywhere in this
// product, so accepting it would be a wrong answer rather than a lenient one.
func TestCreate_AServerMismatch_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Cazic Thule", "Plane of Fear")

	_, err := f.tods.Create(t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: f.reporter, TargetID: target.ID.String(),
		Server: schemaenum.ServerGreen, DiedAt: fixtureNow.Add(-time.Hour),
	})
	requireCode(t, err, apierr.CodeServerMismatch)

	// And the row was never written: a refused report must leave no trace in a log that cannot be
	// edited afterwards.
	rows, err := f.db.Queries().ListTodReportsForCircle(t.Context(), f.circle.String())
	require.NoError(t, err)
	require.Empty(t, rows)
}

// TestCreate_TargetName_RunsTheLadderAndIsRefusedWhenItResolvesNothing checks the branch the plugin
// actually uses: it sends the name it parsed and holds no catalogue.
func TestCreate_TargetName_RunsTheLadderAndIsRefusedWhenItResolvesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", "VA")

	cases := []struct {
		name string
		sent string
		want core.RaidTargetID
		code apierr.Code
	}{
		{"the canonical spelling, backtick included", "Vulak`Aerr", target.ID, ""},
		{"typed with an apostrophe", "Vulak'Aerr", target.ID, ""},
		{"typed with nothing at all", "VulakAerr", target.ID, ""},
		{"typed in the wrong case with a space", "vulak aerr", target.ID, ""},
		{"the alias officers actually type", "VA", target.ID, ""},
		{"a mob nobody has added", "Kerafyrm", core.RaidTargetID{}, apierr.CodeUnknownTarget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// A fresh reporter per case: the natural key is
			// `(circle, target, reporter, died_at)`, so six spellings of one kill from one member
			// would replay rather than resolve.
			reporter := f.seedMember(f.circle, tc.name)
			created, err := f.tods.Create(t.Context(), tod.CreateRequest{
				CircleID: f.circle, Reporter: reporter, TargetName: tc.sent,
				Server: schemaenum.ServerBlue, DiedAt: fixtureNow.Add(-time.Hour),
			})
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, created.Report.TargetID)
		})
	}
}

// TestCreate_BothOrNeitherTarget_IsRefused. Exactly one of `target_id` and `target_name`, and the
// refusal comes from the catalogue's `Ref` so the two callers of the ladder cannot word it
// differently.
func TestCreate_BothOrNeitherTarget_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Talendor", "Skyfire Mountains")

	cases := []struct{ name, id, targetName string }{
		{"both", target.ID.String(), "Talendor"},
		{"neither", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := f.tods.Create(t.Context(), tod.CreateRequest{
				CircleID: f.circle, Reporter: f.reporter,
				TargetID: tc.id, TargetName: tc.targetName,
				Server: schemaenum.ServerBlue, DiedAt: fixtureNow.Add(-time.Hour),
			})
			requireCode(t, err, apierr.CodeValidationFailed)
		})
	}
}

// TestCreate_AMalformedTargetID_IsUnknownTargetNotMalformedRequest: from the client's side an id
// that names nothing and a string that is not an id have exactly the same fix, and answering
// differently would tell a prober that their guess was at least well-formed.
func TestCreate_AMalformedTargetID_IsUnknownTargetNotMalformedRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.tods.Create(t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: f.reporter, TargetID: "not-a-ulid",
		Server: schemaenum.ServerBlue, DiedAt: fixtureNow.Add(-time.Hour),
	})
	requireCode(t, err, apierr.CodeUnknownTarget)
}

// TestCreate_TheSameKillTwice_IsAReplayNotAnError. The natural key is the second line of defence
// behind `Idempotency-Key`: the same reporter cannot lodge the same kill twice even with a botched
// header, and the answer is the row they asked to exist rather than a conflict they cannot act on.
func TestCreate_TheSameKillTwice_IsAReplayNotAnError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Severilous", "Emerald Jungle")
	died := fixtureNow.Add(-90 * time.Minute)

	first := f.report(target, died, schemaenum.TodReportSourceLogLine)
	require.False(t, first.Replayed)

	// Time passes and the client retries with a different header: the natural key is what catches
	// it, and it is keyed on `died_at`, not on when the retry arrived.
	f.clock.Advance(11 * time.Minute)
	second := f.report(target, died, schemaenum.TodReportSourceLogLine)
	require.True(t, second.Replayed, "a duplicate is a replay, not an error")
	require.Equal(t, first.Report.ID, second.Report.ID)
	require.Equal(t, first.Report.ReportedAt, second.Report.ReportedAt,
		"a replay returns the ORIGINAL row; `reported_at` is system truth and never moves")

	rows, err := f.db.Queries().ListTodReportsForCircle(t.Context(), f.circle.String())
	require.NoError(t, err)
	require.Len(t, rows, 1, "the log holds one row, not two")
}

// TestCreate_ACorrectionByTheSameReporter_IsANewRow. The natural key includes `died_at`, so a
// reporter fixing their own mistyped hour is unaffected by it — and both rows survive, because
// corrections are new rows and the log is the audit trail.
func TestCreate_ACorrectionByTheSameReporter_IsANewRow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Gorenaire", "The Dreadlands")

	wrong := f.report(target, fixtureNow.Add(-3*time.Hour), schemaenum.TodReportSourceManual)
	right := f.report(target, fixtureNow.Add(-2*time.Hour), schemaenum.TodReportSourceManual)
	require.NotEqual(t, wrong.Report.ID, right.Report.ID)
	require.False(t, right.Replayed)

	rows, err := f.db.Queries().ListTodReportsForCircle(t.Context(), f.circle.String())
	require.NoError(t, err)
	require.Len(t, rows, 2, "the original stays; the log is never edited")
}

// TestCreate_TwoReportersTwentyNineMinutesApart_AreBothStored is the input the design's ε section
// is written about, at the ingest layer: whether those two reports are one kill or two is the
// derivation's question, and ingest must not pre-empt it by refusing either.
func TestCreate_TwoReportersTwentyNineMinutesApart_AreBothStored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Klandicar", "Great Divide")
	second := f.seedMember(f.circle, "Sneakco")
	died := fixtureNow.Add(-4 * time.Hour)

	first := f.report(target, died, schemaenum.TodReportSourceLogLine)
	other, err := f.tods.Create(t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: second, TargetID: target.ID.String(),
		Server: schemaenum.ServerBlue, DiedAt: died.Add(29 * time.Minute),
		Source: schemaenum.TodReportSourceManual,
	})
	require.NoError(t, err)
	require.NotEqual(t, first.Report.ID, other.Report.ID)

	rows, err := f.db.Queries().ListTodReportsForCircle(t.Context(), f.circle.String())
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

// TestCreate_ARevokedReportersReport_IsStoredAndFlagged. Revocation controls access, never
// history: the report still counts, and the reporter renders as revoked.
func TestCreate_ARevokedReportersReport_IsStoredAndFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Sontalak", "Veeshan's Peak")

	created := f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceLogLine)
	require.False(t, created.Report.ReporterRevoked)

	f.revoke(f.circle, f.reporter)
	after, err := f.tods.Get(t.Context(), f.circle, created.Report.ID)
	require.NoError(t, err)
	require.True(t, after.ReporterRevoked, "the reporter renders as revoked")
	require.Equal(t, created.Report.DiedAt, after.DiedAt, "and the report is otherwise untouched")
}

// TestCreate_AnUnknownSourceOrConfidence_IsRefusedAgainstTheEnumCatalogue. The catalogue generates
// the SQL CHECK and the OpenAPI enum from the same constants, so a value this accepts is a value
// the column accepts — a hand-written list here would be a third copy of one fact.
func TestCreate_AnUnknownSourceOrConfidence_IsRefusedAgainstTheEnumCatalogue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Zlandicar", "Dragon Necropolis")

	_, err := f.tods.Create(t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: f.reporter, TargetID: target.ID.String(),
		Server: schemaenum.ServerBlue, DiedAt: fixtureNow.Add(-time.Hour), Source: "psychic",
	})
	requireCode(t, err, apierr.CodeValidationFailed)

	_, err = f.tods.Create(t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: f.reporter, TargetID: target.ID.String(),
		Server: schemaenum.ServerBlue, DiedAt: fixtureNow.Add(-time.Hour),
		SelfConfidence: "pretty sure",
	})
	requireCode(t, err, apierr.CodeValidationFailed)
}

// TestCreate_AnOmittedSource_IsManualNeverLogLine. Defaulting the other way would let a request
// that never said where its time came from estimate ALONE under consensus §5, outranking every
// hand-typed report in its cluster.
func TestCreate_AnOmittedSource_IsManualNeverLogLine(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Faydedar", "Timorous Deep")

	created := f.report(target, fixtureNow.Add(-time.Hour), "")
	require.Equal(t, schemaenum.TodReportSourceManual, created.Report.Source)
	require.Equal(t, schemaenum.TodReportSelfConfidenceCertain, created.Report.SelfConfidence)
}

// TestCreate_TheAppendIsAtomicWithTheInvalidation asserts the pairing the projection depends on: a
// cached row for a target is gone by the time the report that changes it is visible.
func TestCreate_TheAppendIsAtomicWithTheInvalidation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Dozekar the Cursed", "Temple of Veeshan")

	_, err := f.db.Queries().PutTargetState(t.Context(), sqlitegen.PutTargetStateParams{
		CircleID: f.circle.String(), TargetID: target.ID.String(),
		ComputedAt: int64(fixtureNow), Status: schemaenum.TargetStateStatusUnknown,
		Confidence: schemaenum.TargetStateConfidenceUnknown,
		CreatedAt:  int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(t, err)

	f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceLogLine)

	_, err = f.db.Queries().GetTargetState(t.Context(), sqlitegen.GetTargetStateParams{
		CircleID: f.circle.String(), TargetID: target.ID.String(),
	})
	require.True(t, store.IsNotFound(err),
		"the cached row is dropped in the same transaction as the append")
}
