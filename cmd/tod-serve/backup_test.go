package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// openCopy opens a backup file and hands back the store, closed by the test.
func openCopy(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// TestBackup_RestoresACompleteDatabase — the verb the deploy's only undo is taken with.
//
// The report log is append-only and migrations are forward-only, so the file this writes is the
// only path back from a bad upgrade. "It exited 0" is not the check; the check is that the copy
// opens, passes `PRAGMA integrity_check` and `PRAGMA foreign_key_check`, and holds the same rows.
//
// The comparison is whole-value over the catalogue rather than a count, because a torn copy can
// have the right number of rows.
func TestBackup_RestoresACompleteDatabase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "tod.db")
	copyPath := filepath.Join(dir, "restored.db")

	_, err := captureCLI(t, "migrate", "--db", path)
	require.NoError(t, err)
	_, err = captureCLI(t, "init", "--db", path, "--name", "Backup Test",
		"--circle", "Riot Blue", "--server", "blue")
	require.NoError(t, err)
	_, err = captureCLI(t, "seed", "targets", "--db", path)
	require.NoError(t, err)

	out, err := captureCLI(t, "backup", "--db", path, "--to", copyPath)
	require.NoError(t, err)
	require.Contains(t, out, copyPath, "the operator's next question is which file to restore")

	original := openCopy(t, path)
	restored := openCopy(t, copyPath)

	// A copy that does not survive these two is a copy that restores as a corrupt database, which
	// is the failure a backup exists to not have.
	require.NoError(t, restored.IntegrityCheck(t.Context()))
	require.NoError(t, restored.ForeignKeyCheck(t.Context()))

	// The migration state travels, so the restored file is one an older binary refuses and the
	// matching one serves — rather than a database with rows and no schema_migrations row.
	wantVersion, err := original.SchemaVersion(t.Context())
	require.NoError(t, err)
	gotVersion, err := restored.SchemaVersion(t.Context())
	require.NoError(t, err)
	require.Equal(t, wantVersion, gotVersion)
	require.NoError(t, restored.Ready(t.Context()))

	wantTargets, err := original.Queries().ListAllRaidTargets(t.Context())
	require.NoError(t, err)
	gotTargets, err := restored.Queries().ListAllRaidTargets(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, wantTargets, "the seed wrote nothing; this test would pass over an empty database")
	require.Empty(t, cmp.Diff(wantTargets, gotTargets))

	wantInstance, err := original.Queries().GetInstance(t.Context())
	require.NoError(t, err)
	gotInstance, err := restored.Queries().GetInstance(t.Context())
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(wantInstance, gotInstance))
}

// The deploy takes its snapshot on the STILL-RUNNING old container, so the database is being
// written while the copy is made. `cp` of a WAL-mode SQLite file there is a torn read — the
// committed tail lives in the `-wal` file beside it — and the copy restores as a database missing
// whatever was written most recently, or as one that does not open at all.
//
// `VACUUM INTO` runs inside a read transaction. This asserts what that buys: whatever the writer
// was doing, the copy is a COMPLETE database at some instant, never a half of two.
func TestBackup_WritesInFlight_StillProduceAConsistentCopy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "tod.db")
	copyPath := filepath.Join(dir, "snapshot.db")

	_, err := captureCLI(t, "migrate", "--db", path)
	require.NoError(t, err)

	db := openCopy(t, path)
	// Committed BEFORE the snapshot, so the copy is required to hold every one of them: a test
	// where every row is in flight can only assert "nothing is torn", and would pass over a copy
	// that caught nothing at all.
	const settled = 100
	// Still being written WHILE the snapshot is taken. Nothing is claimed about how many of these
	// it catches — only that whatever it caught is a committed value.
	const inFlight = 200

	// `tod_meta` because it is the one table with no foreign key to satisfy: what is under test is
	// the copy's consistency, not this test's ability to build a valid membership graph.
	for i := range settled {
		require.NoError(t, db.Queries().SetMeta(t.Context(), probeRow(i)))
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := settled; i < settled+inFlight; i++ {
			// The error is deliberately not asserted from here: a t.Error raised from a goroutine
			// after the test function returns is a panic, and this one outlives the backup on
			// purpose. What these rows have to satisfy is checked on the copy below.
			_ = db.Queries().SetMeta(t.Context(), probeRow(i))
		}
	}()

	require.NoError(t, db.BackupTo(t.Context(), copyPath))
	wg.Wait()

	restored := openCopy(t, copyPath)
	require.NoError(t, restored.IntegrityCheck(t.Context()))
	require.NoError(t, restored.ForeignKeyCheck(t.Context()))

	// Everything committed before the snapshot is IN the snapshot. This is the half that makes the
	// loop below non-vacuous: a `VACUUM INTO` that produced an empty file would pass an integrity
	// check and fail here.
	for i := range settled {
		row, err := restored.Queries().GetMeta(t.Context(), probeKey(i))
		require.NoErrorf(t, err, "%s was committed before the snapshot and is not in it", probeKey(i))
		require.Equal(t, strconv.Itoa(i), row.Value)
	}

	// And every row the snapshot caught from the writer still running is a row that was COMMITTED,
	// holding the value committed with it. A torn copy shows up here as a key whose value is not
	// its own index — or as a database that did not open at all, four lines above.
	caught := 0
	for i := settled; i < settled+inFlight; i++ {
		row, err := restored.Queries().GetMeta(t.Context(), probeKey(i))
		if store.IsNotFound(err) {
			continue // Written after the snapshot's instant. Nothing is claimed about those.
		}
		require.NoError(t, err)
		require.Equal(t, strconv.Itoa(i), row.Value, "%s holds a value from a different write", probeKey(i))
		caught++
	}
	t.Logf("the snapshot caught %d of the %d writes that were still in flight", caught, inFlight)

	// The writer really did finish, so "caught none of them" means the snapshot was taken early
	// rather than that the goroutine never ran.
	last, err := db.Queries().GetMeta(t.Context(), probeKey(settled+inFlight-1))
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(settled+inFlight-1), last.Value)
}

// probeKey and probeRow name the rows the snapshot test writes.
func probeKey(i int) string { return "backup.probe." + strconv.Itoa(i) }

func probeRow(i int) sqlitegen.SetMetaParams {
	return sqlitegen.SetMetaParams{Key: probeKey(i), Value: strconv.Itoa(i), UpdatedAt: int64(i)}
}

// What the verb refuses, and why each refusal is the right direction.
func TestBackup_WhatIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "tod.db")
	_, err := captureCLI(t, "migrate", "--db", path)
	require.NoError(t, err)

	t.Run("no destination", func(t *testing.T) {
		t.Parallel()
		// Not defaulted to a file beside the database. A backup on the same volume as the database
		// is an undo, not a backup, and a default would make that the normal case.
		_, err := captureCLI(t, "backup", "--db", path)
		require.Error(t, err)
		require.Contains(t, err.Error(), "--to")
	})

	t.Run("a destination that already exists", func(t *testing.T) {
		t.Parallel()
		to := filepath.Join(t.TempDir(), "taken.db")
		_, err := captureCLI(t, "backup", "--db", path, "--to", to)
		require.NoError(t, err)

		// The one outcome worse than no backup is a backup that silently replaced the last good
		// one. The message has to name the file, because the caller is a deploy script.
		_, err = captureCLI(t, "backup", "--db", path, "--to", to)
		require.ErrorIs(t, err, store.ErrBackupDestinationExists)
		require.Contains(t, err.Error(), to)
	})
}
