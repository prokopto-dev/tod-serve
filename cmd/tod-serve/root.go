package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// The environment this binary reads. Named rather than spelled at each call site, so that
// `grep -rn TOD_ cmd/` lists everything an operator can set.
const (
	envDBPath         = "TOD_DB_PATH"
	envAddr           = "TOD_ADDR"
	envTokenPepper    = "TOD_TOKEN_PEPPER"
	envSessionKey     = "TOD_SESSION_KEY"
	envSPAJoinURL     = "TOD_SPA_JOIN_URL"
	envPublicURL      = "TOD_PUBLIC_URL"
	envMetricsEnabled = "TOD_METRICS_ENABLED"
	envMetricsToken   = "TOD_METRICS_TOKEN"
	envMetricsAddr    = "TOD_METRICS_ADDR"
)

const (
	defaultDBPath      = "tod.db"
	defaultAddr        = ":8080"
	defaultMetricsAddr = ":9090"
	defaultTimezone    = "UTC"
)

// flagDB is the persistent flag every verb resolves its database from.
const flagDB = "db"

// newRootCommand builds the command tree.
//
// Every verb writes to `cmd.OutOrStdout()` and nothing here calls `fmt.Print` — `forbidigo`
// refuses it. That is not style: a command whose output goes to the process's stdout by reference
// cannot be tested without capturing a file descriptor, and the migration log is exactly the
// output an operator most needs to see and a test most needs to assert.
func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "tod-serve",
		Short: "Time-of-death tracking for Project 1999 raid targets",
		Long: "tod-serve — pre-1.0.\n\n" +
			"`serve` needs $" + envTokenPepper + " and $" + envSessionKey + ", and refuses to\n" +
			"start without them. It does NOT migrate: run `tod-serve migrate` first, deliberately.\n\n" +
			"  make status    what is still stubbed, derived from the Makefile itself\n" +
			"  ROADMAP.md     what lands in which phase\n" +
			"  docs/adr/      why things are the way they are, including the downsides\n",
		// The error is printed by main, once, with the binary's name in front of it. Cobra's own
		// rendering would print it a second time and follow it with the usage block, which buries
		// the message an operator needs under a page they did not ask for.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// No verb is not an error: it is somebody finding out what the binary does.
			if err := cmd.Help(); err != nil {
				return fmt.Errorf("write help: %w", err)
			}
			return nil
		},
	}
	root.PersistentFlags().String(flagDB, "",
		"the SQLite database ($"+envDBPath+", default "+defaultDBPath+")")

	root.AddCommand(
		newVersionCommand(),
		newMigrateCommand(),
		newServeCommand(),
		newInitCommand(),
		newCircleCommand(),
		newDoctorCommand(),
	)
	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and nothing else",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The write error is returned rather than discarded. On a banner that sounds like
			// ceremony, but `tod-serve version | head -1` closes stdout, and a binary that exits 0
			// having written nothing is the kind of thing a script trusts.
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), version); err != nil {
				return fmt.Errorf("write version: %w", err)
			}
			return nil
		},
	}
}

// databasePath resolves --db, then the environment, then the default.
//
// The environment is read here rather than passed in because every verb resolves it the same way;
// [resolveDatabasePath] is the pure half, so its test can run in parallel — `t.Setenv` and
// `t.Parallel` are mutually exclusive, and a rule that says every test is parallel is worth one
// function.
func databasePath(cmd *cobra.Command) (string, error) {
	flag, err := cmd.Flags().GetString(flagDB)
	if err != nil {
		return "", fmt.Errorf("read --%s: %w", flagDB, err)
	}
	return resolveDatabasePath(flag, os.Getenv(envDBPath)), nil
}

func resolveDatabasePath(flag, env string) string {
	switch {
	case flag != "":
		return flag
	case env != "":
		return env
	default:
		return defaultDBPath
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
