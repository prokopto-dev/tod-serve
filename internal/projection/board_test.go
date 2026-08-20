package projection_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// TestBoard_WithNoTimersSeededAtAll_RecordsToDsAndRendersNoWindow is the DEFAULT case, not an edge
// one: timer data does not ship (canonical §15), so this is the state of the operator's VPS on the
// day they install the binary.
//
// `no_timer` is deliberately distinct from `unknown`. The client can still render "died 4 hours
// ago" and must not render a window, and the two are told apart by the status alone.
func TestBoard_WithNoTimersSeededAtAll_RecordsToDsAndRendersNoWindow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reported := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)
	silent := f.seedTarget("Lord Nagafen", "Nagafen's Lair", false)

	died := fixtureNow.Add(-4 * time.Hour)
	f.report(reported, died, schemaenum.TodReportSourceLogLine)

	entries := f.board()
	require.Len(t, entries, 2, "a target nobody has reported is still on the board")

	withToD := f.entryFor(entries, reported)
	require.Equal(t, schemaenum.TargetStateStatusNoTimer, withToD.Status,
		"we have a ToD and no window to hang on it")
	require.NotNil(t, withToD.DiedAt)
	require.Equal(t, died, *withToD.DiedAt, "so the client can say 'died 4 hours ago'")
	require.Nil(t, withToD.Window.OpenAt, "and must not render a window")
	require.Nil(t, withToD.Window.CloseAt)
	require.Nil(t, withToD.Window.ProgressBP)
	require.Equal(t, string(consensus.WindowUnknown), string(withToD.Window.Kind))
	require.Equal(t, string(catalogue.TimerSourceNone), withToD.TimerSource)

	without := f.entryFor(entries, silent)
	require.Equal(t, schemaenum.TargetStateStatusUnknown, without.Status,
		"no ToD at all is `unknown`, which is a different thing the client renders differently")
	require.Nil(t, without.DiedAt)
	require.Nil(t, without.ComputedAt, "and there is nothing worth a cache row")
}

// TestBoard_ACircleOverride_BeatsTheCatalogueTimer is the FOOTGUN gate.
//
// `CatalogueEntry.Timer` is the catalogue's own row with the circle's override deliberately not
// applied — it exists for `listRaidTargets?server=`. Feeding it to the derivation would make every
// circle override silently stop working while the board went on looking authoritative, which is
// this project's named failure mode. The effective timer is `ResolveTimers`, and this test fails if
// the board ever reads the other one.
func TestBoard_ACircleOverride_BeatsTheCatalogueTimer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)

	const catalogueOpen = 5 * 24 * time.Hour
	const overrideOpen = 3 * 24 * time.Hour
	f.seedCatalogueTimer(target, catalogueOpen, 7*24*time.Hour)
	f.seedOverride(target, overrideOpen, 4*24*time.Hour)

	died := fixtureNow.Add(-time.Hour)
	f.report(target, died, schemaenum.TodReportSourceLogLine)

	entry := f.entryFor(f.board(), target)
	require.Equal(t, string(catalogue.TimerSourceCircleOverride), entry.TimerSource)
	require.NotNil(t, entry.Window.OpenAt)
	require.Equal(t, died.Add(overrideOpen), *entry.Window.OpenAt,
		"the circle's own number, not the catalogue's")
	require.NotEqual(t, died.Add(catalogueOpen), *entry.Window.OpenAt)

	// And the single-target read agrees, so the two never disagree about which timer applies.
	derived, err := f.states.Get(t.Context(), f.circle, target.ID, false)
	require.NoError(t, err)
	require.Equal(t, string(catalogue.TimerSourceCircleOverride), derived.TimerSource)
	require.Equal(t, *entry.Window.OpenAt, *derived.Window.OpenAt)
}

// TestBoard_StatusAndCountdowns_AreReRenderedAgainstNowNotCached.
//
// A cached `pre_window` is stale the instant the window opens, with no write in between. The cache
// therefore holds the point estimate and every read re-renders the status and the window from it —
// and the countdowns are signed offsets from `as_of`, never absolutes a client subtracts from its
// own clock.
func TestBoard_StatusAndCountdowns_AreReRenderedAgainstNowNotCached(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Lady Vox", "Permafrost Keep", false)
	f.seedCatalogueTimer(target, 16*time.Hour, 24*time.Hour)

	died := fixtureNow.Add(-time.Hour)
	f.report(target, died, schemaenum.TodReportSourceLogLine)

	before := f.entryFor(f.board(), target)
	require.Equal(t, schemaenum.TargetStateStatusPreWindow, before.Status)
	require.NotNil(t, before.Window.SecondsUntilOpen)
	require.Positive(t, *before.Window.SecondsUntilOpen, "the window has not opened yet")
	require.NotNil(t, before.ComputedAt)

	// Nothing is written. Only the clock moves, past `open_at`.
	f.clock.Advance(17 * time.Hour)
	after := f.entryFor(f.board(), target)
	require.Equal(t, schemaenum.TargetStateStatusInWindow, after.Status,
		"the status moved with the clock, with no report and no rebuild")
	require.Negative(t, *after.Window.SecondsUntilOpen, "signed: negative means passed")
	require.Positive(t, *after.Window.SecondsUntilClose)
	require.NotNil(t, after.Window.ProgressBP)
	require.Greater(t, *after.Window.ProgressBP, int32(0))
	require.Equal(t, *before.ComputedAt, *after.ComputedAt,
		"and the cached row behind it was not rewritten to say so")

	f.clock.Advance(10 * time.Hour)
	overdue := f.entryFor(f.board(), target)
	require.Equal(t, schemaenum.TargetStateStatusOverdue, overdue.Status,
		"overdue is real intel, not an error state")
	require.Equal(t, int32(10000), *overdue.Window.ProgressBP, "clamped, never past 100%")
}

// TestBoard_AQuakeTarget_IsUpAndANonQuakeTargetIsNot. A server-wide repop resets what it resets:
// `is_quake_target` is a raid_target fact, and it keeps working on an instance that knows no
// windows at all.
func TestBoard_AQuakeTarget_IsUpAndANonQuakeTargetIsNot(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	repopped := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", true)
	unaffected := f.seedTarget("Cazic Thule", "Plane of Fear", false)

	died := fixtureNow.Add(-6 * time.Hour)
	f.report(repopped, died, schemaenum.TodReportSourceLogLine)
	f.report(unaffected, died, schemaenum.TodReportSourceLogLine)

	quake, err := f.tods.ReportQuake(t.Context(), todQuake(f, fixtureNow.Add(-time.Hour)))
	require.NoError(t, err)

	entries := f.board()
	up := f.entryFor(entries, repopped)
	require.Equal(t, schemaenum.TargetStateStatusUp, up.Status)
	require.NotNil(t, up.UpSince)
	require.Equal(t, quake.OccurredAt, *up.UpSince, "up_since is the quake that repopped it")
	require.Nil(t, up.DiedAt, "the ToD before the quake describes a life the target no longer has")

	still := f.entryFor(entries, unaffected)
	require.Equal(t, schemaenum.TargetStateStatusNoTimer, still.Status,
		"a target a quake does not repop is untouched by one")
	require.NotNil(t, still.DiedAt)
}

// TestBoard_EveryFilter_NarrowsWhatItSays, including the two the cache cannot answer in SQL:
// `status` is re-derived against the current instant on every read, so filtering on the stored
// column would filter on what was true when the row was written.
func TestBoard_EveryFilter_NarrowsWhatItSays(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vulak := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)
	naggy := f.seedTarget("Lord Nagafen", "Nagafen's Lair", false)
	f.seedCatalogueTimer(vulak, 5*24*time.Hour, 7*24*time.Hour)
	f.report(vulak, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceLogLine)

	cases := []struct {
		name   string
		filter projection.BoardFilter
		want   []core.RaidTargetID
	}{
		{
			"by status",
			projection.BoardFilter{Status: schemaenum.TargetStateStatusPreWindow},
			[]core.RaidTargetID{vulak.ID},
		},
		{
			"by the status of a target nobody reported",
			projection.BoardFilter{Status: schemaenum.TargetStateStatusUnknown},
			[]core.RaidTargetID{naggy.ID},
		},
		{
			"by zone, matched normalised",
			projection.BoardFilter{Zone: "temple of veeshan"},
			[]core.RaidTargetID{vulak.ID},
		},
		{
			"by expansion",
			projection.BoardFilter{Expansion: schemaenum.RaidTargetExpansionVelious},
			[]core.RaidTargetID{vulak.ID, naggy.ID},
		},
		{
			"by q, a substring of the name",
			projection.BoardFilter{Query: "nagafen"},
			[]core.RaidTargetID{naggy.ID},
		},
		{
			"by q with the punctuation typed differently",
			projection.BoardFilter{Query: "vulak'a"},
			[]core.RaidTargetID{vulak.ID},
		},
		{
			"uncontested only",
			projection.BoardFilter{Contested: boolPtr(false)},
			[]core.RaidTargetID{vulak.ID, naggy.ID},
		},
		{"contested only", projection.BoardFilter{Contested: boolPtr(true)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filter := tc.filter
			filter.Limit = 200
			entries, hasMore, err := f.states.Board(t.Context(), f.circle, filter)
			require.NoError(t, err)
			require.False(t, hasMore)
			ids := make([]core.RaidTargetID, 0, len(entries))
			for _, e := range entries {
				ids = append(ids, e.Target.ID)
			}
			if tc.want == nil {
				require.Empty(t, ids)
				return
			}
			require.ElementsMatch(t, tc.want, ids)
		})
	}
}

// TestBoard_SortsByWindowOpenAt_WithNoWindowLast. Sorting nulls first would put every unseeded
// target — which on a fresh instance is all of them — above the ones a raid leader is waiting on.
func TestBoard_SortsByWindowOpenAt_WithNoWindowLast(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	soon := f.seedTarget("Lady Vox", "Permafrost Keep", false)
	later := f.seedTarget("Lord Nagafen", "Nagafen's Lair", false)
	never := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)
	f.seedCatalogueTimer(soon, 4*time.Hour, 8*time.Hour)
	f.seedCatalogueTimer(later, 20*time.Hour, 30*time.Hour)

	died := fixtureNow.Add(-time.Hour)
	f.report(soon, died, schemaenum.TodReportSourceLogLine)
	f.report(later, died, schemaenum.TodReportSourceLogLine)
	f.report(never, died, schemaenum.TodReportSourceLogLine)

	entries := f.board()
	require.Len(t, entries, 3)
	require.Equal(t, soon.ID, entries[0].Target.ID)
	require.Equal(t, later.ID, entries[1].Target.ID)
	require.Equal(t, never.ID, entries[2].Target.ID, "no window sorts last, never first")
}

// TestBoard_PagesInSortOrderWithoutRepeatingATarget. The cursor is the previous page's last target
// id and the next page is what follows it IN THIS ORDER, which is why the cut is a search for the
// row rather than a comparison against it.
func TestBoard_PagesInSortOrderWithoutRepeatingATarget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for i := range 5 {
		target := f.seedTarget(namesForPaging[i], "Temple of Veeshan", false)
		f.seedCatalogueTimer(target, time.Duration(5-i)*time.Hour, time.Duration(10-i)*time.Hour)
		f.report(target, fixtureNow.Add(-time.Minute), schemaenum.TodReportSourceLogLine)
	}

	seen := map[core.RaidTargetID]bool{}
	var order []core.RaidTargetID
	cursor := core.RaidTargetID{}
	for pages := 0; ; pages++ {
		require.Less(t, pages, 10, "the cursor is not advancing")
		entries, hasMore, err := f.states.Board(t.Context(), f.circle,
			projection.BoardFilter{Cursor: cursor, Limit: 2})
		require.NoError(t, err)
		for _, e := range entries {
			require.False(t, seen[e.Target.ID], "the cursor repeated %s", e.Target.Name)
			seen[e.Target.ID] = true
			order = append(order, e.Target.ID)
		}
		if !hasMore {
			break
		}
		require.NotEmpty(t, entries)
		cursor = entries[len(entries)-1].Target.ID
	}
	require.Len(t, seen, 5)

	whole := f.board()
	require.Len(t, whole, 5)
	for i, e := range whole {
		require.Equal(t, e.Target.ID, order[i], "paging produced the same order as one page")
	}
}

// TestBoard_AnotherCirclesReports_DoNotReachIt. The catalogue is instance-wide, so both circles see
// the same targets — and each sees only its own ToDs.
func TestBoard_AnotherCirclesReports_DoNotReachIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Trakanon", "Old Sebilis", false)
	f.report(target, fixtureNow.Add(-time.Hour), schemaenum.TodReportSourceLogLine)

	theirs := f.seedCircle("Rival", schemaenum.ServerBlue)
	entries, _, err := f.states.Board(t.Context(), theirs, projection.BoardFilter{Limit: 200})
	require.NoError(t, err)
	require.Len(t, entries, 1, "the mob exists for everybody: its existence is a game fact")
	require.Equal(t, schemaenum.TargetStateStatusUnknown, entries[0].Status,
		"and the ToD does not")
	require.Nil(t, entries[0].DiedAt)
}

var namesForPaging = []string{
	"Vulak`Aerr", "Lord Nagafen", "Lady Vox", "Trakanon", "Gorenaire",
}

func boolPtr(b bool) *bool { return &b }
