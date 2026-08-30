package repo

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/repogate"
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

// The instance-scoped allowlist exists in FOUR places: canonical §9, ADR-0002, INSTANCE_SCOPED in
// scripts/repo-gates.sh, and — by subtraction — the schema test. Three of them were compared and
// the ADR was not, so it sat three tables short: it omitted instance_grant, auth_flow and
// credential_ticket, each of which IS instance-scoped and each of which a reader of that ADR would
// have concluded needs a circle_id.
//
// Canonical §9 is the authority; this asserts the ADR repeats it exactly. Both documents introduce
// the list with the same sentence, so one parser reads both and there is no second spelling to
// drift.
func TestInstanceScopedAllowlist_TheADRAndCanonical_Agree(t *testing.T) {
	t.Parallel()

	canonical, err := canondoc.InstanceScopedTables()
	require.NoError(t, err)
	require.NotEmpty(t, canonical, "canonical §9 parsed to an empty allowlist")
	sort.Strings(canonical)

	adr, err := canondoc.InstanceScopedTablesInTenancyADR()
	require.NoError(t, err)
	require.NotEmpty(t, adr, "ADR-0002 parsed to an empty allowlist; the lead-in sentence moved")
	sort.Strings(adr)

	if diff := cmp.Diff(canonical, adr); diff != "" {
		t.Errorf("the instance-scoped allowlist differs between canonical §9 and ADR-0002 "+
			"(-canonical +adr):\n%s", diff)
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

// SQL001 had no can-fail test at all. Every sibling gate in this file has one — TEN001 has five,
// MIG001 two, LOG001 one, SQLC001 one — and the rule that keeps `*sql.DB` out of every package but
// internal/store rested on a grep nothing had ever seen fire.
//
// It matters more than most: ADR-0002 reintroduces the cross-tenant leak class knowingly and buys
// it back with `circle_id` in every query's WHERE. A handle outside the store goes around all of
// it, so law 2 is what makes law 4 worth anything.
func TestSQL001_DatabaseSQLOutsideTheStore_IsReported(t *testing.T) {
	t.Parallel()
	dir := goDir(t, "circle/leak.go", `package circle

import "database/sql"

func Leak(db *sql.DB) error { return db.Ping() }
`)

	out, err := runGates(t, "TOD_GO_DIRS="+dir)
	require.Error(t, err, "database/sql outside internal/store was accepted:\n%s", out)
	require.Contains(t, out, "SQL001")
	require.Contains(t, out, "leak.go",
		"the finding names no file; \"SQL001\" alone also appears in the gate's PASS line")
}

// The other direction: a tree with no `database/sql` in it passes, so the test above is reporting
// the import rather than the fixture.
func TestSQL001_ATreeWithoutTheImport_Passes(t *testing.T) {
	t.Parallel()
	dir := goDir(t, "circle/circle.go", `package circle

type Circle struct{ ID string }
`)

	out, err := runGates(t, "TOD_GO_DIRS="+dir)
	require.NoError(t, err, out)
	require.Contains(t, out, "database/sql is imported only by internal/store")
}

// A gate reporting success over an empty search space is how a rule quietly stops being enforced.
//
// A tree with NO Go files is reported honestly already — `has_go` is false and the gate prints
// `vacant`, which the script's own header calls the right answer for a rule with nothing to check
// yet. The hole is the other shape: Go files exist, so the gate runs, and every one of them falls
// inside an exclusion. SQL001 then greps an empty list and prints a green tick over nothing.
func TestSQL001_AScanWhereEverythingIsExcluded_IsReportedRatherThanPassed(t *testing.T) {
	t.Parallel()
	// The real internal/store, which is the one directory SQL001 excludes entirely. Every file
	// the gate finds is dropped, so it has nothing left to grep — and before the guard it printed
	// a green tick saying database/sql is imported only by internal/store.
	//
	// A temporary fixture cannot express this: the exclusions are prefixes of `./internal/...`, so
	// a fixture reached by an absolute path is never excluded and the gate scans it.
	out, err := runGates(t, "TOD_GO_DIRS=./internal/store")
	require.Error(t, err,
		"SQL001 passed over a scan in which every file was excluded:\n%s", out)
	require.Contains(t, out, "no files were scanned")
}

// The exclusion SQL001 grants internal/repogate is for a STRING, not an import. SQL002's source
// has to name the package it bans, exactly as internal/identity/outbound is excluded from NET001
// for holding the one client it exists to hold.
//
// Without this the exemption would be a hole shaped like a comment: a package excluded from the
// gate that keeps handles out of packages.
func TestSQL001_TheAnalysersOwnPackage_DoesNotActuallyImportIt(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	files, err := filepath.Glob(filepath.Join(root, "internal", "repogate", "*.go"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "internal/repogate has no Go files; the exemption guards nothing")

	for _, path := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, spec := range parsed.Imports {
			require.NotEqual(t, `"database/sql"`, spec.Path.Value,
				"%s IMPORTS database/sql, and internal/repogate is excluded from SQL001 only "+
					"because it NAMES it in a string. Either stop importing it or stop "+
					"excluding the package", filepath.Base(path))
		}
	}
}

// SQL002, over the real repository: internal/store hands no handle out.
//
// This is the test internal/store/store.go's own comments used to name and nobody had written.
// SQL001 answers "who imports database/sql" and cannot answer "can a handle be obtained without
// importing it" — `db := store.Raw()` names the package nowhere. sqlitegen is scanned too: it sits
// under internal/store, so SQL001 excludes it, and it is generated code nobody reviews line by
// line.
func TestSQL002_TheStore_HandsOutNoHandle(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	got, err := repogate.CheckHandles(root, []string{"internal/store"})
	require.NoError(t, err)
	for _, f := range got.Findings {
		t.Errorf("%s: a database/sql handle leaves internal/store through an exported "+
			"declaration, so a caller can hold one without ever naming the package SQL001 "+
			"greps for", f)
	}
	require.Greater(t, got.Files, 5,
		"SQL002 parsed %d files under internal/store; a gate that looked at nothing reports "+
			"success the same way one that looked at everything does", got.Files)
}

// SQL002's one exemption, checked rather than asserted.
//
// internal/store/sqlitegen is generated by sqlc and exports `DBTX` and `WithTx`, both of which
// name a handle and neither of which can be changed here. What makes that safe is that no value of
// either ever leaves the store — so this drives the rule that says so. Without it the exemption
// would be a directory nobody looks at, inside the package that holds the database.
func TestSQL002_TheGeneratedExemption_IsNotAWayOut(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	require.Equal(t, []string{"internal/store/sqlitegen"}, repogate.HandleAllowDirs(),
		"SQL002 grew a second exemption; each one needs the rule beside it that earns it")

	got, err := repogate.Check(root, scanned(), []repogate.Rule{repogate.SqlitegenRule()})
	require.NoError(t, err)
	for _, f := range got.Findings {
		t.Errorf("%s: %s", f, repogate.SqlitegenRule().Reason)
	}
	require.Greater(t, got.Files, 100, "SQL002's companion parsed only %d files", got.Files)
}

// And the rest of the repository, which SQL001 covers from the other end. A package outside the
// store cannot expose a handle without importing database/sql, so this is belt and braces — but it
// is the belt that survives somebody widening SQL001's exclusion list.
func TestSQL002_NoPackageAnywhere_ExposesAHandle(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	got, err := repogate.CheckHandles(root, scanned())
	require.NoError(t, err)
	for _, f := range got.Findings {
		t.Errorf("%s: an exported declaration hands out a database/sql handle", f)
	}
	require.Greater(t, got.Files, 100, "SQL002 parsed only %d files", got.Files)
}

// goDir writes one Go file into a temporary tree and returns the tree's root, for the gates that
// walk source rather than a directory of SQL.
func goDir(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return dir
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

// TestTenancy_EveryQueryAgainstATableWithACircleID_NamesIt asks the SCHEMA which tables are
// circle-scoped instead of trusting a filename.
//
// TEN001 skips a whole FILE whose basename is on the instance-scoped allowlist. Two of the
// fourteen allowlisted tables carry a `circle_id` anyway — `event_outbox` and `auth_flow`, both
// nullable, both for the reason canonical §9 gives — so every query in those two files is exempt
// from the tenancy gate by nothing more than what the file is called.
//
// That is not hypothetical, and it is aimed straight at the least-weathered thing in the roadmap.
// `event_outbox` is what `subscribeCircleEvents` and `replayCircleEvents` are built on — the two
// circle-scoped operations `uncoveredCircleRoutes` still lists — and `event_seq` is the ONE global
// sequence across every circle on the instance. A Phase 6 replay filtering on `since_seq` and not
// `circle_id` would stream another circle's events to a member of the right circle: the middleware
// passes, because the circle in the path IS theirs, and only the `WHERE` stands in the way. Today
// the one per-circle read is correct, and what says so is a prose comment in the file header.
//
// This closes it before the handler lands. The rule is the same rule; what changes is that
// "circle-scoped" is read off `db/schema.hcl` — the single schema truth — rather than off the
// allowlist, so a table cannot become exempt by being renamed onto a list.
func TestTenancy_EveryQueryAgainstATableWithACircleID_NamesIt(t *testing.T) {
	t.Parallel()

	tenanted := tablesWithACircleID(t)
	require.Greater(t, len(tenanted), 5,
		"only %d tables carry a circle_id; the schema parse is wrong", len(tenanted))

	checked, waived := 0, 0
	for _, q := range everyQuery(t) {
		var touches []string
		for _, table := range tenanted {
			if q.touches(table) {
				touches = append(touches, table)
			}
		}
		if len(touches) == 0 {
			continue
		}
		checked++

		if q.namesCircleWhereItCounts() {
			continue
		}
		waived++
		require.Contains(t, q.Body, "-- tenancy:",
			"%s in %s reads %v, which carries a circle_id, and does not name it where it "+
				"filters. TEN001 does not see this one: %s is on the instance-scoped allowlist, "+
				"so the whole file is skipped by name. Either name the tenant, or carry a "+
				"`-- tenancy:` line saying why this query legitimately spans circles",
			q.Name, q.File, touches, strings.TrimSuffix(q.File, ".sql"))
	}

	require.Greater(t, checked, 30,
		"only %d queries touch a circle-scoped table; the parse has drifted", checked)
	t.Logf("%d queries read a table carrying circle_id; %d name it, %d carry a counted waiver",
		checked, checked-waived, waived)
}

// namesCircleWhereItCounts reports whether the query names the tenant in the place that FILTERS.
//
// For an INSERT that is the column list: the row being written names its circle. For everything
// else it is the WHERE, with the ORDER BY tail removed — sorting by the tenant key is not
// filtering on it, and naming it in a projection is not either.
func (q sqlQuery) namesCircleWhereItCounts() bool {
	upper := strings.ToUpper(q.SQL)
	if strings.HasPrefix(strings.TrimSpace(upper), "INSERT") {
		head := q.SQL
		if at := strings.Index(upper, " VALUES"); at > 0 {
			head = q.SQL[:at]
		} else if at := strings.Index(upper, " SELECT"); at > 0 {
			head = q.SQL[:at]
		}
		return strings.Contains(head, "circle_id")
	}
	return strings.Contains(q.filter(), "circle_id")
}

// tablesWithACircleID reads db/schema.hcl — the single schema truth — for every table carrying a
// circle_id column, nullable or not.
func tablesWithACircleID(t *testing.T) []string {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, "db", "schema.hcl"))
	require.NoError(t, err)

	var out []string
	for _, block := range regexp.MustCompile(`(?ms)^table "([a-z_]+)" \{.*?^\}`).
		FindAllStringSubmatch(string(raw), -1) {
		if strings.Contains(block[0], `column "circle_id"`) {
			out = append(out, block[1])
		}
	}
	sort.Strings(out)
	return out
}
