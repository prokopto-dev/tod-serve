package catalogue_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// timerWrite is one of the four paths in this repository that moves a respawn window.
//
// Three are routes and the fourth is `tod-serve seed timers`, which has no route and is therefore
// invisible to the API's registry gates — so this table is where the four are held to the same
// rule. A fifth write that forgot to take a [catalogue.TimerInvalidator] would not compile; a
// fifth that took one and did nothing with it is what `moved` below catches.
type timerWrite struct {
	name string
	// write performs the write. It is handed the invalidator so a test can substitute one that
	// fails, or one that reads the database from inside the transaction.
	write func(f *fixture, s writeSubject, inv catalogue.TimerInvalidator) error
	// moved reports whether the window this write moves has moved, as seen through q. For the
	// delete that is the row being GONE, which is why this is a predicate and not a row.
	moved func(t *testing.T, ctx context.Context, q *sqlitegen.Queries, s writeSubject) bool
	// arrange runs before the write, for the paths that need something already there.
	arrange func(f *fixture, s writeSubject)
}

// writeSubject is the world one of those writes acts on.
type writeSubject struct {
	circle core.CircleID
	target catalogue.Target
	actor  core.MembershipID
}

func timerWrites() []timerWrite {
	variance := catalogue.WindowRequest{
		WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
		WindowOpenOffsetSeconds:  ptr(int64(300)),
		WindowCloseOffsetSeconds: ptr(int64(400)),
	}
	overrideExists := func(
		t *testing.T, ctx context.Context, q *sqlitegen.Queries, s writeSubject,
	) bool {
		t.Helper()
		_, err := q.GetCircleTimerOverride(ctx, sqlitegen.GetCircleTimerOverrideParams{
			CircleID: s.circle.String(), TargetID: s.target.ID.String(),
		})
		if err != nil && !store.IsNotFound(err) {
			require.NoError(t, err)
		}
		return err == nil
	}
	timerExists := func(
		t *testing.T, ctx context.Context, q *sqlitegen.Queries, s writeSubject,
	) bool {
		t.Helper()
		_, err := q.GetRaidTargetTimer(ctx, sqlitegen.GetRaidTargetTimerParams{
			TargetID: s.target.ID.String(), Server: string(core.ServerBlue),
		})
		if err != nil && !store.IsNotFound(err) {
			require.NoError(t, err)
		}
		return err == nil
	}

	return []timerWrite{
		{
			name: "PutOverride",
			write: func(f *fixture, s writeSubject, inv catalogue.TimerInvalidator) error {
				_, err := f.svc.PutOverride(
					f.t.Context(), s.circle, s.target.ID, s.actor, variance, inv)
				return err
			},
			moved: overrideExists,
		},
		{
			name: "DeleteOverride",
			arrange: func(f *fixture, s writeSubject) {
				_, err := f.svc.PutOverride(
					f.t.Context(), s.circle, s.target.ID, s.actor, variance, &spyInvalidator{})
				require.NoError(f.t, err)
			},
			write: func(f *fixture, s writeSubject, inv catalogue.TimerInvalidator) error {
				_, err := f.svc.DeleteOverride(f.t.Context(), s.circle, s.target.ID, s.actor, inv)
				return err
			},
			// The delete's move is the row's ABSENCE, so this one is inverted on purpose: a
			// predicate that only ever meant "the row is there" would be vacuous here.
			moved: func(
				t *testing.T, ctx context.Context, q *sqlitegen.Queries, s writeSubject,
			) bool {
				return !overrideExists(t, ctx, q, s)
			},
		},
		{
			name: "PutTimer",
			write: func(f *fixture, s writeSubject, inv catalogue.TimerInvalidator) error {
				_, err := f.svc.PutTimer(f.t.Context(), s.target.ID, core.ServerBlue,
					catalogue.WindowRequest{
						WindowKind:               variance.WindowKind,
						WindowOpenOffsetSeconds:  variance.WindowOpenOffsetSeconds,
						WindowCloseOffsetSeconds: variance.WindowCloseOffsetSeconds,
						Source:                   "a fixture, not a wiki",
					}, inv)
				return err
			},
			moved: timerExists,
		},
		{
			name: "ApplySeed",
			write: func(f *fixture, s writeSubject, inv catalogue.TimerInvalidator) error {
				parsed, err := catalogue.ParseSeed(strings.NewReader(`{
				  "version": 1, "source": "a fixture, not a wiki",
				  "timers": [{"target_id": "` + s.target.ID.String() + `",
				              "server": "blue", "window_kind": "variance",
				              "window_open_offset_seconds": 300,
				              "window_close_offset_seconds": 400}]
				}`))
				require.NoError(f.t, err)
				_, err = f.svc.ApplySeed(f.t.Context(), parsed, inv)
				return err
			},
			moved: timerExists,
		},
	}
}

// arrangeSubject builds the circle, member and target every window-moving write needs.
func arrangeSubject(f *fixture) writeSubject {
	circleID := f.circle("Riot")
	return writeSubject{
		circle: circleID,
		target: f.target("Venril Sathir", "Karnor's Castle", "vs"),
		actor:  f.member(circleID),
	}
}

// TestTimerWrite_TheInvalidation_RunsBeforeTheWriteIsVisible is the crash window, closed.
//
// The gap `TimerPushIsNotTransactional` recorded was a window between two operations: the timer
// write committed, and only then was the projection told. A process that died in between never
// told it, and no retry could — a crash produces no response for anybody to act on. Failing the
// request closed the case a CALLER can see and left that one open.
//
// What closes it is that there is no "in between" any more, and this is what that looks like from
// inside: at the moment the invalidation runs, the change is visible on the transaction's own
// query set and NOT on a pooled connection. Nothing is committed yet. A crash at exactly this
// instant loses the window write and the recomputation together, which is the only pair of
// outcomes that cannot leave a board confidently wrong.
//
// The two reads are the whole point. Asserting only the first would pass for a push that ran after
// the commit; asserting only the second would pass for a push handed a query set that sees
// nothing, which is the bug the ordinary tests would never catch — the recomputation would derive
// every board from the window that was there BEFORE.
func TestTimerWrite_TheInvalidation_RunsBeforeTheWriteIsVisible(t *testing.T) {
	t.Parallel()
	for _, tc := range timerWrites() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			subject := arrangeSubject(f)
			if tc.arrange != nil {
				tc.arrange(f, subject)
			}

			var insideSaw, pooledSaw *bool
			f.inv.inside = func(ctx context.Context, q *sqlitegen.Queries) error {
				inside := tc.moved(t, ctx, q, subject)
				// The pool, deliberately: a second connection, reading the last COMMITTED
				// snapshot. In WAL a reader does not block on the open writer, so this answers
				// rather than hanging — and what it answers is what a process that died here
				// would have left behind.
				pooled := tc.moved(t, ctx, f.store.Queries(), subject)
				insideSaw, pooledSaw = &inside, &pooled
				return nil
			}

			require.NoError(t, tc.write(f, subject, f.inv))
			require.Len(t, f.inv.recorded(), 1, "the write never pushed at all")

			require.NotNil(t, insideSaw, "the hook never ran")
			require.True(t, *insideSaw,
				"the invalidation ran without the window it is invalidating: every board it "+
					"recomputes would be derived from the timer that was there before")
			require.False(t, *pooledSaw,
				"the window was already committed when the invalidation ran, so a process that "+
					"died here would leave the board serving it with nothing to recompute it")
		})
	}
}

// TestTimerWrite_AFailedInvalidation_LeavesTheWindowExactlyAsItWas is the other half.
//
// A failed push must not merely fail the request — it must undo the write, because the caller is
// not the only thing that can go away. The assertion is about the row: after the failure, the
// window is exactly where it was.
func TestTimerWrite_AFailedInvalidation_LeavesTheWindowExactlyAsItWas(t *testing.T) {
	t.Parallel()
	for _, tc := range timerWrites() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			subject := arrangeSubject(f)
			if tc.arrange != nil {
				tc.arrange(f, subject)
			}
			f.inv.reset()

			before := tc.moved(t, t.Context(), f.store.Queries(), subject)
			require.False(t, before,
				"%s: the window had already moved before the write ran, so this test would "+
					"pass over a write that did nothing", tc.name)

			f.inv.failWith(errors.New("the projection is unreachable"))
			require.Error(t, tc.write(f, subject, f.inv))
			require.Len(t, f.inv.recorded(), 1,
				"the write failed before it reached the push, so this ran the wrong experiment")

			require.False(t, tc.moved(t, t.Context(), f.store.Queries(), subject),
				"%s kept its write after the invalidation failed; nothing recomputed the boards "+
					"behind that window and nothing ever will", tc.name)
		})
	}
}

// TestTimerWrite_ANilInvalidator_IsRefused.
//
// "Nobody wired an invalidator" is the exact condition the port exists to make impossible, so it
// is an error at the write rather than a branch that quietly skips the push — the same shape as
// every constructor here that refuses a nil entropy source. A nil check that no-opped would make
// the unwired case the silent one.
func TestTimerWrite_ANilInvalidator_IsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range timerWrites() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			subject := arrangeSubject(f)
			if tc.arrange != nil {
				tc.arrange(f, subject)
			}
			err := tc.write(f, subject, nil)
			require.Error(t, err)
			coded, ok := apierr.From(err)
			require.True(t, ok, "%s answered a bare error, so the edge would render a 500 with "+
				"no code at all", tc.name)
			require.Equal(t, apierr.CodeInternalError, coded.Code())
			require.Contains(t, err.Error(), "no timer invalidator")

			require.False(t, tc.moved(t, t.Context(), f.store.Queries(), subject),
				"%s refused the write and wrote it anyway", tc.name)
		})
	}
}

// TestApplySeed_AFailurePartWay_KeepsTheWindowsItWroteAndNoOthers is the unit-of-atomicity
// decision, made visible.
//
// A seed is not shaped like the three routes. A route moves one window for one circle; a seed
// moves many, and each fans out over every circle pinned to that server. Putting the whole file in
// one transaction would hold SQLite's single write lock across hundreds of recomputations, and
// reports timing out during a seed is a worse failure than the staleness this closes — so the unit
// is ONE window: its rows and its recomputation commit together, and the file does not.
//
// What that buys, and what this asserts: a run that stops part-way leaves every window it wrote
// with its boards already recomputed, and every window it did not reach untouched. There is no
// state in which a board is serving a window this command replaced. What it costs is that the file
// is no longer all-or-nothing, and the remedy is to run it again — which is why the report says
// how far it got rather than only that it failed.
func TestApplySeed_AFailurePartWay_KeepsTheWindowsItWroteAndNoOthers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.target("Venril Sathir", "Karnor's Castle", "vs")
	second := f.target("Lord Nagafen", "Nagafen's Lair", "naggy")
	third := f.target("Lady Vox", "Permafrost Keep", "vox")

	seed := `{"version": 1, "source": "a fixture, not a wiki", "timers": [`
	for i, target := range []catalogue.Target{first, second, third} {
		if i > 0 {
			seed += ","
		}
		seed += `{"target_id": "` + target.ID.String() + `", "server": "blue",
		          "window_kind": "variance", "window_open_offset_seconds": 300,
		          "window_close_offset_seconds": 400}`
	}
	parsed, err := catalogue.ParseSeed(strings.NewReader(seed + `]}`))
	require.NoError(t, err)

	// The second window's recomputation fails. Nothing about the first or the third does.
	pushes := 0
	f.inv.inside = func(context.Context, *sqlitegen.Queries) error {
		pushes++
		if pushes == 2 {
			return errors.New("the projection is unreachable")
		}
		return nil
	}

	report, err := f.svc.ApplySeed(t.Context(), parsed, f.inv)
	require.Error(t, err, "a seed that could not recompute a board must not report success")

	// The report is returned WITH the error, because "how far did it get" is the only thing an
	// operator can act on and a failure that hid its own progress would make the remedy a guess.
	require.Equal(t, 1, report.TimersWritten)
	require.Equal(t, 3, report.TimersTotal)
	require.Len(t, report.Changed, 1)
	require.Equal(t, 3, report.WindowsTotal)
	require.Equal(t, first.ID, report.Changed[0].TargetID)

	present := func(target catalogue.Target) bool {
		_, getErr := f.store.Queries().GetRaidTargetTimer(t.Context(),
			sqlitegen.GetRaidTargetTimerParams{
				TargetID: target.ID.String(), Server: string(core.ServerBlue),
			})
		if getErr != nil && !store.IsNotFound(getErr) {
			require.NoError(t, getErr)
		}
		return getErr == nil
	}
	require.True(t, present(first),
		"the window that committed with its recomputation was rolled back too; the seed is now "+
			"all-or-nothing and holds the write lock for the whole file")
	require.False(t, present(second),
		"the window whose recomputation failed was kept, so its boards are stale and nothing "+
			"will tell them")
	require.False(t, present(third),
		"a window after the failure was written; the run did not stop where it said it did")
}
