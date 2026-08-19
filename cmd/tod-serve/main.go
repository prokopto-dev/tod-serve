// Command tod-serve is the time-of-death tracking server for Project 1999 raid targets.
//
// It serves the API and migrates the database, and the two are separate verbs on purpose: a server
// that migrates on boot upgrades a database whenever a container restarts, which is how a
// half-tested schema change reaches production without anybody deciding to run it.
//
// ADR-0006 makes goose a library this binary embeds rather than a tool the deployment has to
// provide, because an officer double-clicking tod-serve.exe has no migration CLI on their PATH,
// and a migration path that only works on a developer's machine is one nobody finds out about
// until an upgrade.
//
// This package is wiring and nothing else. Every decision it makes is which component to hand to
// which other component; the logic is in internal/. The one thing it decides on its own is where
// randomness comes from, and RAND001 checks that answer — see [wiring.go].
//
// See ROADMAP.md for what lands in which phase.
package main

import (
	"context"
	"fmt"
	"os"
)

// version is set at build time via -ldflags.
var version = "0.0.0-dev"

func main() {
	// context.Background() is permitted here: this is main wiring, and there is no caller above
	// it to inherit a context from.
	if err := newRootCommand().ExecuteContext(context.Background()); err != nil {
		// Deliberate waiver: the write below is the error path, and there is nowhere left to
		// report a failure to report. Exiting non-zero is the signal that survives.
		_, _ = fmt.Fprintln(os.Stderr, "tod-serve:", err)
		os.Exit(1)
	}
}
