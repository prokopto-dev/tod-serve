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
	require.Contains(t, out, "ListEveryReport",
		"the finding names no query; \"TEN001\" alone also appears in the gate's PASS line")
}

// Naming the tenant key is not filtering on it. This is the mutation the gate used to pass: a
// query that reads EVERY circle's reports and merely returns `circle_id` in its projection read as
// "names circle_id" to a gate that grepped the whole statement.
//
// It is the same class as the ORDER BY case above and it is the more likely one to be written,
// because an explicit column list is what a join or an aggregate needs.
func TestTEN001_CircleIDOnlyInTheSelectList_IsReported(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "tod_report.sql", `
-- name: ListEveryReportWithItsCircle :many
SELECT id, circle_id, target_id FROM tod_report WHERE id = ?;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.Error(t, err, "circle_id in a SELECT list was accepted as a filter:\n%s", out)
	require.Contains(t, out, "TEN001")
	require.Contains(t, out, "ListEveryReportWithItsCircle")
}

// The tenant key in an UPDATE's SET clause is not a filter either: it names the circle a row is
// being moved TO, which is the write version of the same mistake.
//
// The table is `membership` rather than `tod_report` deliberately. An UPDATE against an
// append-only table is reported by LOG001, so writing this against `tod_report` produced a test
// that passed on the OLD gate too — for a finding TEN001 never made. The query NAME is asserted
// for the same reason: "TEN001" appears in the gate's PASS line as well as its failure, so
// matching on the identifier alone is an assertion that cannot fail.
func TestTEN001_CircleIDOnlyInAnUpdatesSetClause_IsReported(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "membership.sql", `
-- name: MoveMemberToAnotherCircle :exec
UPDATE membership SET circle_id = ? WHERE id = ?;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.Error(t, err, "circle_id in a SET clause was accepted as a filter:\n%s", out)
	require.Contains(t, out, "MoveMemberToAnotherCircle")
}

// An INSERT has no WHERE, so the rule for one is that the row being WRITTEN names its circle.
// Without this branch the stricter WHERE reading would report every INSERT in the repository and
// somebody would widen the gate back out to silence it.
func TestTEN001_AnInsertNamingTheCircle_IsAccepted(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "tod_report.sql", `
-- name: CreateReport :one
INSERT INTO tod_report (id, circle_id, target_id)
VALUES (?, ?, ?)
RETURNING *;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.NoError(t, err, out)
	require.Contains(t, out, "1 queries")
}

// And an INSERT that does NOT name it is reported, so the branch above is a rule rather than a
// hole shaped like one.
func TestTEN001_AnInsertWithoutTheCircle_IsReported(t *testing.T) {
	t.Parallel()
	dir := queriesDir(t, "tod_report.sql", `
-- name: CreateUntenantedReport :one
INSERT INTO tod_report (id, target_id)
VALUES (?, ?)
RETURNING *;
`)

	out, err := runGates(t, "TOD_QUERIES_DIR="+dir)
	require.Error(t, err, "an INSERT that writes no circle_id was accepted:\n%s", out)
	require.Contains(t, out, "TEN001")
	require.Contains(t, out, "CreateUntenantedReport")
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
