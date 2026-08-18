// Package clock is the only place in this repository that may call time.Now.
//
// Everything else takes a [Clock]. The rule exists because time-dependent behaviour that reads the
// wall clock directly can only be tested by waiting, and a test suite that waits is a test suite
// that gets a `-short` flag and then stops running. It is also what makes the consensus derivation
// replayable: `now` is an input to it, not something it reaches for.
//
// Enforced by CLOCK001 — an AST analyser in internal/repogate, run by
// TestCLOCK001_Repository_HasNoTimeNowOutsideClock, so an aliased import does not defeat it.
package clock

import (
	"sync"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// Clock reports the current instant.
//
// It is one method on purpose. Timers and tickers are deliberately absent: under
// testing/synctest the runtime's own timers already run on fake time, so wrapping them would buy
// nothing and would give every caller a second way to express a delay.
type Clock interface {
	// Now returns the current instant in UTC microseconds.
	Now() core.Micros
}

// System reads the wall clock. It is the zero value, so wiring it is `clock.System{}`.
//
// Inside a testing/synctest bubble this reads the bubble's fake clock, which is why a test that
// needs time to pass does not need a different Clock — only a test that needs to control an exact
// instant does.
type System struct{}

// Now returns the current instant.
func (System) Now() core.Micros { return core.MicrosFromTime(time.Now()) }

// Test is a clock that only moves when a test moves it. It is safe for concurrent use, because the
// code under test is usually the thing with the goroutines.
type Test struct {
	mu  sync.Mutex
	now core.Micros
}

// NewTest returns a test clock reading at.
func NewTest(at core.Micros) *Test { return &Test{now: at} }

// Now returns the instant the clock was last set to.
func (c *Test) Now() core.Micros {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d. A negative d moves it back, which is a legitimate thing to
// test: the projection cache and the ULID generator both have to survive an NTP step.
func (c *Test) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set moves the clock to an exact instant.
func (c *Test) Set(at core.Micros) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

// Both implementations satisfy the interface; a test clock that has drifted out of shape is a
// compile error rather than a puzzle in whichever package tried to inject it.
var (
	_ Clock = System{}
	_ Clock = (*Test)(nil)
)
