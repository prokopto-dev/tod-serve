package store

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/dbschema"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// tenantRoot is the one table whose tenancy key is its own `id`. It is spelled out rather than
// inferred, because "the table with no circle_id that is not on the allowlist" is exactly the
// shape of the mistake the tenancy gate exists to catch, and inferring it would make the gate
// blind to precisely that.
const tenantRoot = "circle"

// STRICT is what makes the column types in db/schema.hcl mean anything: without it SQLite accepts
// 'banana' in an INTEGER column and stores it as text. Checked against the applied schema rather
// than against the HCL, because the migration is what an officer's database actually ran.
func TestSchema_EveryTable_IsStrict(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	tables, err := db.Tables(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tables, "no tables in the applied schema; the migration did not run")

	for _, tbl := range tables {
		require.True(t, tbl.Strict, "%s is not STRICT", tbl.Name)
	}
}

// Every table not on the instance-scoped allowlist carries circle_id NOT NULL REFERENCES
// circle(id). This is the schema half of the three-gate replacement for the tenant column DKP
// deletes outright (ADR-0002): a new table is tenancy-checked whether or not anybody remembered to
// think about it, because the allowlist is a closed list in a normative document.
func TestInstanceScopedAllowlist_MatchesTheAppliedSchema(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	allowed, err := canondoc.InstanceScopedTables()
	require.NoError(t, err)
	instanceScoped := map[string]bool{}
	for _, name := range allowed {
		instanceScoped[name] = true
	}

	tables, err := db.Tables(ctx)
	require.NoError(t, err)

	checked := 0
	for _, tbl := range tables {
		if instanceScoped[tbl.Name] || tbl.Name == tenantRoot {
			continue
		}
		checked++

		columns, err := db.Columns(ctx, tbl.Name)
		require.NoError(t, err)
		var circleID *ColumnInfo
		for i, c := range columns {
			if c.Name == "circle_id" {
				circleID = &columns[i]
			}
		}
		require.NotNil(t, circleID,
			"%s is not on the instance-scoped allowlist in canonical section 9, so it must carry "+
				"circle_id. Adding a table to that list is a reviewed decision, not a fix for "+
				"this failure", tbl.Name)
		require.True(t, circleID.NotNull, "%s.circle_id must be NOT NULL", tbl.Name)

		references := false
		fks, err := db.ForeignKeys(ctx, tbl.Name)
		require.NoError(t, err)
		for _, fk := range fks {
			if fk.References("circle_id", tenantRoot, "id") {
				references = true
			}
		}
		require.True(t, references, "%s.circle_id must REFERENCE circle(id)", tbl.Name)
	}

	// A gate reporting success over an empty search space is how a rule quietly stops being
	// enforced, so the gate is asked how much it looked at.
	require.Greater(t, checked, 5, "the tenancy check found almost no circle-scoped tables")
}

// The allowlist appears in canonical section 9 as prose and in the domain model as a table. They
// are compared here so the pair cannot drift; scripts/repo-gates.sh holds a third copy, and
// TestInstanceScopedAllowlist_MatchesRepoGates in test/repo compares that one.
func TestInstanceScopedAllowlist_MatchesTheDomainModel(t *testing.T) {
	t.Parallel()

	fromCanonical, err := canondoc.InstanceScopedTables()
	require.NoError(t, err)
	sort.Strings(fromCanonical)

	if diff := cmp.Diff(fromCanonical, domainModelTables(t, "Instance-scoped tables")); diff != "" {
		t.Errorf("the instance-scoped allowlist differs between canonical section 9 and the "+
			"domain model (-canonical +domain model):\n%s", diff)
	}
}

// The domain model lists every table the schema is supposed to have. Compared in both directions,
// so a table added to the schema without a row in the document fails here rather than arriving
// undocumented, and a documented table nobody created fails too.
func TestSchema_TableSet_MatchesTheDomainModel(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	documented := append(domainModelTables(t, "Instance-scoped tables"),
		domainModelTables(t, "Circle-scoped tables")...)
	sort.Strings(documented)

	tables, err := db.Tables(ctx)
	require.NoError(t, err)
	applied := make([]string, 0, len(tables))
	for _, tbl := range tables {
		applied = append(applied, tbl.Name)
	}

	if diff := cmp.Diff(documented, applied); diff != "" {
		t.Errorf("the applied schema differs from the domain model (-documented +applied):\n%s", diff)
	}
}

// Every enum column's CHECK, read back out of the DDL SQLite stored, is exactly what the catalogue
// renders. This is the reverse of `make gen`: generation puts the catalogue into the schema, and
// this proves the schema an officer's database actually runs still says the same thing.
//
// A hand-edited migration that widened an enum -- the tempting fix when a value is rejected -- is
// caught here, not by a value the API rejects and the database accepts.
func TestEnumColumns_AppliedSchema_MatchTheCatalogue(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	ddl := map[string]string{}
	tables, err := db.Tables(ctx)
	require.NoError(t, err)
	for _, tbl := range tables {
		ddl[tbl.Name] = tbl.DDL
	}

	bindings := dbschema.Bindings()
	require.NotEmpty(t, bindings)

	for _, b := range bindings {
		table, ok := ddl[b.Table]
		require.True(t, ok, "binding %s.%s names a table the schema does not have", b.Table, b.Column)

		want, err := b.Predicate()
		require.NoError(t, err)

		got, ok := dbschema.CheckConstraints(table)[b.ConstraintName()]
		require.True(t, ok,
			"%s has no CHECK named %s; db/schema.hcl must name it that so this comparison can "+
				"find it", b.Table, b.ConstraintName())
		require.Equal(t, want, got, "%s.%s", b.Table, b.Column)

		// The column must actually exist, or the CHECK is a predicate over nothing.
		columns, err := db.Columns(ctx, b.Table)
		require.NoError(t, err)
		found := false
		for _, c := range columns {
			if c.Name == b.Column {
				found = true
				// The wire value IS the database value, so an enum is TEXT. An enum stored as an
				// integer would need a translation layer, which canonical section 5 forbids.
				require.Equal(t, "text", strings.ToLower(c.Type), "%s.%s", b.Table, b.Column)
			}
		}
		require.True(t, found, "%s has no column %s", b.Table, b.Column)
	}
}

// Every enum in the catalogue reaches the database, or is recorded as deliberately unstored. The
// binding table already asserts that in Go; this asserts it against the schema, so an enum that is
// bound to a column nobody created cannot pass.
func TestEnumColumns_EveryStoredEnum_IsConstrainedSomewhere(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	constrained := map[string]bool{}
	tables, err := db.Tables(ctx)
	require.NoError(t, err)
	byTable := map[string]map[string]string{}
	for _, tbl := range tables {
		byTable[tbl.Name] = dbschema.CheckConstraints(tbl.DDL)
	}
	for _, b := range dbschema.Bindings() {
		if _, ok := byTable[b.Table][b.ConstraintName()]; ok {
			constrained[b.Enum] = true
		}
	}

	unstored := dbschema.UnstoredEnums()
	for _, e := range schemaenum.All() {
		if _, recorded := unstored[e.Name]; recorded {
			continue
		}
		require.True(t, constrained[e.Name],
			"enum %s is neither enforced by a CHECK in the applied schema nor recorded in "+
				"dbschema.UnstoredEnums", e.Name)
	}
}

// Ids are ULIDs in TEXT (canonical section 2), which is what makes them sortable cursors. An id
// column that became INTEGER would still work and would silently stop being one.
func TestSchema_EveryIDColumn_IsText(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	tables, err := db.Tables(ctx)
	require.NoError(t, err)

	for _, tbl := range tables {
		columns, err := db.Columns(ctx, tbl.Name)
		require.NoError(t, err)
		for _, c := range columns {
			if !strings.HasSuffix(c.Name, "_id") && c.Name != "id" {
				continue
			}
			// `instance.id` is the singleton's CHECK (id = 1), not a ULID; the whole table is one
			// row and there is nothing for a cursor to order. `discord_guild_id` and the Discord
			// role ids are Discord's snowflakes, which are theirs to shape, not ours.
			if tbl.Name == "instance" && c.Name == "id" {
				continue
			}
			if strings.HasPrefix(c.Name, "discord_") {
				continue
			}
			require.Equal(t, "text", strings.ToLower(c.Type),
				"%s.%s holds a ULID and must be TEXT", tbl.Name, c.Name)
		}
	}
}

// Times are Micros in INTEGER (canonical section 1). A `_at` column stored as TEXT would take an
// RFC 3339 string without complaint and compare wrongly against every other timestamp.
func TestSchema_EveryAtColumn_IsInteger(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	tables, err := db.Tables(ctx)
	require.NoError(t, err)

	checked := 0
	for _, tbl := range tables {
		columns, err := db.Columns(ctx, tbl.Name)
		require.NoError(t, err)
		for _, c := range columns {
			if !strings.HasSuffix(c.Name, "_at") {
				continue
			}
			checked++
			require.Equal(t, "integer", strings.ToLower(c.Type),
				"%s.%s is a Micros timestamp and must be INTEGER", tbl.Name, c.Name)
		}
	}
	require.Greater(t, checked, 20, "almost no _at columns were checked; the walk is wrong")
}

// domainModelTables reads one of the domain model's scope tables. Parsed rather than copied, for
// the reason the whole canondoc package exists.
func domainModelTables(t *testing.T, heading string) []string {
	t.Helper()
	doc, err := canondoc.LoadDomainModel()
	require.NoError(t, err)
	table, err := doc.TableUnder(heading, 0)
	require.NoError(t, err)
	names, err := table.Column("Table")
	require.NoError(t, err)

	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, canondoc.Unquote(n))
	}
	require.NotEmpty(t, out)
	sort.Strings(out)
	return out
}

// db/schema.hcl is the declarative truth and db/migrations-sqlite is what a database actually ran.
// ADR-0006 names the drift between them as a cost of checking generated output in: `make gen`
// proves they agree at generation time, and this proves it on every `make test`, with no Atlas
// installed.
//
// It compares table and column NAMES. Types, defaults and constraints are checked against the
// applied schema by the tests above, which read what SQLite enforces rather than what a file says;
// a type changed in the HCL with no migration is caught by `make gen` re-running the diff.
func TestSchemaHCL_DeclaredShape_MatchesTheAppliedSchema(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	src, err := os.ReadFile(filepath.Join(root, dbschema.SchemaHCLPath))
	require.NoError(t, err)
	declared, err := dbschema.HCLTables(string(src))
	require.NoError(t, err)

	applied := map[string][]string{}
	tables, err := db.Tables(ctx)
	require.NoError(t, err)
	for _, tbl := range tables {
		columns, err := db.Columns(ctx, tbl.Name)
		require.NoError(t, err)
		names := make([]string, 0, len(columns))
		for _, c := range columns {
			names = append(names, c.Name)
		}
		sort.Strings(names)
		applied[tbl.Name] = names
	}

	if diff := cmp.Diff(declared, applied); diff != "" {
		t.Errorf("%s and the applied migrations disagree (-declared +applied):\n%s\n"+
			"run `make gen` to author the missing migration",
			dbschema.SchemaHCLPath, diff)
	}
}

// A single-column foreign key proves the referenced row EXISTS. It does not prove the row belongs
// to this circle — so `tod_report.reporter_membership_id REFERENCES membership(id)` accepts a
// report filed in circle B naming a reporter from circle A, with both foreign keys satisfied and
// the tenant boundary gone.
//
// Every circle-scoped reference to another circle-scoped table therefore carries circle_id, and
// this is the gate that says so. Writing the twelfth one correctly is not something to leave to
// whoever adds the twelfth table.
func TestSchema_EveryCircleScopedForeignKey_NamesTheCircle(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	allowed, err := canondoc.InstanceScopedTables()
	require.NoError(t, err)
	instanceScoped := map[string]bool{}
	for _, name := range allowed {
		instanceScoped[name] = true
	}

	tables, err := db.Tables(ctx)
	require.NoError(t, err)

	checked := 0
	for _, tbl := range tables {
		// An instance-scoped table has no circle to match against: `api_token.membership_id` and
		// `identity.blocked_by_membership_id` name a membership in whichever circle it is in, and
		// that is the whole fact. Only a table that HAS a circle can be asked to match it.
		if instanceScoped[tbl.Name] || tbl.Name == tenantRoot {
			continue
		}
		fks, err := db.ForeignKeys(ctx, tbl.Name)
		require.NoError(t, err)

		for _, fk := range fks {
			if instanceScoped[fk.RefTable] || fk.RefTable == tenantRoot {
				continue // nothing tenant-shaped on the other end
			}
			checked++
			require.True(t, fk.Has("circle_id"),
				"%s references circle-scoped %s via %v without naming circle_id: a row in one "+
					"circle can point at a row in another", tbl.Name, fk.RefTable, fk.Columns)

			// The circle on this row must be compared against the circle on THAT row, not against
			// some other column that happens to be named circle_id on the parent.
			for i, col := range fk.Columns {
				if col == "circle_id" {
					require.Equal(t, "circle_id", fk.RefColumns[i],
						"%s maps circle_id to %s.%s", tbl.Name, fk.RefTable, fk.RefColumns[i])
				}
			}
		}
	}
	require.Greater(t, checked, 8, "almost no circle-scoped references were checked")
}

// The reviewer's case, made permanent: a report filed in one circle naming a reporter from another
// is refused by the database, not merely by whichever handler remembered to check.
func TestTodReport_ReporterFromAnotherCircle_IsRefused(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	mine := seed(t, ctx, db)
	rival := seedRivalCircle(t, ctx, db, mine)

	err := exec(t, ctx, db, `
		INSERT INTO tod_report (id, circle_id, target_id, kind, died_at, reported_at,
			reporter_membership_id, source, self_confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newIDs(rand.Reader).next(t), rival.CircleID, mine.TargetID, schemaenum.TodReportKindKill,
		int64(now), int64(now), mine.MembershipID, schemaenum.TodReportSourceLogLine,
		schemaenum.TodReportSelfConfidenceCertain)
	require.Error(t, err, "a report in one circle named a reporter from another")
	require.Contains(t, err.Error(), "FOREIGN KEY")

	// The rival's own membership works, so the constraint is a tenant boundary rather than a wall.
	mustExec(t, ctx, db, `
		INSERT INTO tod_report (id, circle_id, target_id, kind, died_at, reported_at,
			reporter_membership_id, source, self_confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newIDs(rand.Reader).next(t), rival.CircleID, mine.TargetID, schemaenum.TodReportKindKill,
		int64(now), int64(now), rival.MembershipID, schemaenum.TodReportSourceLogLine,
		schemaenum.TodReportSelfConfidenceCertain)
}

// A retraction that reached across circles would delete another tenant's ToD from their board
// while their own reports stayed put — the derivation folds a retraction by id, and it has no
// tenant of its own to check against.
func TestTodReport_RetractionAcrossCircles_IsRefused(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	mine := seed(t, ctx, db)
	rival := seedRivalCircle(t, ctx, db, mine)
	victim := insertReport(t, ctx, db, mine, now)

	err := exec(t, ctx, db, `
		INSERT INTO tod_report (id, circle_id, target_id, kind, died_at, reported_at,
			reporter_membership_id, source, self_confidence, retracts_report_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newIDs(rand.Reader).next(t), rival.CircleID, mine.TargetID,
		schemaenum.TodReportKindRetraction, int64(now), int64(now), rival.MembershipID,
		schemaenum.TodReportSourceManual, schemaenum.TodReportSelfConfidenceCertain, victim)
	require.Error(t, err, "a retraction reached into another circle's report log")
	require.Contains(t, err.Error(), "FOREIGN KEY")
}

// seedRivalCircle builds a second, fully separate circle: the tenant a cross-circle write would
// reach into.
func seedRivalCircle(t *testing.T, ctx context.Context, db *DB, mine fixture) fixture {
	t.Helper()
	id := newIDs(rand.Reader)
	rival := mine
	rival.CircleID = id.next(t)
	rival.IdentityID = id.next(t)
	rival.MembershipID = id.next(t)

	mustExec(t, ctx, db, `
		INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
		VALUES (?, ?, '9999999999', 'Rival', ?, ?)`,
		rival.IdentityID, mine.ProviderID, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO circle (id, name, name_norm, description, server, timezone,
			min_reporters_to_supersede, revoke_invalidates_invites, state, created_at, updated_at)
		VALUES (?, 'Rival Blue', 'rivalblue', '', ?, 'UTC', 1, 1, ?, ?, ?)`,
		rival.CircleID, schemaenum.ServerBlue, schemaenum.CircleStateActive, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO membership (id, circle_id, identity_id, kind, display_name, display_name_norm,
			role, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'Rival', 'rival', ?, ?, ?, ?)`,
		rival.MembershipID, rival.CircleID, rival.IdentityID, schemaenum.MembershipKindHuman,
		schemaenum.MembershipRoleOwner, int64(now), int64(now), int64(now))
	return rival
}
