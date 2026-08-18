// Command tod-serve is the time-of-death tracking server for Project 1999 raid targets.
//
// There is no working software in this repository yet. This binary exists so that `go build ./...`
// is a real check from the first commit rather than one that starts working later — a build target
// that does not exist cannot regress, and a toolchain that was never exercised is a toolchain
// nobody notices is broken.
//
// See ROADMAP.md for what lands in which phase.
package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags.
var version = "0.0.0-dev"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "tod-serve:", err)
		os.Exit(1)
	}
}

// run is separated from main so a test can drive it with an explicit writer. main() does wiring;
// everything testable lives below it.
func run(args []string, out *os.File) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintln(out, version)
		return nil
	}

	fmt.Fprintf(out, `tod-serve %s — pre-1.0, design phase.

There is no working software in this repository yet. What exists is the design, the
roadmap, and the contract that implementation follows.

  make status    what is still stubbed, derived from the Makefile itself
  ROADMAP.md     what lands in which phase
  docs/adr/      why things are the way they are, including the downsides

Two release blockers are open and neither is code; see ROADMAP.md.
`, version)
	return nil
}
