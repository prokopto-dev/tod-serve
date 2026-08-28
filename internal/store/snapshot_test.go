package store

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// These are the three properties [DB.InReadSnapshot] exists for, and each is asserted by making
// the database behave rather than by reading the DSN back: a DSN test would pass on the day a
// driver upgrade stopped honouring one of these strings.

// Isolation. This is the whole point: two reads either side of a commit see one state.
func TestInReadSnapshot_AWriteCommittedWhileItIsOpen_IsNotVisibleInsideIt(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	id := newIDs(rand.Reader)
	circleID := id.next(t)
	createCircle(t, ctx, db, circleID, "Riot", 1)

	committed := make(chan struct{})
	snapshotRead := make(chan struct{})

	go func() {
		defer close(committed)
		<-snapshotRead
		// A real transaction on the WRITING pool, which is what a timer edit is.
		_ = db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
			_, err := q.UpdateCircle(ctx, sqlitegen.UpdateCircleParams{
				Name: "Riot", NameNorm: "riot", Description: "", Timezone: "UTC",
				MinReportersToSupersede: 9, RevokeInvalidatesInvites: 1,
				State: schemaenum.CircleStateActive, UpdatedAt: int64(now), CircleID: circleID,
			})
			return err
		})
	}()

	var before, after int64
	require.NoError(t, db.InReadSnapshot(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		row, err := q.GetCircle(ctx, circleID)
		if err != nil {
			return err
		}
		before = row.MinReportersToSupersede
		// The snapshot is pinned by the read above, so everything the writer does from here is
		// invisible until this function returns.
		close(snapshotRead)
		<-committed

		row, err = q.GetCircle(ctx, circleID)
		if err != nil {
			return err
		}
		after = row.MinReportersToSupersede
		return nil
	}))

	require.Equal(t, int64(1), before)
	require.Equal(t, int64(1), after, "a write committed after the snapshot opened leaked into it")

	// And the write really did land — otherwise the assertion above proves only that nothing
	// happened, which is true of any implementation.
	row, err := db.Queries().GetCircle(ctx, circleID)
	require.NoError(t, err)
	require.Equal(t, int64(9), row.MinReportersToSupersede)
}

// Concurrency. `_txlock=immediate` on the writing pool takes the WRITE lock at BEGIN; if the
// snapshot pool ever did the same, this write would sit behind the reader for busy_timeout and
// then fail. That is the cost ADR-0014 refuses to pay, and this is what notices it being paid.
func TestInReadSnapshot_HoldsNoWriteLock_SoAConcurrentWriteCommits(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	id := newIDs(rand.Reader)
	circleID := id.next(t)
	createCircle(t, ctx, db, circleID, "Riot", 1)

	open := make(chan struct{})
	wrote := make(chan error, 1)
	go func() {
		<-open
		wrote <- db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
			_, err := q.UpdateCircle(ctx, sqlitegen.UpdateCircleParams{
				Name: "Riot", NameNorm: "riot", Description: "", Timezone: "UTC",
				MinReportersToSupersede: 4, RevokeInvalidatesInvites: 1,
				State: schemaenum.CircleStateActive, UpdatedAt: int64(now), CircleID: circleID,
			})
			return err
		})
	}()

	require.NoError(t, db.InReadSnapshot(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		if _, err := q.GetCircle(ctx, circleID); err != nil {
			return err
		}
		close(open)
		// Deliberately shorter than busy_timeout (5s): a snapshot pool that took the write lock
		// would not merely be slow here, it would still be waiting.
		select {
		case err := <-wrote:
			return err
		case <-time.After(2 * time.Second):
			t.Error("a write did not commit while a read snapshot was open")
			return nil
		}
	}))
}

// Read-only, enforced by SQLite rather than by review. Without `query_only` a write reached
// through a snapshot would try to upgrade a deferred read transaction, which is the
// SQLITE_BUSY_SNAPSHOT deadlock the writing pool's `_txlock=immediate` exists to prevent.
func TestInReadSnapshot_AWriteThroughIt_IsRefused(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	id := newIDs(rand.Reader)
	circleID := id.next(t)

	err := db.InReadSnapshot(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		_, createErr := q.CreateCircle(ctx, sqlitegen.CreateCircleParams{
			CircleID: circleID, Name: "Riot", NameNorm: "riot", Description: "",
			Server: schemaenum.ServerBlue, Timezone: "UTC",
			MinReportersToSupersede: 1, RevokeInvalidatesInvites: 1,
			State: schemaenum.CircleStateActive, CreatedAt: int64(now), UpdatedAt: int64(now),
		})
		return createErr
	})
	require.Error(t, err, "a write through the snapshot pool was accepted")

	// And it wrote nothing, which is the half an error message does not prove.
	_, readErr := db.Queries().GetCircle(ctx, circleID)
	require.True(t, IsNotFound(readErr))
}

// A private in-memory database is per-connection, so there is no second handle to give. It says so
// rather than falling back to the pool, which would be two unsynchronised reads wearing the name
// of their own fix.
func TestInReadSnapshot_OnAMemoryStore_ReturnsErrNoSnapshot(t *testing.T) {
	t.Parallel()
	db, err := Open(t.Context(), MemoryPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	err = db.InReadSnapshot(t.Context(), func(context.Context, *sqlitegen.Queries) error {
		return nil
	})
	require.ErrorIs(t, err, ErrNoSnapshot)
}

func TestInReadSnapshot_OnAClosedStore_ReturnsErrClosed(t *testing.T) {
	t.Parallel()
	db := openEmpty(t)
	require.NoError(t, db.Close())

	err := db.InReadSnapshot(t.Context(), func(context.Context, *sqlitegen.Queries) error {
		return nil
	})
	require.ErrorIs(t, err, ErrClosed)
}

// createCircle is the smallest write these tests need; the trigger suite's `seed` builds a whole
// graph, which is more than a lock question wants.
func createCircle(t *testing.T, ctx context.Context, db *DB, id, name string, minReporters int64) {
	t.Helper()
	_, err := db.Queries().CreateCircle(ctx, sqlitegen.CreateCircleParams{
		CircleID: id, Name: name, NameNorm: name, Description: "",
		Server: schemaenum.ServerBlue, Timezone: "UTC",
		MinReportersToSupersede: minReporters, RevokeInvalidatesInvites: 1,
		State: schemaenum.CircleStateActive, CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)
}
