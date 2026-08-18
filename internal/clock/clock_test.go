package clock_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// A synctest bubble starts its fake clock at this instant.
const bubbleStart = "2000-01-01T00:00:00.000000Z"

func TestSystem_Now_ReadsTheClockItIsRunningUnder(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		require.Equal(t, bubbleStart, clock.System{}.Now().String())
	})
}

// Time-dependent tests use testing/synctest, so the system clock does not need replacing to make
// time pass — which is the reason [clock.Clock] is one method and owns no timers.
func TestSystem_Now_MovesWithTheBubbleClock(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		before := clock.System{}.Now()

		timer := time.NewTimer(90 * time.Second)
		defer timer.Stop()
		<-timer.C

		require.Equal(t, 90*time.Second, clock.System{}.Now().Sub(before))
	})
}

func TestTest_Advance_MovesOnlyWhenTheTestSaysSo(t *testing.T) {
	t.Parallel()
	at := core.Micros(1_755_483_247_000_000)
	c := clock.NewTest(at)

	require.Equal(t, at, c.Now())
	require.Equal(t, at, c.Now(), "a test clock does not tick between reads")

	c.Advance(90 * time.Second)
	require.Equal(t, at.Add(90*time.Second), c.Now())

	// Backwards too: an NTP step is a thing the projection cache and the id generator both have to
	// survive, so it has to be expressible.
	c.Advance(-2 * time.Hour)
	require.Equal(t, at.Add(90*time.Second).Add(-2*time.Hour), c.Now())

	c.Set(at)
	require.Equal(t, at, c.Now())
}

func TestTest_Concurrent_ReadsAndAdvancesSafely(t *testing.T) {
	t.Parallel()
	c := clock.NewTest(0)

	// The code under test is usually the thing with the goroutines, so the clock has to be safe
	// under -race rather than merely correct.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			c.Advance(time.Millisecond)
		}
	}()
	for range 100 {
		_ = c.Now()
	}
	<-done

	require.Equal(t, core.Micros(0).Add(100*time.Millisecond), c.Now())
}
