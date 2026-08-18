package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpen_NewDatabase_AppliesEveryPragmaThisWorkloadNeeds(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	// The pragmas are set in the DSN so that every pooled connection gets them. This reads them
	// back over the pool, which is the only way to notice if that ever stopped being true.
	for _, tc := range []struct {
		pragma string
		want   string
		why    string
	}{
		{"journal_mode", "wal", "a long read must not block the writer"},
		{"foreign_keys", "1", "SQLite defaults it off, and every REFERENCES depends on it"},
		{"busy_timeout", "5000", "two writers at once should wait, not fail"},
		{"synchronous", "1", "NORMAL: with WAL this cannot corrupt the database"},
	} {
		var got string
		row := db.sql.QueryRowContext(ctx, "PRAGMA "+tc.pragma)
		require.NoError(t, row.Scan(&got), tc.pragma)
		require.Equal(t, tc.want, got, "PRAGMA %s: %s", tc.pragma, tc.why)
	}
}

func TestMigrate_FreshDatabase_ReachesTheEmbeddedVersion(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	expected, err := ExpectedSchemaVersion()
	require.NoError(t, err)
	require.Positive(t, expected, "no migrations are embedded; the embed directive is wrong")

	applied, err := db.SchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, expected, applied)
	require.NoError(t, db.Ready(ctx))
}

func TestMigrate_RunTwice_AppliesNothingTheSecondTime(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	before, err := db.SchemaVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	after, err := db.SchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// A database nobody migrated must not report ready. /readyz is what holds a load balancer off a
// half-deployed instance, and an instance that answers "ready" while its schema is a version
// behind is exactly the deploy that half works.
func TestReady_UnmigratedDatabase_ReportsSchemaBehind(t *testing.T) {
	t.Parallel()
	db := openEmpty(t)

	err := db.Ready(t.Context())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSchemaBehind), "got %v", err)
}

func TestReady_ClosedStore_ReportsClosedRatherThanPanicking(t *testing.T) {
	t.Parallel()
	db := openEmpty(t)
	require.NoError(t, db.Close())

	// Shutdown ordering is exactly where a late request arrives, so this path has to answer.
	require.True(t, errors.Is(db.Ready(t.Context()), ErrClosed))
	require.NoError(t, db.Close(), "closing twice is not an error")
}

func TestMigrationNames_Embedded_AreNumberedAndOrdered(t *testing.T) {
	t.Parallel()
	names, err := MigrationNames()
	require.NoError(t, err)

	// Contiguous from 1: goose applies in version order, and a gap means a migration that exists
	// in the repository is not embedded in the binary.
	for i, name := range names {
		version, err := migrationVersion(name)
		require.NoError(t, err)
		require.Equal(t, int64(i+1), version, "%s is out of sequence", name)
		require.Regexp(t, `^\d{6}_[a-z0-9_]+\.sql$`, name, "canonical section 16 names migrations NNNNNN_snake_case.sql")
	}
}

func TestIntegrityCheck_MigratedDatabase_Passes(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	seed(t, ctx, db)

	require.NoError(t, db.IntegrityCheck(ctx))
	require.NoError(t, db.ForeignKeyCheck(ctx))
}
