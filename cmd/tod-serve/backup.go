package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// flagTo is where `backup` writes.
const flagTo = "to"

// newBackupCommand writes a consistent copy of the database somewhere else.
//
// **This is the only undo that exists.** The report log is append-only and migrations are
// forward-only, so there is no path back from a bad upgrade except a file taken before it. The
// deploy workflow calls this on the still-running old container before it pulls anything, and
// treats a failure as fatal.
//
// It is a verb on this binary rather than a runbook step for two reasons. The shipped image is
// `FROM scratch`, so there is no `sqlite3` and no shell to run one from — and a `cp` of a live
// SQLite file in WAL mode is a torn read, because the committed tail is in the `-wal` file beside
// it. [store.DB.BackupTo] uses `VACUUM INTO`, which is consistent against a server that is still
// taking reports.
func newBackupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write a consistent copy of the database to --to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			to, err := cmd.Flags().GetString(flagTo)
			if err != nil {
				return fmt.Errorf("read --%s: %w", flagTo, err)
			}
			if to == "" {
				// Refused rather than defaulted to something beside the database. A backup on the
				// same volume as the database is an undo, not a backup, and a default that put it
				// there would make that the normal case.
				return fmt.Errorf("--%s is required: it names the file to write", flagTo)
			}

			path, err := databasePath(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			db, closeDB, err := openStore(cmd.Context(), path, textLogger(out))
			if err != nil {
				return err
			}
			defer closeDB()

			if err := db.BackupTo(cmd.Context(), to); err != nil {
				return err
			}
			// Said out loud, with both paths. An operator's next question after a deploy is which
			// file to restore, and a silent success answers it with nothing.
			if _, err := fmt.Fprintf(out, "%s backed up to %s\n", path, to); err != nil {
				return fmt.Errorf("write backup result: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().String(flagTo, "", "the file to write the copy to; it must not already exist")
	return cmd
}
