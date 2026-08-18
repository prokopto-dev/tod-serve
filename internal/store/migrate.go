package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"

	"github.com/prokopto-dev/tod-serve/db"
)

// ErrSchemaBehind is returned by [DB.Ready] when the database has not had every embedded migration
// applied. It is a sentinel because /readyz answers differently for "the database is unreachable"
// and "the database is fine but this binary is newer than it" — the second is a deploy that has
// not finished, and a load balancer should hold traffic rather than report a fault.
var ErrSchemaBehind = errors.New("database schema is behind the binary")

// Migrate applies every embedded migration that has not been applied.
//
// Migrations are FORWARD-ONLY (ADR-0006): every Down block is a RAISE(ABORT), so there is
// deliberately no Down method here for something to call in a hurry. Recovery from a bad migration
// is a new migration, or a restored snapshot.
func (d *DB) Migrate(ctx context.Context) error {
	provider, err := d.provider()
	if err != nil {
		return err
	}
	// Every migration runs on ONE connection, for the duration.
	//
	// database/sql hands out whichever pooled connection it likes per statement, and a migration
	// that has to turn foreign keys off — a table rebuild; SQLite's own 12-step ALTER requires it,
	// and `PRAGMA foreign_keys` is per-connection and a no-op inside a transaction — would
	// otherwise set the pragma on one connection and run the rebuild on another. That failure is
	// silent and data-dependent: it only bites a database that has rows referencing the table
	// being rebuilt, which is every real one and no fresh test one.
	//
	// Migrations are a startup-time, single-goroutine operation, so serialising them costs
	// nothing. The limit is lifted afterwards, restoring the unbounded pool the WAL settings in
	// dsn() are chosen for.
	d.sql.SetMaxOpenConns(1)
	defer d.sql.SetMaxOpenConns(0)

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations to %s: %w", d.path, err)
	}
	for _, r := range results {
		d.log.InfoContext(ctx, "migration applied",
			slog.String("source", r.Source.Path),
			slog.Int64("version", r.Source.Version),
			slog.Duration("took", r.Duration))
	}
	if len(results) == 0 {
		d.log.InfoContext(ctx, "schema is current", slog.String("database", d.path))
	}
	return nil
}

// SchemaVersion returns the highest migration version applied to this database.
func (d *DB) SchemaVersion(ctx context.Context) (int64, error) {
	provider, err := d.provider()
	if err != nil {
		return 0, err
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("read schema version of %s: %w", d.path, err)
	}
	return version, nil
}

// Ready reports whether the database is fit to serve: reachable, and migrated to the version this
// binary embeds. It is what /readyz calls.
//
// /healthz deliberately does NOT call this. A health check that touches the database lets Docker
// kill the container mid-migration, which is how a half-migrated database happens.
func (d *DB) Ready(ctx context.Context) error {
	if d.sql == nil {
		return ErrClosed
	}
	if err := d.sql.PingContext(ctx); err != nil {
		return fmt.Errorf("reach database %s: %w", d.path, err)
	}
	applied, err := d.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	expected, err := ExpectedSchemaVersion()
	if err != nil {
		return err
	}
	if applied != expected {
		return fmt.Errorf("%w: at %d, binary embeds %d", ErrSchemaBehind, applied, expected)
	}
	return nil
}

// provider builds a goose provider over the embedded migrations.
//
// It is built per call rather than held on the struct: a provider is cheap, and one stored on a
// long-lived struct is package-level mutable state with extra steps. WithDisableGlobalRegistry is
// set because goose's package-level registry is exactly the shared mutable state AGENTS.md bans —
// two stores in one process (which every parallel test is) must not see each other's migrations.
func (d *DB) provider() (*goose.Provider, error) {
	if d.sql == nil {
		return nil, ErrClosed
	}
	fsys, err := db.Migrations()
	if err != nil {
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, d.sql, fsys,
		goose.WithDisableGlobalRegistry(true),
		goose.WithSlog(d.log))
	if err != nil {
		return nil, fmt.Errorf("build migration runner for %s: %w", d.path, err)
	}
	return provider, nil
}

// ExpectedSchemaVersion is the highest migration version this binary embeds.
//
// Derived from the embedded files rather than a constant somebody bumps: a constant that is one
// behind reports a migrated database as behind forever, and a constant that is one ahead reports
// an unmigrated one as ready.
func ExpectedSchemaVersion() (int64, error) {
	names, err := MigrationNames()
	if err != nil {
		return 0, err
	}
	var highest int64
	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			return 0, err
		}
		if version > highest {
			highest = version
		}
	}
	return highest, nil
}

// MigrationNames returns the embedded migration file names, in version order.
func MigrationNames() ([]string, error) {
	fsys, err := db.Migrations()
	if err != nil {
		return nil, err
	}
	entries, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list embedded migrations: %w", err)
	}
	if len(entries) == 0 {
		// A store that migrates nothing and says it is ready is the failure this repository is
		// built against: it looks green over an empty search space.
		return nil, errors.New("list embedded migrations: none are embedded")
	}
	return entries, nil
}

// migrationVersion reads the NNNNNN prefix canonical §16 requires.
func migrationVersion(name string) (int64, error) {
	base := path.Base(name)
	prefix, _, found := strings.Cut(base, "_")
	if !found {
		return 0, fmt.Errorf("read migration version from %s: no NNNNNN_ prefix", base)
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("read migration version from %s: %w", base, err)
	}
	return version, nil
}
