// Package store is the only package in this repository that holds a *sql.DB.
//
// Everything above it takes a [*DB] or the typed query set it exposes, so "which layer talks to
// the database" is a compile-time fact rather than a review habit. SQL001 in
// scripts/repo-gates.sh and TestSQL001_DatabaseSQL_IsImportedOnlyByTheStore are the mechanism.
//
// The generated half lives in internal/store/sqlitegen and is never hand-edited: `make gen` writes
// it from db/queries with sqlc.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	// modernc.org/sqlite is a pure-Go SQLite. It costs some speed against the cgo driver and buys
	// the thing this project is built around: `go build` produces one static binary an officer can
	// double-click, cross-compiled from anywhere, with no toolchain on the target machine.
	//
	// It is imported by NAME rather than blankly so that [IsUniqueViolation] can read the driver's
	// own error code. This is the only package permitted to know the driver exists at all.
	"modernc.org/sqlite"

	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// closeRows releases a result set.
//
// Its error is deliberately discarded, and this is the one place that says why: every caller
// checks rows.Err() after iterating, which reports anything that went wrong, and Close on a
// drained result set can only report the same failure a second time. A `defer rows.Close()` at
// each call site would be seven undocumented waivers instead of one documented one.
func closeRows(rows *sql.Rows) { _ = rows.Close() }

// driverName is the database/sql driver modernc.org/sqlite registers.
const driverName = "sqlite"

// MemoryPath opens a private in-memory database. It exists for tests that genuinely do not touch
// migrations; the integration suite uses a real file in t.TempDir(), because a schema this
// trigger-dependent deserves to be exercised the way it will actually be run.
//
// **A store opened here has no snapshot pool and [DB.InReadSnapshot] returns [ErrNoSnapshot].**
// `:memory:` is private to a CONNECTION, so a second handle would open a second, empty database
// rather than another view of this one — and a snapshot of the wrong database is worse than no
// snapshot, because it answers.
const MemoryPath = ":memory:"

// ErrClosed is returned by operations on a store that has been closed.
var ErrClosed = errors.New("store is closed")

// ErrNoSnapshot is returned by [DB.InReadSnapshot] on a store that has no snapshot pool, which is
// every store opened at [MemoryPath].
//
// It is an error rather than a silent fall back to the writing pool. Falling back would give the
// caller two pooled reads while the call site said "snapshot", which is the bug this primitive
// exists to prevent, wearing the name of its fix.
var ErrNoSnapshot = errors.New("store has no read snapshot pool")

// ErrNoRows is what a `:one` query returns when it finds nothing.
//
// It is re-exported here because `database/sql` is imported by this package and no other — SQL001
// and TestSQL001_DatabaseSQL_IsImportedOnlyByTheStore are the mechanism — and a caller that has to
// distinguish "no such row" from a real failure would otherwise have to break that rule to spell
// the sentinel. Compare with errors.Is, never ==.
var ErrNoRows = sql.ErrNoRows

// DB is an open database. It is safe for concurrent use.
//
// It holds TWO pools over the same file, and which one a caller gets is decided by the primitive
// they reach for rather than by a field they pick. `sql` is the writing pool, opened
// `_txlock=immediate`; `read` is the snapshot pool, opened `_txlock=deferred` and `query_only`.
// See [DB.InTx] and [DB.InReadSnapshot], and [readDSN] for why the second one has to be a second
// handle rather than a second kind of BEGIN.
type DB struct {
	sql     *sql.DB
	read    *sql.DB
	queries *sqlitegen.Queries
	path    string
	log     *slog.Logger
}

// Open opens the database at path, applying the pragmas this workload needs, and verifies the
// connection. It does NOT migrate; call [DB.Migrate] for that, so that a caller which only wants
// to inspect an existing database cannot accidentally upgrade it.
//
// A nil logger is an error rather than a silent default: the migration runner logs through it, and
// a migration that ran with its output discarded is the one thing an operator most needs to see.
func Open(ctx context.Context, path string, log *slog.Logger) (*DB, error) {
	if log == nil {
		return nil, errors.New("open store: logger is nil")
	}
	if path == "" {
		return nil, errors.New("open store: path is empty")
	}

	handle, err := sql.Open(driverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err := handle.PingContext(ctx); err != nil {
		// The failed handle is closed here rather than left to a finaliser: on Windows an open
		// handle to a corrupt file is what stops the operator moving it out of the way.
		return nil, errors.Join(
			fmt.Errorf("reach database %s: %w", path, err),
			handle.Close(),
		)
	}

	db := &DB{sql: handle, queries: sqlitegen.New(handle), path: path, log: log}

	// The read pool is opened SECOND and only for a file, both deliberately.
	//
	// Second, because it is `query_only`: it cannot create the database or its WAL, so the write
	// handle's ping above has to have done that already. Only for a file, because `:memory:` is
	// private to a connection — see [MemoryPath] — so a second handle to it would be a second,
	// empty database rather than another view of this one.
	if path != MemoryPath {
		reader, readErr := sql.Open(driverName, readDSN(path))
		if readErr != nil {
			return nil, errors.Join(
				fmt.Errorf("open read pool %s: %w", path, readErr), db.Close())
		}
		if pingErr := reader.PingContext(ctx); pingErr != nil {
			return nil, errors.Join(
				fmt.Errorf("reach read pool %s: %w", path, pingErr),
				reader.Close(), db.Close())
		}
		db.read = reader
	}

	return db, nil
}

// dsn builds the connection string for the WRITING pool. Every pragma is set here, in the DSN,
// rather than with an Exec after opening: database/sql keeps a POOL, it opens new connections
// whenever it feels like it, and a pragma applied to one connection is not applied to the next
// one. A foreign-key check that holds on some connections is worse than one that holds on none,
// because it passes in testing.
//
//	journal_mode=WAL    readers do not block the writer, which is what makes a long read (the
//	                    nightly projection verify) survivable on a box also taking reports.
//	foreign_keys=ON     SQLite defaults it OFF. Every REFERENCES in db/schema.hcl is decoration
//	                    without this line.
//	busy_timeout=5000   WAL still serialises writers. Five seconds of retry turns the ordinary
//	                    two-writers-at-once case into a wait instead of an error a user sees.
//	synchronous=NORMAL  With WAL this loses at most the last transactions on power loss, never the
//	                    database. FULL costs an fsync per commit for a durability guarantee a home
//	                    server's disk does not really make anyway.
//	_txlock=immediate   Take the write lock when a transaction BEGINS. Without it a transaction
//	                    that reads and then writes can fail with SQLITE_BUSY_SNAPSHOT, which
//	                    busy_timeout does NOT retry — the classic Go-plus-SQLite deadlock. The cost
//	                    is that a read-only transaction also serialises; our transactions are short
//	                    and nearly all of them write. A read that is NEITHER — a board render —
//	                    goes to the second pool instead of paying it: see [readDSN].
func dsn(path string) string {
	return connectionString(path, "immediate", false)
}

// readDSN builds the connection string for the SNAPSHOT pool: the same pragmas, plus
// `_txlock=deferred` and `query_only`.
//
// **It is a second handle rather than a second kind of BEGIN because `_txlock` is a property of
// the connection.** The driver reads it when a connection opens and prefixes every BEGIN with it
// thereafter. Today's driver will also emit a plain, deferred BEGIN for a transaction opened with
// `sql.TxOptions{ReadOnly: true}` — but that makes the lock mode a driver behaviour depended on
// silently, and it buys nothing at all for the read-only half, which SQLite would still not
// enforce. A pool is where both can be spelled once and checked.
//
//	_txlock=deferred    Take no lock at BEGIN. Under WAL the read snapshot is then pinned by the
//	                    FIRST read statement and held until the transaction ends, which is exactly
//	                    what a multi-read render needs — and writers are not blocked by it, which
//	                    is what `_txlock=immediate` would have cost. See ADR-0014.
//	query_only(1)       SQLite refuses every write on this pool. Without it a write reached through
//	                    a snapshot would try to UPGRADE a deferred read transaction to a write one,
//	                    which is the SQLITE_BUSY_SNAPSHOT deadlock `_txlock=immediate` exists to
//	                    prevent — reintroduced by the back door. It is LAST so the pragmas before
//	                    it run while the connection can still write. On a database already in WAL
//	                    none of them needs to — `journal_mode=WAL` reads `wal` back and the rest
//	                    are connection settings — which is exactly why [Open] pings the writing
//	                    pool first: this pool must never be the connection that creates the file.
func readDSN(path string) string {
	return connectionString(path, "deferred", true)
}

// connectionString assembles a DSN. Both pools share it so a pragma added for one is not silently
// missing from the other; the two arguments are the only things they disagree about.
func connectionString(path, txlock string, queryOnly bool) string {
	if path == MemoryPath {
		path = ":memory:"
	}
	pragmas := []string{
		"journal_mode(WAL)",
		"foreign_keys(1)",
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
	}
	if queryOnly {
		pragmas = append(pragmas, "query_only(1)")
	}
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", txlock)
	return "file:" + path + "?" + q.Encode()
}

// Path returns the file the store was opened from, for log and error context.
func (d *DB) Path() string { return d.path }

// Queries returns the generated query set, bound to the pool.
//
// It is deliberately not embedded: a caller writing d.CreateCircle(...) would read as though the
// store had a domain method, and the whole point of this package is that it does not.
func (d *DB) Queries() *sqlitegen.Queries {
	return d.queries
}

// InTx runs fn inside a transaction, committing when it returns nil and rolling back otherwise.
//
// The queries handed to fn are bound to the transaction. That is the only way to get transactional
// queries, so "did this run in a transaction" is answerable by looking at the call rather than at
// what was in scope.
func (d *DB) InTx(ctx context.Context, fn func(context.Context, *sqlitegen.Queries) error) error {
	if d.sql == nil {
		return ErrClosed
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(ctx, d.queries.WithTx(tx)); err != nil {
		// Deliberate waiver: the rollback's own error is reported instead of the cause, and the
		// cause is what the caller can act on. A failed rollback still ends the transaction when
		// the connection returns to the pool.
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// InReadSnapshot runs fn inside a deferred, read-only transaction on the snapshot pool, so every
// statement fn issues sees ONE state of the database.
//
// **This is the primitive for a render that reads more than one table and pairs the answers.**
// [DB.InTx] is the primitive for anything that writes. Reaching for the wrong one is meant to be
// obvious from the name: a write inside a snapshot does not merely fail review, it fails at
// SQLite, because the pool is `query_only` — see [readDSN].
//
// Two properties are being bought, and they are separate:
//
//   - Isolation. Without it, two pooled reads are two implicit transactions, and anything that
//     commits between them gives the caller a pair of answers that never coexisted. The board's
//     effective timer and its cached `died_at` are exactly such a pair — the timer carries the
//     clustering ε the `died_at` was derived under — which is what
//     https://github.com/prokopto-dev/tod-serve/issues/17 is about.
//   - Concurrency. `InTx` would give isolation too, and would take the WRITE lock at BEGIN to do
//     it, serialising the whole instance behind the slowest reader. This does not: under WAL a
//     deferred read transaction blocks no writer. ADR-0014 is the trade in full.
//
// The snapshot is pinned by the FIRST read fn issues, not by this call, and it is held until fn
// returns. So fn should read what it needs and get out: everything it holds open, it holds the WAL
// from checkpointing.
//
// It always rolls back, never commits. A read-only transaction has nothing to commit, and
// `ROLLBACK` says at the one place it matters that this was never going to write.
//
// A store opened at [MemoryPath] has no snapshot pool and this returns [ErrNoSnapshot].
func (d *DB) InReadSnapshot(
	ctx context.Context, fn func(context.Context, *sqlitegen.Queries) error,
) error {
	if d.sql == nil {
		return ErrClosed
	}
	if d.read == nil {
		return ErrNoSnapshot
	}
	tx, err := d.read.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin read snapshot: %w", err)
	}
	// Deliberate waiver, and the only correct one here: the rollback of a transaction that wrote
	// nothing cannot lose anything, and its error is not something a caller can act on. Turning a
	// board render that read correctly into a 500 because the connection died on the way back to
	// the pool would report a failure that did not happen.
	defer func() { _ = tx.Rollback() }()
	return fn(ctx, sqlitegen.New(tx))
}

// Close releases both pools. A closed store returns [ErrClosed] rather than panicking on a nil
// pointer, because shutdown ordering is exactly where a late request arrives.
func (d *DB) Close() error {
	if d.sql == nil {
		return nil
	}
	handle, reader := d.sql, d.read
	d.sql, d.read, d.queries = nil, nil, nil
	// Both pools are closed and both errors are reported. Closing one and returning on its error
	// would leave the other's connections open on a database the caller believes it has released,
	// which is the failure [Open] closes a handle it could not ping to avoid.
	var errs []error
	if reader != nil {
		if err := reader.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close read pool %s: %w", d.path, err))
		}
	}
	if err := handle.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close database %s: %w", d.path, err))
	}
	return errors.Join(errs...)
}

// IntegrityCheck runs `PRAGMA integrity_check` and reports what SQLite found.
//
// SQLite answers with the single row "ok" on a healthy database and one row per problem otherwise,
// so a caller cannot tell success from failure by the absence of an error. This turns that into a
// Go error, which is the shape `tod-serve doctor` and the readiness check both want.
func (d *DB) IntegrityCheck(ctx context.Context) error {
	if d.sql == nil {
		return ErrClosed
	}
	rows, err := d.sql.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("integrity check %s: %w", d.path, err)
	}
	defer closeRows(rows)

	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("integrity check %s: %w", d.path, err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("integrity check %s: %w", d.path, err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("integrity check %s: %s", d.path, strings.Join(problems, "; "))
	}
	return nil
}

// ForeignKeyCheck runs `PRAGMA foreign_key_check` and reports any orphaned row.
//
// Foreign keys are only enforced when foreign_keys is ON, and this schema is full of them. If the
// pragma were ever lost — a connection opened outside [Open], a restored database written by
// something else — the damage is silent until a join returns nothing. This is how that is found.
func (d *DB) ForeignKeyCheck(ctx context.Context) error {
	if d.sql == nil {
		return ErrClosed
	}
	rows, err := d.sql.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign key check %s: %w", d.path, err)
	}
	defer closeRows(rows)

	var violations []string
	for rows.Next() {
		var (
			table  string
			rowid  sql.NullInt64
			parent string
			fkid   int64
		)
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("foreign key check %s: %w", d.path, err)
		}
		violations = append(violations,
			fmt.Sprintf("%s row %d references a missing %s", table, rowid.Int64, parent))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign key check %s: %w", d.path, err)
	}
	if len(violations) > 0 {
		return fmt.Errorf("foreign key check %s: %s", d.path, strings.Join(violations, "; "))
	}
	return nil
}

// IsNotFound reports whether err is "no such row".
//
// It exists so that a service package can answer that question without importing database/sql,
// which SQL001 forbids everywhere outside this package. Without it, every adapter above the store
// would either import the driver's error — reopening the rule this package exists to enforce — or
// compare error strings.
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// The SQLite extended result codes for a duplicate key. They are spelled here rather than imported
// from the driver's constant set because that set lives in a generated subpackage whose import
// path is an implementation detail of the driver; the numbers are SQLite's own and stable.
const (
	sqliteConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
	sqliteConstraintPrimaryKey = 1555 // SQLITE_CONSTRAINT_PRIMARYKEY
)

// IsUniqueViolation reports whether err is SQLite refusing a duplicate on a unique index or a
// primary key.
//
// It lives here for the same reason [IsNotFound] does: this is the only package that may name the
// driver, so a service above it would otherwise have to compare error strings — and a string match
// that stops matching after a driver upgrade turns a `409 conflict` into a `500` silently.
func IsUniqueViolation(err error) bool {
	var e *sqlite.Error
	if !errors.As(err, &e) {
		return false
	}
	code := e.Code()
	return code == sqliteConstraintUnique || code == sqliteConstraintPrimaryKey
}
