package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrBackupDestinationExists is returned when the file [DB.BackupTo] was asked to write already
// exists.
//
// SQLite refuses to overwrite here, and so does this. The one outcome worse than having no backup
// is a backup that silently replaced the last good one, so a repeated `tod-serve backup --to` with
// the same argument is an error rather than a fresh copy over yesterday's.
var ErrBackupDestinationExists = errors.New("the backup destination already exists")

// BackupTo writes a consistent copy of this database to path.
//
// `VACUUM INTO` rather than a file copy. The database is in WAL mode, so the bytes on disk at
// `tod.db` are not the database — the committed tail is in `tod.db-wal` — and `cp tod.db` while
// the server is running captures a torn read that restores as a database missing whatever was
// written most recently. `VACUUM INTO` runs inside a read transaction and writes a complete,
// integrity-checkable file, against a server that is still taking reports.
//
// The append-only report log IS the product, and migrations are forward-only, so this file is the
// only undo that exists. That is why it lives in the shipped binary rather than in a runbook step
// that needs `sqlite3` on the host: the deploy takes its snapshot inside the container, where this
// binary is the only tool there is.
func (d *DB) BackupTo(ctx context.Context, path string) error {
	if d.sql == nil {
		return ErrClosed
	}
	if path == "" {
		return errors.New("back up database: destination is empty")
	}
	// A relative destination is resolved by SQLite against the PROCESS's working directory, not
	// against the database's own directory. Making it absolute here means the path in the error —
	// and the path the operator then goes looking for — are the same string.
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("back up database to %s: %w", path, err)
	}
	// Checked here as well as by SQLite, for the message: SQLite says "output file already
	// exists", which names neither the file nor what to do about it.
	if _, err := os.Stat(abs); err == nil {
		return fmt.Errorf("back up database to %s: %w", abs, ErrBackupDestinationExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("back up database to %s: %w", abs, err)
	}

	// VACUUM cannot run inside a transaction, so this is deliberately a plain Exec on the pool.
	if _, err := d.sql.ExecContext(ctx, "VACUUM INTO ?", abs); err != nil {
		return fmt.Errorf("back up database %s to %s: %w", d.path, abs, err)
	}
	return nil
}
