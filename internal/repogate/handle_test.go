package repogate_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/repogate"
)

// SQL002 catches every way a `database/sql` handle can leave a package, and lets through the way
// the store legitimately holds one.
//
// The cases are written as source rather than as fixtures on disk for the reason
// TestCheckSource_TimeNow_IsFoundHoweverItIsSpelled is: committing a file that violates the rule under
// test would make the repository-wide run red, so the analyser has to be drivable directly.
func TestSQL002_AnExportedHandle_IsReported(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "an exported accessor returning the handle",
			src: `package store
import "database/sql"
type DB struct{ sql *sql.DB }
func (d *DB) Raw() *sql.DB { return d.sql }`,
			want: true,
		},
		{
			name: "an unexported field holding it, which is how the store holds it",
			src: `package store
import "database/sql"
type DB struct{ sql *sql.DB; read *sql.DB }
func (d *DB) Path() string { return "" }`,
			want: false,
		},
		{
			name: "an exported struct field",
			src: `package store
import "database/sql"
type Config struct{ Handle *sql.DB }`,
			want: true,
		},
		{
			name: "an exported function taking one",
			src: `package store
import "database/sql"
func Wrap(db *sql.DB) error { _ = db; return nil }`,
			want: true,
		},
		{
			name: "an exported interface method returning one",
			src: `package store
import "database/sql"
type Opener interface{ Open() (*sql.DB, error) }`,
			want: true,
		},
		{
			name: "an exported package-level variable",
			src: `package store
import "database/sql"
var Shared *sql.DB`,
			want: true,
		},
		{
			name: "a transaction, which is a handle too",
			src: `package store
import "database/sql"
func Begin() (*sql.Tx, error) { return nil, nil }`,
			want: true,
		},
		{
			// The alias is the whole reason this is an AST analyser: a grep for `sql.DB` reads
			// past it, and it is a two-character change.
			name: "an aliased import",
			src: `package store
import d "database/sql"
func Raw() *d.DB { return nil }`,
			want: true,
		},
		{
			name: "a dot import",
			src: `package store
import . "database/sql"
func Raw() *DB { return nil }`,
			want: true,
		},
		{
			// A slice of them is not a cleverer way of handing one over.
			name: "a handle nested inside another type",
			src: `package store
import "database/sql"
func All() map[string]*sql.DB { return nil }`,
			want: true,
		},
		{
			name: "a method on an unexported type is unreachable from outside",
			src: `package store
import "database/sql"
type pool struct{ db *sql.DB }
func (p *pool) Raw() *sql.DB { return p.db }`,
			want: false,
		},
		{
			// `sql.Result` and the null types are values that came back, not the connection. A
			// rule that banned them would ban the store's own honest signatures.
			name: "a value type from database/sql is not a handle",
			src: `package store
import "database/sql"
func Rows() (sql.Result, sql.NullString) { return nil, sql.NullString{} }`,
			want: false,
		},
		{
			name: "a file that does not import database/sql at all",
			src: `package circle
type DB struct{}
func (d *DB) Raw() *DB { return d }`,
			want: false,
		},
		{
			// `ErrNoRows` is re-exported deliberately, so a caller distinguishing "no such row"
			// from a real failure does not have to import database/sql to spell the sentinel.
			name: "re-exporting the sentinel is not handing out a handle",
			src: `package store
import "database/sql"
var ErrNoRows = sql.ErrNoRows`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			found, err := repogate.CheckExportedHandles("internal/store/store.go", tc.src)
			require.NoError(t, err)
			if !tc.want {
				require.Empty(t, found, "SQL002 reported %v", found)
				return
			}
			require.NotEmpty(t, found,
				"SQL002 did not report a handle leaving the package; SQL001 cannot see this "+
					"one, because a caller of it need never name database/sql")
			require.Equal(t, repogate.HandleRuleID, found[0].Rule)
		})
	}
}

// The four types SQL002 treats as handles are the connection itself. Named here so that adding a
// fifth is a deliberate act rather than a silent widening.
func TestSQL002_HandleTypes_AreTheConnectionAndNotItsResults(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"DB", "Tx", "Conn", "Stmt"}, repogate.HandleTypes())
}
