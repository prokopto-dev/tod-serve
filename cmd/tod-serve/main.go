// Command tod-serve is the time-of-death tracking server for Project 1999 raid targets.
//
// There is no working software in this repository yet. This binary exists so that `go build ./...`
// and `make lint` are real checks from the first commit rather than ones that start working later —
// a build target that does not exist cannot regress, and a toolchain nobody exercised is a
// toolchain nobody notices is broken.
//
// See ROADMAP.md for what lands in which phase.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is set at build time via -ldflags.
var version = "0.0.0-dev"

// cmdVersion is the one verb this binary understands. Named rather than repeated so the test
// exercises the real string instead of a copy that can drift away from it.
const cmdVersion = "version"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		// Deliberate waiver: the write below is the error path, and there is nowhere left to
		// report a failure to report. Exiting non-zero is the signal that survives.
		_, _ = fmt.Fprintln(os.Stderr, "tod-serve:", err)
		os.Exit(1)
	}
}

// run is separated from main so a test can drive it with an explicit writer. main() does wiring;
// everything testable lives below it.
//
// Write errors are returned rather than discarded. On a banner that sounds like ceremony, but a
// closed stdout is exactly how `tod-serve version | head -1` behaves, and a binary that exits 0
// having written nothing is the kind of thing a script trusts.
func run(args []string, out io.Writer) error {
	if len(args) > 0 && (args[0] == cmdVersion || args[0] == "--"+cmdVersion) {
		if _, err := fmt.Fprintln(out, version); err != nil {
			return fmt.Errorf("write version: %w", err)
		}
		return nil
	}

	if _, err := fmt.Fprintf(out, `tod-serve %s — pre-1.0, design phase.

There is no working software in this repository yet. What exists is the design, the
roadmap, and the contract that implementation follows.

  make status    what is still stubbed, derived from the Makefile itself
  ROADMAP.md     what lands in which phase
  docs/adr/      why things are the way they are, including the downsides

Two release blockers are open and neither is code; see ROADMAP.md.
`, version); err != nil {
		return fmt.Errorf("write banner: %w", err)
	}
	return nil
}
