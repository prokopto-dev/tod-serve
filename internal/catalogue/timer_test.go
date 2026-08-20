package catalogue_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// TestResolveTimer_AnUnseededInstance_IsUnknownEverywhere is the default case, and it is first in
// this file on purpose.
//
// Timer data does not ship — canonical §15 — so an instance whose operator installed the binary
// this morning has a complete catalogue and not one window in it. Every one of those targets must
// resolve to a timer, and that timer must say `none` rather than fail, be nil, or invent a band.
func TestResolveTimer_AnUnseededInstance_IsUnknownEverywhere(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()
	circleID := f.circle("Riot")

	resolved, err := f.svc.ResolveTimers(t.Context(), circleID, core.ServerBlue)
	require.NoError(t, err)
	require.NotEmpty(t, resolved, "the embedded catalogue produced no targets to resolve")

	for id, timer := range resolved {
		require.Equal(t, catalogue.TimerSourceNone, timer.Source,
			"target %s resolved to a timer on an instance nobody seeded", id)
		require.Equal(t, consensus.WindowUnknown, timer.Timer.Kind)
		require.Nil(t, timer.Timer.OpenOffsetSeconds)
		require.Nil(t, timer.Timer.CloseOffsetSeconds)
		require.Equal(t, int64(catalogue.DefaultFixedGraceSeconds), timer.Timer.FixedGraceSeconds)
	}

	// And the single-target path agrees with the bulk one, which is the property that lets
	// getTargetState and listTargetStates render the same answer.
	vulak, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Vulak`Aerr"})
	require.NoError(t, err)
	one, err := f.svc.ResolveTimer(t.Context(), circleID, vulak.Target.ID, core.ServerBlue)
	require.NoError(t, err)
	require.Equal(t, resolved[vulak.Target.ID], one)
}

// TestResolveTimer_AnUnknownTimer_StillCarriesTheQuakeFlag is why [unknownTimer] populates
// IsQuakeTarget rather than returning a zero value.
//
// An unseeded instance knows nothing about windows and still knows a quake repops the target. If
// the flag were lost here, a quake would stop truncating reports on exactly the instances that
// have the least information, and the board would go on showing a ToD the server had wiped.
func TestResolveTimer_AnUnknownTimer_StillCarriesTheQuakeFlag(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()
	circleID := f.circle("Riot")

	resolved, err := f.svc.ResolveTimers(t.Context(), circleID, core.ServerBlue)
	require.NoError(t, err)

	var sawQuakeTarget, sawImmune bool
	for id, timer := range resolved {
		target, getErr := f.svc.Get(t.Context(), id)
		require.NoError(t, getErr)
		require.Equal(t, target.IsQuakeTarget, timer.Timer.IsQuakeTarget,
			"%s lost its quake flag on the way through timer resolution", target.Name)
		sawQuakeTarget = sawQuakeTarget || target.IsQuakeTarget
		sawImmune = sawImmune || !target.IsQuakeTarget
	}
	require.True(t, sawQuakeTarget, "no quake target in the catalogue; the assertion proves nothing")
	require.True(t, sawImmune, "no quake-immune target; the assertion proves nothing")
}

// TestResolveTimer_ThePrecedence_IsOverrideThenCatalogueThenUnknown walks all three rungs on one
// target, in the order they take effect and then back down as each is removed.
//
// Every number here is invented for the test. They are not P99 data and are not meant to be:
// SEED001 keeps real ones out of this repository, and a fixture that used plausible values would
// be the first place somebody copied them from.
func TestResolveTimer_ThePrecedence_IsOverrideThenCatalogueThenUnknown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	circleID := f.circle("Riot")
	actor := f.member(circleID)
	target := f.target("Test Wyrm", "Nowhere")

	// Rung 3 first: nothing written anywhere.
	got, err := f.svc.ResolveTimer(t.Context(), circleID, target.ID, core.ServerBlue)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceNone, got.Source)
	require.Equal(t, consensus.WindowUnknown, got.Timer.Kind)

	// Rung 2: a catalogue timer for blue.
	_, err = f.svc.PutTimer(t.Context(), target.ID, core.ServerBlue, catalogue.WindowRequest{
		WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
		WindowOpenOffsetSeconds:  ptr(int64(100)),
		WindowCloseOffsetSeconds: ptr(int64(200)),
		Source:                   "a fixture, not a wiki",
	})
	require.NoError(t, err)

	got, err = f.svc.ResolveTimer(t.Context(), circleID, target.ID, core.ServerBlue)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceCatalogue, got.Source)
	require.Equal(t, int64(100), *got.Timer.OpenOffsetSeconds)

	// The timer is PER SERVER, so green still has nothing. A resolution that ignored the server
	// would put blue's window on a green circle's board.
	green, err := f.svc.ResolveTimer(t.Context(), circleID, target.ID, core.ServerGreen)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceNone, green.Source)

	// Rung 1: the circle disagrees, which is the whole reason the table exists.
	_, err = f.svc.PutOverride(t.Context(), circleID, target.ID, actor, catalogue.WindowRequest{
		WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
		WindowOpenOffsetSeconds:  ptr(int64(300)),
		WindowCloseOffsetSeconds: ptr(int64(400)),
		Note:                     "we have tracked this for two years",
	})
	require.NoError(t, err)

	got, err = f.svc.ResolveTimer(t.Context(), circleID, target.ID, core.ServerBlue)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceCircleOverride, got.Source)
	require.Equal(t, int64(300), *got.Timer.OpenOffsetSeconds)

	// An override has no server column — the circle is pinned to one — so it wins on every server
	// the circle could be asked about.
	green, err = f.svc.ResolveTimer(t.Context(), circleID, target.ID, core.ServerGreen)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceCircleOverride, green.Source)

	// And a DIFFERENT circle is unaffected: an override is one circle's opinion, not an edit.
	other := f.circle("Ossuary")
	got, err = f.svc.ResolveTimer(t.Context(), other, target.ID, core.ServerBlue)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceCatalogue, got.Source)
	require.Equal(t, int64(100), *got.Timer.OpenOffsetSeconds)

	// Removing the override falls back to the catalogue rather than to nothing.
	_, err = f.svc.DeleteOverride(t.Context(), circleID, target.ID, actor)
	require.NoError(t, err)
	got, err = f.svc.ResolveTimer(t.Context(), circleID, target.ID, core.ServerBlue)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceCatalogue, got.Source)
}

// TestResolveTimers_TheBulkPath_AgreesWithTheSingleOne guards the one duplication risk in this
// package: two entry points to one precedence rule.
func TestResolveTimers_TheBulkPath_AgreesWithTheSingleOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	circleID := f.circle("Riot")
	actor := f.member(circleID)

	overridden := f.target("Overridden Wyrm", "Nowhere")
	catalogued := f.target("Catalogued Wyrm", "Nowhere")
	bare := f.target("Bare Wyrm", "Nowhere")

	for _, id := range []core.RaidTargetID{overridden.ID, catalogued.ID} {
		_, err := f.svc.PutTimer(t.Context(), id, core.ServerBlue, catalogue.WindowRequest{
			WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
			WindowOpenOffsetSeconds:  ptr(int64(10)),
			WindowCloseOffsetSeconds: ptr(int64(20)),
		})
		require.NoError(t, err)
	}
	_, err := f.svc.PutOverride(t.Context(), circleID, overridden.ID, actor,
		catalogue.WindowRequest{
			WindowKind:               schemaenum.RaidTargetTimerWindowKindFixed,
			WindowOpenOffsetSeconds:  ptr(int64(30)),
			WindowCloseOffsetSeconds: ptr(int64(30)),
		})
	require.NoError(t, err)

	bulk, err := f.svc.ResolveTimers(t.Context(), circleID, core.ServerBlue)
	require.NoError(t, err)
	for _, id := range []core.RaidTargetID{overridden.ID, catalogued.ID, bare.ID} {
		one, oneErr := f.svc.ResolveTimer(t.Context(), circleID, id, core.ServerBlue)
		require.NoError(t, oneErr)
		require.Equal(t, one, bulk[id], "the two paths disagree about %s", id)
	}
	require.Equal(t, catalogue.TimerSourceCircleOverride, bulk[overridden.ID].Source)
	require.Equal(t, catalogue.TimerSourceCatalogue, bulk[catalogued.ID].Source)
	require.Equal(t, catalogue.TimerSourceNone, bulk[bare.ID].Source)
}

// TestPutTimer_AWindowTheSchemaWouldRefuse_Is422AndNotA500 covers the four CHECK constraints from
// the Go side.
//
// The constraints are the enforcement. What this asserts is that a caller finds out WHICH field
// was wrong, rather than getting a 500 and a driver error with a constraint name in it — canonical
// §5's rule that a rejection says what to do next.
func TestPutTimer_AWindowTheSchemaWouldRefuse_Is422AndNotA500(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.target("Test Wyrm", "Nowhere")

	tests := []struct {
		name string
		req  catalogue.WindowRequest
	}{
		{
			name: "unknown, with offsets it should not have",
			req: catalogue.WindowRequest{
				WindowKind:               schemaenum.RaidTargetTimerWindowKindUnknown,
				WindowOpenOffsetSeconds:  ptr(int64(10)),
				WindowCloseOffsetSeconds: ptr(int64(20)),
			},
		},
		{
			name: "variance with only one edge",
			req: catalogue.WindowRequest{
				WindowKind:              schemaenum.RaidTargetTimerWindowKindVariance,
				WindowOpenOffsetSeconds: ptr(int64(10)),
			},
		},
		{
			name: "a band that closes before it opens",
			req: catalogue.WindowRequest{
				WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
				WindowOpenOffsetSeconds:  ptr(int64(200)),
				WindowCloseOffsetSeconds: ptr(int64(100)),
			},
		},
		{
			name: "fixed, but with two different offsets — a point that is a band",
			req: catalogue.WindowRequest{
				WindowKind:               schemaenum.RaidTargetTimerWindowKindFixed,
				WindowOpenOffsetSeconds:  ptr(int64(100)),
				WindowCloseOffsetSeconds: ptr(int64(200)),
			},
		},
		{
			name: "variance whose offsets are equal — a band that is a point",
			req: catalogue.WindowRequest{
				WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
				WindowOpenOffsetSeconds:  ptr(int64(100)),
				WindowCloseOffsetSeconds: ptr(int64(100)),
			},
		},
		{
			name: "a window kind nobody defined",
			req: catalogue.WindowRequest{
				WindowKind:               "approximate",
				WindowOpenOffsetSeconds:  ptr(int64(100)),
				WindowCloseOffsetSeconds: ptr(int64(200)),
			},
		},
		{
			name: "a negative grace",
			req: catalogue.WindowRequest{
				WindowKind:        schemaenum.RaidTargetTimerWindowKindUnknown,
				FixedGraceSeconds: ptr(int64(-1)),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := f.svc.PutTimer(t.Context(), target.ID, core.ServerBlue, tt.req)
			require.Error(t, err)
			coded, ok := apierr.From(err)
			require.True(t, ok, "not a coded problem, so the edge would render a 500: %v", err)
			require.Equal(t, apierr.CodeValidationFailed, coded.Code())
			require.NotEmpty(t, coded.Problem().Errors,
				"the rejection names no field, so a caller cannot tell what to fix")
		})
	}
}

// TestPutTimer_AFixedWindow_IsAPointWithAGrace is the shape ADR-0008 needs to make `in_window`
// reachable at all for a fixed timer.
func TestPutTimer_AFixedWindow_IsAPointWithAGrace(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.target("Test Wyrm", "Nowhere")

	got, err := f.svc.PutTimer(t.Context(), target.ID, core.ServerBlue, catalogue.WindowRequest{
		WindowKind:               schemaenum.RaidTargetTimerWindowKindFixed,
		WindowOpenOffsetSeconds:  ptr(int64(500)),
		WindowCloseOffsetSeconds: ptr(int64(500)),
	})
	require.NoError(t, err)
	require.Equal(t, int64(catalogue.DefaultFixedGraceSeconds), got.FixedGraceSeconds,
		"a fixed timer with no grace flips pre_window to overdue with no state in between")
}

// TestPutTimer_ReplacingATimer_OverwritesRatherThanAppends: `raid_target_timer` is mutable, unlike
// the report log. A community number being corrected is a correction, not a second opinion.
func TestPutTimer_ReplacingATimer_OverwritesRatherThanAppends(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.target("Test Wyrm", "Nowhere")

	for _, open := range []int64{100, 150} {
		_, err := f.svc.PutTimer(t.Context(), target.ID, core.ServerBlue,
			catalogue.WindowRequest{
				WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
				WindowOpenOffsetSeconds:  ptr(open),
				WindowCloseOffsetSeconds: ptr(open + 50),
			})
		require.NoError(t, err)
	}
	timers, err := f.svc.Timers(t.Context(), target.ID)
	require.NoError(t, err)
	require.Len(t, timers, 1)
	require.Equal(t, int64(150), *timers[0].WindowOpenOffsetSeconds)
}

// TestOverride_Delete_IsA404WhenThereIsNothingToRemove: "removed" and "there was nothing there"
// are different answers, and a DELETE that reported success for both would let an officer believe
// they had cleared an override they had never set.
func TestOverride_Delete_IsA404WhenThereIsNothingToRemove(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	circleID := f.circle("Riot")
	actor := f.member(circleID)
	target := f.target("Test Wyrm", "Nowhere")

	_, err := f.svc.DeleteOverride(t.Context(), circleID, target.ID, actor)
	require.Error(t, err)
	coded, ok := apierr.From(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeNotFound, coded.Code())
}

// TestOverride_Writes_AreAudited. An override changes every board in the circle and every ToD
// derived after it, so it is a thing that happened to the circle rather than a preference.
func TestOverride_Writes_AreAudited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	circleID := f.circle("Riot")
	actor := f.member(circleID)
	target := f.target("Test Wyrm", "Nowhere")

	_, err := f.svc.PutOverride(t.Context(), circleID, target.ID, actor, catalogue.WindowRequest{
		WindowKind: schemaenum.RaidTargetTimerWindowKindUnknown,
	})
	require.NoError(t, err)
	_, err = f.svc.DeleteOverride(t.Context(), circleID, target.ID, actor)
	require.NoError(t, err)

	rows, err := f.store.Queries().ListAuditLog(t.Context(), sqlitegen.ListAuditLogParams{
		CircleID: circleID.String(),
		// A ULID sorts as its text, so the highest possible one is the "before" that reads
		// everything. There is no unbounded variant of this query, deliberately.
		BeforeID: "ZZZZZZZZZZZZZZZZZZZZZZZZZZ",
		RowLimit: 100,
	})
	require.NoError(t, err)
	actions := make([]string, 0, len(rows))
	for _, row := range rows {
		actions = append(actions, row.Action)
	}
	require.Contains(t, actions, "timer_override.set")
	require.Contains(t, actions, "timer_override.cleared")
}

// TestUnseededInstance_DerivesNoTimerAndStillCountsEveryReport is the second half of the milestone's
// acceptance criterion, and the half that is easy to assume.
//
// "Reports `no_timer` everywhere AND still records ToDs correctly" is two claims. The first is
// about the window; the second is about everything else — the reports still cluster, the evidence
// still counts them, the estimate is still a real instant, and a quake still clears the board. An
// instance with no timer data must be DEGRADED, not broken, and the difference is measurable right
// here, at the seam between this package and internal/consensus.
//
// It drives the real derivation rather than asserting on the resolved timer alone, because the
// resolved timer being shaped correctly and the derivation doing the right thing with it are
// different facts.
func TestUnseededInstance_DerivesNoTimerAndStillCountsEveryReport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()
	circleID := f.circle("Riot")

	vulak, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Vulak`Aerr"})
	require.NoError(t, err)
	resolved, err := f.svc.ResolveTimer(t.Context(), circleID, vulak.Target.ID, core.ServerBlue)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceNone, resolved.Source)

	diedAt := fixtureNow.Add(-2 * time.Hour)
	reports := []consensus.Report{
		newReport(t, f, diedAt, consensus.SourceLogLine),
		newReport(t, f, diedAt.Add(30*time.Second), consensus.SourceManual),
		newReport(t, f, diedAt.Add(45*time.Second), consensus.SourceManual),
	}

	state := consensus.Derive(reports, nil, resolved.Timer, fixtureNow,
		consensus.CircleConfig{MinReportersToSupersede: 1})

	// The window is silent, and says so with the status that means "we have a ToD and no window to
	// hang on it" rather than the one that means "we have no ToD".
	require.Equal(t, consensus.StatusNoTimer, state.Status)
	require.Equal(t, consensus.WindowUnknown, state.Window.Kind)
	require.Nil(t, state.Window.OpenAt)
	require.Nil(t, state.Window.CloseAt)
	require.Nil(t, state.Window.ProgressBP)

	// Everything else is intact. This is the "still records ToDs correctly" half: an officer with
	// no seed still gets a time of death, the evidence behind it, and who reported it.
	require.NotNil(t, state.DiedAt, "an unseeded instance lost the time of death itself")
	require.Equal(t, 3, state.Evidence.ReportCount)
	require.Equal(t, 3, state.Evidence.DistinctReporterCount)
	require.Equal(t, 1, state.Evidence.LogLineCount)
	require.Len(t, state.Evidence.ReportIDs, 3)
	require.NotEqual(t, consensus.ConfidenceUnknown, state.Confidence,
		"an unseeded instance refused to say how good its evidence was, which is a separate "+
			"question from whether it knows the window")

	// And a quake still clears it. The quake flag survives timer resolution precisely so that the
	// instances that know least about windows do not also stop noticing server-wide repops.
	quaked := consensus.Derive(reports,
		[]consensus.Quake{{OccurredAt: fixtureNow.Add(-1 * time.Hour)}},
		resolved.Timer, fixtureNow, consensus.CircleConfig{MinReportersToSupersede: 1})
	require.Equal(t, consensus.StatusUp, quaked.Status,
		"a quake did not clear the board on an unseeded instance")
}

// newReport builds one kill report from a distinct reporter, which is what makes
// DistinctReporterCount meaningful above.
func newReport(
	t *testing.T, f *fixture, diedAt core.Micros, source consensus.Source,
) consensus.Report {
	t.Helper()
	id, err := core.NewID[core.TodReport](f.ids, diedAt)
	require.NoError(t, err)
	reporter, err := core.NewID[core.Membership](f.ids, diedAt)
	require.NoError(t, err)
	return consensus.Report{
		ID: id, Kind: consensus.KindKill, DiedAt: diedAt, ReportedAt: fixtureNow,
		ReporterMembershipID: reporter, Source: source,
	}
}
