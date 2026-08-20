package main

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/projection"
)

// The verbs, named rather than spelled at each call site so `tod-serve --help` and the tests read
// the same strings.
const (
	verbRebuildStates = "rebuild-states"
	verbVerifyStates  = "verify-states"
)

// newRebuildStatesCommand rebuilds every cached target state from the report log.
//
// It exists because `target_state_cache` is droppable by construction, and a droppable cache needs
// exactly one command that proves it: `DELETE FROM target_state_cache` followed by this is a
// supported thing to do to a production database, and if it ever is not, the cache has become an
// authority and that is the bug.
func newRebuildStatesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   verbRebuildStates,
		Short: "Recompute every cached target state from the report log",
		Long: "Rebuilds `target_state_cache` for every live circle.\n\n" +
			"The cache is never authority — the report log is — so this is always safe to run and\n" +
			"is the answer to a cache you have any doubt about.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			path, err := databasePath(cmd)
			if err != nil {
				return err
			}
			states, closeDB, err := openStates(ctx, cmd, path)
			if err != nil {
				return err
			}
			defer closeDB()
			written, err := states.RebuildAll(ctx)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(),
				"rebuilt %d target states\n", written); err != nil {
				return fmt.Errorf("write the rebuild summary: %w", err)
			}
			return nil
		},
	}
}

// newVerifyStatesCommand is the nightly job, at the command line.
//
// **It exits non-zero when it repairs something**, which is what makes it an alert rather than a
// log line: a cron entry that mails on failure, a systemd timer that goes degraded, or a CI job all
// notice a non-zero exit and none of them notice an ERROR in a log nobody tails. The repair itself
// has already happened by then — the recomputation wins, always — so the non-zero status says
// "something drifted and you should find out why", not "the board is broken".
func newVerifyStatesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   verbVerifyStates,
		Short: "Recompute every target state, diff it against the cache, and repair any drift",
		Long: "Recomputes every target's state from the report log and compares it with\n" +
			"`target_state_cache`. THE RECOMPUTATION WINS: any disagreement is written over the\n" +
			"cached row and reported at ERROR.\n\n" +
			"Exits non-zero if anything was repaired, so a scheduler notices.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			path, err := databasePath(cmd)
			if err != nil {
				return err
			}
			states, closeDB, err := openStates(ctx, cmd, path)
			if err != nil {
				return err
			}
			defer closeDB()
			report, err := states.Verify(ctx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out,
				"checked %d targets across %d circles\n",
				report.TargetsChecked, report.CirclesChecked); err != nil {
				return fmt.Errorf("write the verify summary: %w", err)
			}
			for _, d := range report.Discrepancies {
				if _, err := fmt.Fprintln(out, "  drift: "+d.String()); err != nil {
					return fmt.Errorf("write a discrepancy: %w", err)
				}
			}
			if report.Healthy() {
				return nil
			}
			return fmt.Errorf(
				"the cache disagreed with the report log: %d states repaired, %d orphans removed",
				report.Repaired, report.Orphans)
		},
	}
}

// openStates opens the database and wires only what these two verbs need.
//
// It deliberately does NOT call `wire`: that builds the whole API's dependency graph, which needs
// $TOD_TOKEN_PEPPER, $TOD_SESSION_KEY and a public URL for the OAuth callback. None of those has
// anything to do with recomputing a cache, and requiring them would mean an operator could not
// repair a board without the secrets that mint credentials.
//
// The log goes to stderr and the summary to stdout, so `tod-serve verify-states > report.txt` keeps
// the alert visible on the terminal where a person is standing.
func openStates(ctx context.Context, cmd *cobra.Command, path string) (
	*projection.Service, func(), error,
) {
	log := textLogger(cmd.ErrOrStderr())
	db, closeDB, err := openStore(ctx, path, log)
	if err != nil {
		return nil, nil, err
	}
	if err := db.Ready(ctx); err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("%w: run `tod-serve migrate` first", err)
	}
	clk, ids := clock.System{}, core.NewGenerator(rand.Reader)
	_, _, catalogues, err := dataServices(db, clk, ids, log)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	_, states, err := todServices(db, clk, ids, catalogues, log)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	return states, closeDB, nil
}
