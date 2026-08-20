package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/store"
)

// newMigrateCommand applies every embedded migration.
//
// It reports the version it reached rather than only succeeding silently, because "did the upgrade
// run" is the question an operator has after a deploy, and an empty success looks identical to a
// binary that did nothing.
func newMigrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply the embedded migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := databasePath(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			// Migration output goes to the same writer as everything else this command prints: it
			// IS the command's output, not a side channel, and a migration whose log went
			// somewhere the operator was not looking is how a half-applied upgrade goes unnoticed.
			log := textLogger(out)

			db, closeDB, err := openStore(cmd.Context(), path, log)
			if err != nil {
				return err
			}
			defer closeDB()

			if err := db.Migrate(cmd.Context()); err != nil {
				return err
			}
			version, err := db.SchemaVersion(cmd.Context())
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "%s is at schema version %d\n", path, version); err != nil {
				return fmt.Errorf("write migration result: %w", err)
			}
			return nil
		},
	}
}

// openStore opens the database and returns the closer every verb defers.
//
// The close error is logged rather than returned: by the time it fires the command has already
// done its work and printed its answer, and replacing a successful run's exit code with a failure
// to release a file handle would be a worse report than the log line.
func openStore(ctx context.Context, path string, log *slog.Logger) (*store.DB, func(), error) {
	db, err := store.Open(ctx, path, log)
	if err != nil {
		return nil, nil, err
	}
	return db, func() {
		if cerr := db.Close(); cerr != nil {
			log.ErrorContext(ctx, "close database", slog.Any("error", cerr))
		}
	}, nil
}

func textLogger(out io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
