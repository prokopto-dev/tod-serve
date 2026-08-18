package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// The instance-scoped allowlist exists in canonical §9 as prose and in scripts/repo-gates.sh as a
// shell variable. Two hand-maintained copies of one fact is exactly the drift this repository
// gates against everywhere else, and the copy that silently grows is the one that stops a table
// being tenancy-checked — so they are compared in each direction.
func TestInstanceScopedAllowlist_MatchesRepoGates(t *testing.T) {
	t.Parallel()

	fromDoc, err := canondoc.InstanceScopedTables()
	require.NoError(t, err)
	sort.Strings(fromDoc)

	fromGate := repoGatesAllowlist(t)
	sort.Strings(fromGate)

	if diff := cmp.Diff(fromDoc, fromGate); diff != "" {
		t.Errorf("the instance-scoped allowlist differs between canonical §9 and "+
			"INSTANCE_SCOPED in scripts/repo-gates.sh (-document +gate):\n%s", diff)
	}
}

// A gate nobody has seen fail is a gate nobody knows works. This one is pointed at a query file
// that reads a circle-scoped table without naming the tenant, and it must say so.
func TestTEN001_AQueryMissingCircleID_IsReported(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "membership.sql", `
-- name: ListEverybody :many
SELECT * FROM membership ORDER BY id;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.Error(t, err, "the gate passed a query that reads every tenant:\n%s", out)
	require.Contains(t, out, "TEN001")
	require.Contains(t, out, "ListEverybody")
}

// The waiver is the escape hatch, and an escape hatch that is not counted becomes the default. It
// must suppress the finding AND appear in the tally.
func TestTEN001_AWaivedQuery_IsCountedRatherThanReported(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "membership.sql", `
-- name: ListForVerifiedIdentity :many
-- tenancy: keyed on a verified identity, never on a caller-supplied circle id.
SELECT * FROM membership WHERE identity_id = ?;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.NoError(t, err, out)
	require.Contains(t, out, "1 explicitly waived")
}

// Sorting by the tenant key is not filtering by it. Without this the gate passes a query that
// reads every circle and then puts the rows in a tidy order.
func TestTEN001_CircleIDOnlyInAnOrderBy_IsReported(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "tod_report.sql", `
-- name: ListEveryReport :many
SELECT * FROM tod_report ORDER BY circle_id, id;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.Error(t, err, "circle_id in an ORDER BY was accepted as a filter:\n%s", out)
	require.Contains(t, out, "TEN001")
}

// A query file named after a table on the instance-scoped allowlist is not checked, which is the
// whole point of the allowlist. If this stopped being true the gate would report findings against
// every provider lookup and somebody would turn it off.
func TestTEN001_AnInstanceScopedTable_IsNotChecked(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "identity_provider.sql", `
-- name: ListIdentityProviders :many
SELECT * FROM identity_provider ORDER BY key;
`)
	// A circle-scoped file alongside it, so the "no queries were checked" guard has something to
	// count. That guard is the reason this test cannot simply hand the gate one allowlisted file:
	// a gate that checked nothing at all must not read as a pass.
	writeQuery(t, dir, "membership.sql", `
-- name: ListMemberships :many
SELECT * FROM membership WHERE circle_id = ?;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.NoError(t, err, out)
	require.Contains(t, out, "1 queries", "the allowlisted file's query was checked after all")
}

// Migrations are forward-only. A Down block that looked runnable would be reached for at exactly
// the wrong moment, and DROP TABLE is not an undo of a migration that already ran.
func TestMIG001_ADownBlockWithDDL_IsReported(t *testing.T) {
	t.Parallel()
	dir := migrationsDir(t, "000001_initial.sql", `-- +goose Up
CREATE TABLE circle (id TEXT NOT NULL PRIMARY KEY) STRICT;

-- +goose Down
DROP TABLE circle;
`)

	out, err := runGates(t, "TOD_MIGRATIONS_DIR="+dir)
	require.Error(t, err, "a reversible migration was accepted:\n%s", out)
	require.Contains(t, out, "MIG001")
}

func TestMIG001_AMisnumberedMigration_IsReported(t *testing.T) {
	t.Parallel()
	dir := migrationsDir(t, "000007_out_of_order.sql", `-- +goose Up
CREATE TABLE circle (id TEXT NOT NULL PRIMARY KEY) STRICT;

-- +goose Down
SELECT RAISE(ABORT, 'migrations are forward-only');
`)

	out, err := runGates(t, "TOD_MIGRATIONS_DIR="+dir)
	require.Error(t, err, "a migration numbered 7 with nothing before it was accepted:\n%s", out)
	require.Contains(t, out, "MIG001")
}

// The report log is never UPDATEd or DELETEd, in Go, in SQL, or in a migration. Go reaches the
// database only through code sqlc generates from these files, so this is where that is caught.
func TestLOG001_AnUpdateAgainstAnAppendOnlyTable_IsReported(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "tod_report.sql", `
-- name: FixThatTypo :exec
UPDATE tod_report SET died_at = ? WHERE circle_id = ? AND id = ?;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.Error(t, err, "an UPDATE against the append-only report log was accepted:\n%s", out)
	require.Contains(t, out, "LOG001")
}

// Not a style rule: sqlc rewrites sqlc.arg() by byte offset while reporting positions in runes, so
// one em dash in a comment silently mangles every query after it in the file.
func TestSQLC001_NonASCIIInAQueryFile_IsReported(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "circle.sql", `
-- circle — the tenant root.
-- name: GetCircle :one
SELECT * FROM circle WHERE id = ?;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.Error(t, err, "an em dash in a query file was accepted:\n%s", out)
	require.Contains(t, out, "SQLC001")
}

// runGates runs the real scripts/repo-gates.sh with the given environment overrides and returns
// its combined output. The real script is run rather than a reimplementation of it: a test that
// reimplements the gate proves the reimplementation works.
func runGates(t *testing.T, env ...string) (string, error) {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "repo-gates.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// queriesDir writes one query file into a temporary directory and returns its path.
func queriesDir(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeQuery(t, dir, name, body)
	return dir
}

// writeQuery adds another query file to an existing directory.
func writeQuery(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}

// migrationsDir writes one migration into a temporary directory and returns its path.
func migrationsDir(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	return dir
}

// repoGatesAllowlist reads INSTANCE_SCOPED out of the gate script.
func repoGatesAllowlist(t *testing.T) []string {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "repo-gates.sh"))
	require.NoError(t, err)

	const prefix = "INSTANCE_SCOPED='"
	body := string(raw)
	start := strings.Index(body, prefix)
	require.GreaterOrEqual(t, start, 0, "scripts/repo-gates.sh declares no INSTANCE_SCOPED")
	rest := body[start+len(prefix):]
	end := strings.Index(rest, "'")
	require.GreaterOrEqual(t, end, 0, "INSTANCE_SCOPED is unterminated")

	return strings.Split(rest[:end], "|")
}
