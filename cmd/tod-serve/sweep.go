package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/sweep"
)

// verbSweep is named rather than spelled at each call site, so `tod-serve --help` and the tests
// read the same string.
const verbSweep = "sweep"

// newSweepCommand deletes rows that have outlived their expiry.
//
// **It exits zero whether it deleted nothing or ten thousand rows**, which is the deliberate
// opposite of `verify-states`. That command's non-zero exit is an alert: it repaired something, so
// the cache drifted and somebody has to find out why. Removing expired litter is the routine
// healthy case, and a scheduler that went degraded every night because the sweep did its job is a
// scheduler somebody switches off. What it deleted is on stdout and in the structured log; a
// failure to sweep at all is what the non-zero exit is reserved for.
func newSweepCommand() *cobra.Command {
	return &cobra.Command{
		Use:   verbSweep,
		Short: "Delete expired auth flows, credential tickets and idempotency records",
		Long: "Removes rows from the three prunable tables once they have been expired for\n" +
			"longer than the grace period.\n\n" +
			"Every reader of these tables already refuses a row past `expires_at`, so this\n" +
			"deletes rows that are dead to the application either way — it is always safe to\n" +
			"run, including against a live server. Run it from cron or a systemd timer.\n\n" +
			"Exits zero whenever the sweep ran, however much or little it removed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			path, err := databasePath(cmd)
			if err != nil {
				return err
			}
			sweeper, closeDB, err := openSweeper(ctx, cmd, path)
			if err != nil {
				return err
			}
			defer closeDB()
			report, err := sweeper.Sweep(ctx)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(),
				"swept %d expired rows: %d auth flows, %d credential tickets, "+
					"%d idempotency records, %d session revocations\n",
				report.Total(), report.AuthFlows, report.CredentialTickets,
				report.IdempotencyRecords, report.SessionRevocations); err != nil {
				return fmt.Errorf("write the sweep summary: %w", err)
			}
			return nil
		},
	}
}

// openSweeper opens the database and wires only what the sweep needs.
//
// Like `openStates` it deliberately does not call `wire`: the full graph needs
// $TOD_TOKEN_PEPPER, $TOD_SESSION_KEY and a public callback URL, none of which has anything to do
// with deleting expired rows. An operator should not need the secrets that mint credentials in
// order to prune the table those credentials left behind.
func openSweeper(ctx context.Context, cmd *cobra.Command, path string) (
	*sweep.Service, func(), error,
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
	sweeper, err := sweep.New(sweep.Config{Store: db, Clock: clock.System{}, Log: log})
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	return sweeper, closeDB, nil
}
