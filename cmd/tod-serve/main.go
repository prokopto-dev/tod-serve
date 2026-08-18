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
// See ROADMAP.md for what lands in which phase.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/prokopto-dev/tod-serve/internal/store"
)

// version is set at build time via -ldflags.
var version = "0.0.0-dev"

// The verbs this binary understands. Named rather than repeated so the tests exercise the real
// strings instead of copies that can drift away from them.
const (
	cmdVersion = "version"
	cmdMigrate = "migrate"
	cmdServe   = "serve"
)

// flagDB names the database on the command line.
const flagDB = "--db"

// dbPathEnv names the database, so a container can set it once instead of every invocation
// repeating a flag.
const dbPathEnv = "TOD_DB_PATH"

// defaultDBPath is where the database lands when nothing says otherwise.
const defaultDBPath = "tod.db"

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

	if len(args) > 0 && args[0] == cmdMigrate {
		// context.Background() is permitted here: this is main wiring, and there is no caller
		// above it to inherit a context from.
		return migrate(context.Background(), args[1:], out)
	}

	if len(args) > 0 && args[0] == cmdServe {
		return serve(context.Background(), args[1:], out)
	}

	if _, err := fmt.Fprintf(out, `tod-serve %s — pre-1.0.

  tod-serve serve                 serve the API ($TOD_ADDR, default :8080)
  tod-serve migrate [--db PATH]   apply the embedded migrations ($%s, default %s)
  tod-serve version               print the version and nothing else

`+"`serve`"+` needs $TOD_TOKEN_PEPPER and $TOD_SESSION_KEY, and refuses to start without them.
It does NOT migrate: run `+"`tod-serve migrate`"+` first, deliberately.

  make status    what is still stubbed, derived from the Makefile itself
  ROADMAP.md     what lands in which phase
  docs/adr/      why things are the way they are, including the downsides
`, version, dbPathEnv, defaultDBPath); err != nil {
		return fmt.Errorf("write banner: %w", err)
	}
	return nil
}

// migrate applies every embedded migration to the database at --db, or at $TOD_DB_PATH, or at
// ./tod.db.
//
// It reports the version it reached rather than only succeeding silently, because "did the upgrade
// run" is the question an operator has after a deploy, and an empty success looks identical to a
// binary that did nothing.
func migrate(ctx context.Context, args []string, out io.Writer) error {
	path, err := databasePath(args, os.Getenv(dbPathEnv))
	if err != nil {
		return err
	}

	// Migration output goes to the same writer as everything else this command prints: it IS the
	// command's output, not a side channel, and a migration whose log went somewhere the operator
	// was not looking is how a half-applied upgrade goes unnoticed.
	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))

	db, err := store.Open(ctx, path, log)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			log.ErrorContext(ctx, "close database", slog.Any("error", cerr))
		}
	}()

	if err := db.Migrate(ctx); err != nil {
		return err
	}
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "%s is at schema version %d\n", path, version); err != nil {
		return fmt.Errorf("write migration result: %w", err)
	}
	return nil
}

// databasePath resolves --db, then the environment value, then the default.
//
// The environment is passed in rather than read here so the function is pure and its test can run
// in parallel: t.Setenv and t.Parallel are mutually exclusive, and a rule that says every test is
// parallel is worth one parameter.
func databasePath(args []string, env string) (string, error) {
	switch {
	case len(args) == 0:
		if env != "" {
			return env, nil
		}
		return defaultDBPath, nil
	case args[0] != flagDB:
		// Refused rather than ignored: `--database /srv/tod.db` would otherwise migrate ./tod.db
		// and report success, which is the worst available outcome for an upgrade command.
		return "", fmt.Errorf("unknown argument %q: %s takes only %s <path>",
			args[0], cmdMigrate, flagDB)
	case len(args) == 1:
		return "", fmt.Errorf("%s needs a path", flagDB)
	case len(args) > 2:
		return "", fmt.Errorf("unexpected argument %q after %s", args[2], flagDB)
	default:
		return args[1], nil
	}
}
