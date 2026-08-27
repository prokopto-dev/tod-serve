package projection_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
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
	Status     string
	Confidence string
	Window     consensus.Window
}

func pairingOf(e projection.BoardEntry) pairing {
	return pairing{DiedAt: e.DiedAt, Status: e.Status, Confidence: e.Confidence, Window: e.Window}
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
	openSeconds, closeSeconds := int64(open.Seconds()), int64(closeAt.Seconds())
	timer := consensus.Timer{
		Kind:               consensus.WindowVariance,
		OpenOffsetSeconds:  &openSeconds,
		CloseOffsetSeconds: &closeSeconds,
		FixedGraceSeconds:  catalogue.DefaultFixedGraceSeconds,
		IsQuakeTarget:      false,
	}
	reports := make([]consensus.Report, 0, len(kills))
	for _, at := range kills {
		reports = append(reports, consensus.Report{
			ID: newID[core.TodReport](f), Kind: consensus.KindKill, DiedAt: at, ReportedAt: at,
			ReporterMembershipID: f.reporter, Source: consensus.SourceManual,
		})
	}
	state := consensus.Derive(reports, nil, timer, fixtureNow,
		consensus.CircleConfig{MinReportersToSupersede: 1})
	return pairing{
		DiedAt: state.DiedAt, Status: string(state.Status),
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
