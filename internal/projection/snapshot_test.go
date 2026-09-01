package projection_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
)

// The two timers the storm below alternates between, chosen so that BOTH halves of the pair move.
//
// ε = clamp(window_open_offset / 4, 5 min, 30 min) — see [consensus.EpsilonSeconds] — so a forty
// minute open offset clusters at ten minutes and a four hour one clusters at thirty. With the two
// kills twenty minutes apart that is two clusters under the narrow timer and one under the wide
// one, so the `died_at` moves as well as the offsets the window is drawn with. A pair of timers
// that changed only the offsets would let a mixed render go unnoticed, because there would be
// nothing about the `died_at` to be mixed.
const (
	narrowOpen  = 40 * time.Minute
	narrowClose = 90 * time.Minute
	wideOpen    = 4 * time.Hour
	wideClose   = 6 * time.Hour
	killsApart  = 20 * time.Minute
)

// pairing is the half of a rendered row that a timer decides: the estimate, and the window drawn
// from it. **A render is correct exactly when both of these come from the same timer**, which is
// what makes it the unit this file asserts on rather than a field at a time.
type pairing struct {
	DiedAt     *core.Micros
	UpSince    *core.Micros
	Status     string
	Confidence string
	Window     consensus.Window
}

func pairingOf(e projection.BoardEntry) pairing {
	return pairing{
		DiedAt: e.DiedAt, UpSince: e.UpSince, Status: e.Status,
		Confidence: e.Confidence, Window: e.Window,
	}
}

func pairingOfDerived(d projection.Derived) pairing {
	return pairing{
		DiedAt: d.DiedAt, UpSince: d.UpSince, Status: d.Status,
		Confidence: d.Confidence, Window: d.Window,
	}
}

// expected is what a timer with these offsets produces for these kills, straight out of
// [consensus.Derive].
//
// **This is the independent oracle, and it has to be.** Deriving the expected values by rendering
// the board — or by reading them back through the same snapshot the render used — would give both
// sides of the assertion one derivation, and it would then pass for any implementation, mixed or
// not. `internal/consensus` is pure: no store, no clock, no query set, nothing this test is about.
func expected(t *testing.T, f *fixture, kills []core.Micros, open, closeAt time.Duration) pairing {
	t.Helper()
	return expectedWithQuake(t, f, kills, open, closeAt, false, nil)
}

func expectedWithQuake(
	t *testing.T, f *fixture, kills []core.Micros, open, closeAt time.Duration,
	isQuakeTarget bool, quakeAt *core.Micros,
) pairing {
	t.Helper()
	openSeconds, closeSeconds := int64(open.Seconds()), int64(closeAt.Seconds())
	timer := consensus.Timer{
		Kind:               consensus.WindowVariance,
		OpenOffsetSeconds:  &openSeconds,
		CloseOffsetSeconds: &closeSeconds,
		FixedGraceSeconds:  catalogue.DefaultFixedGraceSeconds,
		IsQuakeTarget:      isQuakeTarget,
	}
	reports := make([]consensus.Report, 0, len(kills))
	for _, at := range kills {
		reports = append(reports, consensus.Report{
			ID: newID[core.TodReport](f), Kind: consensus.KindKill, DiedAt: at, ReportedAt: at,
			ReporterMembershipID: f.reporter, Source: consensus.SourceManual,
		})
	}
	var quakes []consensus.Quake
	if quakeAt != nil {
		quakes = append(quakes, consensus.Quake{
			ID: newID[core.QuakeEvent](f), OccurredAt: *quakeAt,
		})
	}
	state := consensus.Derive(reports, quakes, timer, fixtureNow,
		consensus.CircleConfig{MinReportersToSupersede: 1})
	return pairing{
		DiedAt: state.DiedAt, UpSince: state.UpSince, Status: string(state.Status),
		Confidence: string(state.Confidence), Window: state.Window,
	}
}

// TestBoard_ATimerCommittingMidRender_NeverPairsAWindowWithADiedAtFromAnotherTimer is the gate for
// issue #17, and it is behavioural on purpose.
//
// A test that only proved [store.DB.InReadSnapshot] exists, or that `Board` calls it, would prove
// nothing about what a render shows while a timer moves under it. So this commits timer writes
// concurrently with renders and asserts the property directly: every row it draws is a derivation
// that happened. Under the defect the render pairs one timer's offsets with a `died_at` clustered
// under the other's ε, which is neither of the two answers below.
//
// The failure it is built to catch is a CONFIDENT one — a window, a confidence and an evidence
// count, with nothing on screen saying the halves disagree — which is why it fails on any row that
// is not exactly one of the two, rather than on a field at a time.
func TestBoard_ATimerCommittingMidRender_NeverPairsAWindowWithADiedAtFromAnotherTimer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)

	kills := []core.Micros{
		fixtureNow.Add(-6 * time.Hour),
		fixtureNow.Add(-6*time.Hour + killsApart),
	}
	for _, at := range kills {
		f.report(target, at, schemaenum.TodReportSourceManual)
	}
	f.seedCatalogueTimer(target, narrowOpen, narrowClose)

	narrow := expected(t, f, kills, narrowOpen, narrowClose)
	wide := expected(t, f, kills, wideOpen, wideClose)
	// If the two answers were not distinguishable in BOTH halves, a mixed render would be
	// indistinguishable from a correct one and every assertion below would be vacuous.
	require.NotEqual(t, narrow.DiedAt, wide.DiedAt,
		"the two timers must cluster differently, or the estimate cannot be mixed")
	require.NotEqual(t, narrow.Window.OpenAt, wide.Window.OpenAt,
		"the two timers must open differently, or the window cannot be mixed")

	render := func() pairing {
		entries, _, err := f.states.Board(t.Context(), f.circle, projection.BoardFilter{Limit: 200})
		require.NoError(t, err)
		return pairingOf(f.entryFor(entries, target))
	}

	// The quiescent render, before anything is racing. It also forces the read-miss rebuild, so
	// the storm below is renders against a cache row that always exists.
	require.Empty(t, cmp.Diff(narrow, render()), "the board is wrong before any concurrency at all")

	flips := f.flipTimerContinuously(target)
	defer flips.stop()

	// Run until BOTH answers have been seen enough times that the writer is demonstrably
	// committing across the reads, rather than for a fixed number of renders that might all have
	// landed on one side of a single write.
	//
	// The cap is renders rather than a deadline because `time.Now` is banned outside
	// internal/clock — CLOCK001 — and because a count reproduces where a duration does not. The
	// writer flips faster than this loop renders, so each answer turns up within a handful of
	// renders; a thousand of them means something has stopped flipping, not that the machine is
	// busy.
	const (
		wantEach   = 25
		maxRenders = 1000
	)
	narrows, wides := 0, 0
	for renders := 0; narrows < wantEach || wides < wantEach; renders++ {
		require.Lessf(t, renders, maxRenders,
			"the timer writer never produced both answers: narrow %d, wide %d", narrows, wides)

		require.NoError(t, flips.err(), "the timer writer failed")
		got := render()
		switch {
		case cmp.Diff(narrow, got) == "":
			narrows++
		case cmp.Diff(wide, got) == "":
			wides++
		default:
			// Neither. The two halves came from different timers, which is a derivation that never
			// happened. Both diffs are printed because which one is closer says which half moved.
			require.Fail(t, "a render paired a window with a died_at from another timer",
				"against narrow:\n%s\nagainst wide:\n%s",
				cmp.Diff(narrow, got), cmp.Diff(wide, got))
		}
	}
}

// TestGetTargetState_AQuakeFlagFlippingMidRead_NeverRendersItAgainstTheOtherDerivation is the gate
// for the second half of the pairing: the TARGET, not the timer row.
//
// `is_quake_target` is not identity. [catalogue.timerOf] copies it into the [consensus.Timer] the
// answer is derived with, and [consensus.Derive] uses it to truncate every kill before the latest
// quake and report `up` instead. Read the target outside the snapshot that resolved the timer and
// a flip by `catalogue.Update` — a transaction of its own — renders `target.is_quake_target` from
// one instant beside a `status` and an `up_since` derived under the other. Same mixed row as
// issue #17, different door.
//
// `getTargetState` is where this is observable, and that is not a coincidence: it re-derives from
// the report log on every call, so the flag it renders and the derivation beside it are produced
// microseconds apart. The board reads a CACHED row, which is a separate defect and was issue #21:
// the flip recomputed nothing, so the row on disk was simply wrong and no snapshot could close it.
// `catalogue.Update` now pushes [projection.Service.OnQuakeTargetChange] inside its own
// transaction, which is why the flips below are doing instance-wide recomputations as well as
// racing this read. TestQuakeTargetFlip_TheBoardAndTheDetailPage_Agree is that gate; this one is
// still about the torn read, which the push does not address.
func TestGetTargetState_AQuakeFlagFlippingMidRead_NeverRendersItAgainstTheOtherDerivation(
	t *testing.T,
) {
	t.Parallel()
	f := newFixture(t)
	target := f.seedTarget("Vulak`Aerr", "Temple of Veeshan", false)

	kills := []core.Micros{
		fixtureNow.Add(-6 * time.Hour),
		fixtureNow.Add(-6*time.Hour + killsApart),
	}
	for _, at := range kills {
		f.report(target, at, schemaenum.TodReportSourceManual)
	}
	// After every kill, so the quake truncates all of them and the two answers are as far apart as
	// this derivation gets: an estimate and a window, or `up` and neither.
	quakeAt := fixtureNow.Add(-2 * time.Hour)
	_, err := f.tods.ReportQuake(t.Context(), todQuake(f, quakeAt))
	require.NoError(t, err)
	f.seedCatalogueTimer(target, narrowOpen, narrowClose)

	quaked := expectedWithQuake(t, f, kills, narrowOpen, narrowClose, true, &quakeAt)
	plain := expectedWithQuake(t, f, kills, narrowOpen, narrowClose, false, &quakeAt)
	require.NotEqual(t, quaked.UpSince, plain.UpSince,
		"the flag must change the answer, or this test is vacuous")
	require.NotEqual(t, quaked.DiedAt, plain.DiedAt)

	flips := f.flipQuakeFlagBetweenReads(target)
	defer flips.stop()

	const (
		reads = 200
		// Both answers have to turn up, or the pacing below has quietly made this a test that
		// reads two hundred times and never sees the flag move. A writer taking one token per read
		// alternates, so the split is even: measured over eight runs under `-race`, every one of
		// them split 99/101 and the threshold is a quarter of the thinner side.
		wantEach = 25
		// A contended read is retried rather than failed, because SQLITE_BUSY is the database
		// saying "not now", not the pairing coming back mixed — issue #40, where losing that
		// distinction failed CI on four separate heads. The retry is BOUNDED and COUNTED so that
		// genuine lock pathology still turns the build red instead of being absorbed: a paced
		// writer leaves this at zero, so any nonzero total is already worth reading.
		maxBusyPerRead = 5
		maxBusyTotal   = 20
	)

	busy := 0
	// render reads the target once, waiting out contention and nothing else.
	render := func(i int) projection.Derived {
		t.Helper()
		for attempt := 1; ; attempt++ {
			derived, err := f.states.Get(t.Context(), f.circle, target.ID, false)
			if err == nil {
				return derived
			}
			// Anything that is not the lock is a failure and is reported as one. A retry loop
			// that swallowed every error would spin five times on a broken read and then blame
			// the database for it.
			require.Truef(t, store.IsBusy(err), "read %d failed: %v", i, err)
			busy++
			require.LessOrEqualf(t, attempt, maxBusyPerRead,
				"read %d lost the lock %d times running", i, attempt)
			require.LessOrEqualf(t, busy, maxBusyTotal,
				"%d contended reads in %d; this is lock pathology, not a busy machine", busy, i+1)
		}
	}

	quakedSeen, plainSeen := 0, 0
	for i := range reads {
		require.NoError(t, flips.err(), "the catalogue writer failed")
		// One flip per read, asked for here rather than run flat out on the writer's own
		// goroutine. See [fixture.flipQuakeFlagBetweenReads].
		flips.next()

		derived := render(i)

		// **The assertion is the pairing itself**: whichever flag this response renders, the
		// derivation beside it has to be the one that flag produces.
		want := plain
		if derived.Target.IsQuakeTarget {
			want, quakedSeen = quaked, quakedSeen+1
		} else {
			plainSeen++
		}
		require.Emptyf(t, cmp.Diff(want, pairingOfDerived(derived)),
			"read %d rendered is_quake_target=%v against the other derivation",
			i, derived.Target.IsQuakeTarget)
	}

	// Pacing the writer gave the rest of the suite its write lock back. It must not also have
	// given it a test that passes over nothing: every assertion in the loop is satisfied by a run
	// in which the flag never moved at all, so the split is asserted here rather than assumed.
	require.GreaterOrEqualf(t, quakedSeen, wantEach,
		"the flag was on for only %d of %d reads; the writer is not racing this read", quakedSeen, reads)
	require.GreaterOrEqualf(t, plainSeen, wantEach,
		"the flag was off for only %d of %d reads; the writer is not racing this read", plainSeen, reads)
}

// timerFlips is a catalogue timer being rewritten, narrow then wide then narrow, on another
// goroutine.
//
// It reports its failure over a channel rather than calling `require`: a `require` outside the test
// goroutine stops that goroutine and leaves the test waiting on whatever it was waiting for, which
// turns a real failure into a timeout with no message.
//
// Each write goes through [catalogue.Service.PutTimer] with the projection as its invalidator, so
// the window and the recomputed cache row commit together — ADR-0013. **That is what makes the
// answers a reader can legitimately see exactly two rather than four**: at every committed instant
// the effective timer and the derived row agree, so a reader that sees anything else built it
// itself.
type timerFlips struct {
	failure  chan error
	stopped  chan struct{}
	finished chan struct{}
}

// flipTimerContinuously starts a writer that rewrites the timer as fast as it can until
// [timerFlips.stop].
//
// **Continuously, not once per read, and that is what makes the board gate fire.** The window a
// commit has to land in is the gap between two of the render's statements; one write started in
// lockstep with each render lands wherever it lands and mostly misses it. A writer that is always
// mid-transaction commits at every offset into a render sooner or later, which is what a sampled
// race needs. Measured on the defect: red within a few renders, against a paced writer that never
// reproduced it at all.
//
// Only a reader that does not itself write can use this — the write lock is held nearly
// continuously — which is exactly the board and not the verify sweep.
func (f *fixture) flipTimerContinuously(target catalogue.Target) *timerFlips {
	flips := &timerFlips{
		failure:  make(chan error, 1),
		stopped:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	go func() {
		defer close(flips.finished)
		wide := true
		for {
			select {
			case <-flips.stopped:
				return
			default:
			}
			open, closeAt := narrowOpen, narrowClose
			if wide {
				open, closeAt = wideOpen, wideClose
			}
			wide = !wide
			if err := f.putCatalogueTimer(target, open, closeAt); err != nil {
				flips.failure <- err
				return
			}
		}
	}()
	return flips
}

// err is the writer's failure, or nil while it is still going.
func (t *timerFlips) err() error {
	select {
	case err := <-t.failure:
		return err
	default:
		return nil
	}
}

func (t *timerFlips) stop() {
	close(t.stopped)
	<-t.finished
}

// quakeFlips is a target's `is_quake_target` being rewritten on another goroutine, ONE FLIP PER
// READ rather than as fast as the machine allows.
//
// [fixture.flipTimerContinuously] is unpaced and stays that way: the pair it races lives between
// two statements of a single render, and only a writer that is permanently mid-transaction commits
// into a gap that small. **This writer races something far wider.** Since issue #21
// `catalogue.Update` recomputes every board on the instance inside its own transaction, so one
// flip overlaps essentially the whole of the read it is racing, and running flat out bought no
// interleaving the pacing does not already give.
//
// What it did buy was issue #40. Every iteration is a full `BEGIN IMMEDIATE` — ADR-0013 puts the
// invalidation inside the writing transaction — and the read it races writes too, because
// [projection.Service.Get] refreshes the cache row it derived. Under `-race`, beside the rest of
// this package running in parallel, the two exhausted `busy_timeout` and the test failed on
// SQLITE_BUSY: a flake that cost four CI re-runs and three investigations across four heads. The
// fix is here rather than in the DSN, which already reasons about the production value — see
// [store.connectionString] — because the contention was manufactured by this file.
type quakeFlips struct {
	*timerFlips
	flip chan struct{}
}

// next asks for one flip.
//
// The send is deliberately non-blocking. If the writer is still committing the last one then the
// read that follows runs against a write in flight, which is the interleaving this test is for;
// waiting for it instead would serialise the two and leave no race to observe. Either way at most
// one write is outstanding, and the whole run commits no more writes than it performs reads.
func (q *quakeFlips) next() {
	select {
	case q.flip <- struct{}{}:
	default:
	}
}

// flipQuakeFlagBetweenReads starts a writer that flips the target's `is_quake_target` once for
// each [quakeFlips.next], until [timerFlips.stop].
//
// **It is `catalogue.Update`, not a bare UPDATE**, because the point is that a supported write
// path moves a derivation input in a transaction of its own — and, since issue #21, recomputes
// every board on the instance inside that same transaction.
func (f *fixture) flipQuakeFlagBetweenReads(target catalogue.Target) *quakeFlips {
	flips := &quakeFlips{
		timerFlips: &timerFlips{
			failure:  make(chan error, 1),
			stopped:  make(chan struct{}),
			finished: make(chan struct{}),
		},
		flip: make(chan struct{}),
	}
	go func() {
		defer close(flips.finished)
		quake, busy := true, 0
		for {
			select {
			case <-flips.stopped:
				return
			case <-flips.flip:
			}
			on := quake
			if _, err := f.catalogue.Update(f.t.Context(), target.ID,
				catalogue.UpdateRequest{IsQuakeTarget: &on}, f.states); err != nil {
				// The writer needs the same distinction the reader does, and for the same reason:
				// a flip that lost the lock did not happen, which is contention, while anything
				// else is the write path being broken. `quake` is left alone so the next token
				// retries the value this one failed to write rather than skipping it.
				if store.IsBusy(err) {
					busy++
					if busy <= maxWriterBusy {
						continue
					}
					err = fmt.Errorf("%d contended writes: %w", busy, err)
				}
				flips.failure <- err
				return
			}
			quake = !quake
		}
	}()
	return flips
}

// maxWriterBusy is how much contention the paced writer absorbs before it reports it. Bounded for
// the reason the reader's budget is: a writer that retried forever would hide the lock pathology
// this test would otherwise be the thing that noticed.
const maxWriterBusy = 20

// putCatalogueTimer is [fixture.seedCatalogueTimer] that returns its error instead of failing the
// test, because it is called from a goroutine that is not the test's.
func (f *fixture) putCatalogueTimer(target catalogue.Target, open, closeAt time.Duration) error {
	openSeconds, closeSeconds := int64(open.Seconds()), int64(closeAt.Seconds())
	_, err := f.catalogue.PutTimer(f.t.Context(), target.ID, core.Server(schemaenum.ServerBlue),
		catalogue.WindowRequest{
			WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
			WindowOpenOffsetSeconds:  &openSeconds,
			WindowCloseOffsetSeconds: &closeSeconds,
			Source:                   "test",
		}, f.states)
	return err
}
