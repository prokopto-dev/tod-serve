package store

import (
	"crypto/rand"
	"database/sql"
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

// The upgrade path that the empty-database tests cannot see.
//
// Migration 000003 rebuilds `identity_provider`, and a rebuild drops the table that `identity`,
// `auth_flow` and `credential_ticket` all point at. goose runs migrations in a transaction and
// SQLite makes `PRAGMA foreign_keys` a no-op inside one, so the pragma Atlas wrote would have left
// enforcement ON and `DROP TABLE` would have failed on every database with a single provider-owned
// row in it. Every test here migrates an empty database, which is exactly why none of them noticed.
//
// This one seeds schema version 2 the way a real instance looks and then upgrades.
func TestMigrate_PopulatedDatabaseAtVersionTwo_UpgradesWithItsReferencingRows(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db := openEmpty(t)

	provider, err := db.provider()
	require.NoError(t, err)
	_, err = provider.UpTo(ctx, 2)
	require.NoError(t, err)

	version, err := db.SchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), version)

	id := newIDs(rand.Reader)
	discordProv, localProv := id.next(t), id.next(t)
	identityID, flowID, ticketID := id.next(t), id.next(t), id.next(t)

	mustExec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			client_id, created_at, updated_at)
		VALUES (?, 'discord', 'discord', 'Sign in with Discord', 1, 1, 'app-id', ?, ?)`,
		discordProv, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			created_at, updated_at)
		VALUES (?, 'local', 'local', 'Local account', 0, 0, ?, ?)`,
		localProv, int64(now), int64(now))

	// One row in each table that references a provider. These are what the old pragma would have
	// tripped over.
	mustExec(t, ctx, db, `
		INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
		VALUES (?, ?, '1234567890', 'Tankguy', ?, ?)`,
		identityID, discordProv, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO auth_flow (id, state, pkce_verifier, provider_id, expires_at, created_at, updated_at)
		VALUES (?, 'state-1', 'verifier-1', ?, ?, ?, ?)`,
		flowID, discordProv, int64(now)+600_000_000, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO credential_ticket (id, ticket_hash, provider_id, subject, display_name,
			guild_roles_json, expires_at, created_at, updated_at)
		VALUES (?, X'0011', ?, '1234567890', 'Tankguy', '{}', ?, ?, ?)`,
		ticketID, discordProv, int64(now)+120_000_000, int64(now), int64(now))

	require.NoError(t, db.Migrate(ctx))

	version, err = db.SchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, latestMigration(t, db), version,
		"the upgrade stopped short of the newest migration")

	// Every row survived, still pointing at the provider it pointed at.
	for _, q := range []struct {
		what  string
		query string
		arg   string
	}{
		{"providers", `SELECT COUNT(*) FROM identity_provider`, ""},
		{"the identity", `SELECT COUNT(*) FROM identity WHERE provider_id = ?`, discordProv},
		{"the auth flow", `SELECT COUNT(*) FROM auth_flow WHERE provider_id = ?`, discordProv},
		{"the ticket", `SELECT COUNT(*) FROM credential_ticket WHERE provider_id = ?`, discordProv},
	} {
		var count int
		var row *sql.Row
		if q.arg == "" {
			row = db.sql.QueryRowContext(ctx, q.query)
		} else {
			row = db.sql.QueryRowContext(ctx, q.query, q.arg)
		}
		require.NoError(t, row.Scan(&count))
		if q.what == "providers" {
			require.Equal(t, 2, count, "both providers survived the rebuild")
			continue
		}
		require.Equal(t, 1, count, "%s survived the rebuild still referencing its provider", q.what)
	}

	// The deferred checks ran at COMMIT rather than being skipped, so nothing is dangling.
	require.NoError(t, db.ForeignKeyCheck(ctx))
	require.NoError(t, db.IntegrityCheck(ctx))

	// The corrected CHECK is in force on the rebuilt table.
	require.Error(t, exec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			created_at, updated_at)
		VALUES (?, 'keycloak', 'oidc', 'Keycloak', 1, 1, ?, ?)`,
		id.next(t), int64(now), int64(now)),
		"an oidc row with no client id has no audience to check and is refused")

	// And the trigger the rebuild had to drop is back. A rebuild that silently lost it would leave
	// `local` identities linkable, which is the hole identity_link exists not to open.
	linkedID := id.next(t)
	mustExec(t, ctx, db, `
		INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
		VALUES (?, ?, 'tanky', 'Tanky', ?, ?)`,
		linkedID, localProv, int64(now), int64(now))
	require.Error(t, exec(t, ctx, db, `
		INSERT INTO identity_link (id, primary_identity_id, linked_identity_id, method,
			linked_by_membership_id, linked_at)
		VALUES (?, ?, ?, 'officer_asserted', ?, ?)`,
		id.next(t), identityID, linkedID, id.next(t), int64(now)),
		"trg_identity_link_requires_verifiable_participants must have survived the table rebuild")
}

// The one shape that cannot be carried forward, failing the way it should.
//
// Under version 2 an `oidc` row was FORCED to have a NULL client_id — that CHECK is the bug 000003
// corrects — and such a row can never verify anything, because `aud = client_id` is the audience
// check and there is nothing to compare against. The migration will not invent a client id, and it
// will not drop the operator's row. It fails, and because it is still transactional it fails
// CLEANLY: the database stays at version 2 with every row intact, which is what `-- +goose NO
// TRANSACTION` would have given up.
func TestMigrate_VersionTwoRowThatCannotBeCarriedForward_FailsAtomically(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db := openEmpty(t)

	provider, err := db.provider()
	require.NoError(t, err)
	_, err = provider.UpTo(ctx, 2)
	require.NoError(t, err)

	id := newIDs(rand.Reader)
	mustExec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			issuer, jwks_uri, created_at, updated_at)
		VALUES (?, 'authentik', 'oidc', 'Authentik', 1, 1,
			'https://id.example.com', 'https://id.example.com/jwks', ?, ?)`,
		id.next(t), int64(now), int64(now))

	err = db.Migrate(ctx)

	require.Error(t, err)
	require.Contains(t, err.Error(), "ck_identity_provider_application_matches_kind",
		"the failure names the constraint, so the operator knows which column to fill in")

	version, err := db.SchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), version, "a failed migration leaves the schema where it was")

	var count int
	require.NoError(t, db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM identity_provider`).Scan(&count))
	require.Equal(t, 1, count, "and leaves the operator's row alone rather than dropping it")
	require.NoError(t, db.IntegrityCheck(ctx))

	// And the trigger the rebuild drops is still there, which is the assertion this test was
	// missing when the drop sat above BEGIN and auto-committed on its own. A database left at
	// version 2 with `identity_link` unguarded lets a `local` identity be linked to a verifiable
	// one — the hole that table exists not to open — and nothing else would have noticed.
	localProv, verifiableProv := id.next(t), id.next(t)
	localIdent, verifiableIdent := id.next(t), id.next(t)
	mustExec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			created_at, updated_at)
		VALUES (?, 'local', 'local', 'Local account', 0, 0, ?, ?)`,
		localProv, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			client_id, created_at, updated_at)
		VALUES (?, 'discord', 'discord', 'Sign in with Discord', 1, 1, 'app-id', ?, ?)`,
		verifiableProv, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
		VALUES (?, ?, 'tanky', 'Tanky', ?, ?)`,
		localIdent, localProv, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
		VALUES (?, ?, '1234567890', 'Tankguy', ?, ?)`,
		verifiableIdent, verifiableProv, int64(now), int64(now))

	require.Error(t, exec(t, ctx, db, `
		INSERT INTO identity_link (id, primary_identity_id, linked_identity_id, method,
			linked_by_membership_id, linked_at)
		VALUES (?, ?, ?, 'officer_asserted', ?, ?)`,
		id.next(t), verifiableIdent, localIdent, id.next(t), int64(now)),
		"a failed migration must not leave identity_link unguarded")
}

// latestMigration is the highest embedded migration version.
//
// It is read from the sources rather than written down, because a literal here has to be edited by
// whoever adds the next migration — and the one thing worse than a test that needs editing is one
// that silently keeps passing while asserting a version two behind the binary.
func latestMigration(t *testing.T, db *DB) int64 {
	t.Helper()
	provider, err := db.provider()
	require.NoError(t, err)
	sources := provider.ListSources()
	require.NotEmpty(t, sources)
	return sources[len(sources)-1].Version
}

// **The `circle` rebuild, on a database that has rows referencing it.**
//
// 000004 adds `circle.deleted_at`, which SQLite can only do by rebuilding the table — and `circle`
// is the one nearly every circle-scoped table has a foreign key to, three of which are APPEND-ONLY
// and cannot be deleted from at all. That combination is what makes the rebuild's implicit
// `DELETE FROM` impossible rather than merely unwanted, and it only bites a populated database:
// every other migration test in this package starts from an empty one, which is the single shape
// this cannot fail in.
//
// It is the same trap 000003 hit for `identity_provider`, on a bigger table, so it gets the same
// test rather than a comment saying it was thought about.
func TestMigrate_APopulatedDatabase_SurvivesTheCircleRebuild(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db := openEmpty(t)

	provider, err := db.provider()
	require.NoError(t, err)
	// Every migration except the last, so the rows below are written against the schema an
	// upgrading operator actually has.
	latest := latestMigration(t, db)
	_, err = provider.UpTo(ctx, latest-1)
	require.NoError(t, err)

	id := newIDs(rand.Reader)
	circleID, providerID, identityID := id.next(t), id.next(t), id.next(t)
	memberID, inviteID, targetID := id.next(t), id.next(t), id.next(t)

	mustExec(t, ctx, db, `
		INSERT INTO circle (id, name, name_norm, description, server, timezone,
			min_reporters_to_supersede, revoke_invalidates_invites, state, created_at, updated_at)
		VALUES (?, 'Riot Blue', 'riotblue', '', 'blue', 'UTC', 1, 1, 'active', ?, ?)`,
		circleID, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			created_at, updated_at)
		VALUES (?, 'local', 'local', 'This server', 1, 0, ?, ?)`,
		providerID, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
		VALUES (?, ?, 'subject-1', 'Tankguy', ?, ?)`,
		identityID, providerID, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO membership (id, circle_id, identity_id, kind, display_name, display_name_norm,
			role, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, 'human', 'Tankguy', 'tankguy', 'owner', ?, ?, ?)`,
		memberID, circleID, identityID, int64(now), int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO invite (id, circle_id, code_hash, code_prefix, role, max_uses, uses,
			expires_at, created_by_membership_id, minted_by_kind, note, created_at, updated_at)
		VALUES (?, ?, X'00', '4KQ7M', 'member', 1, 0, ?, ?, 'session', '', ?, ?)`,
		inviteID, circleID, int64(now)+600_000_000, memberID, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO raid_target (id, name, name_norm, zone, zone_norm, expansion, category,
			is_quake_target, state, created_at, updated_at)
		VALUES (?, 'Vulak', 'vulak', 'Temple of Veeshan', 'tov', 'velious', 'ntov', 1, 'active', ?, ?)`,
		targetID, int64(now), int64(now))

	// The two APPEND-ONLY rows. A rebuild that dropped `circle` with foreign keys enforced would
	// have to delete these to succeed, and a trigger refuses to let it.
	mustExec(t, ctx, db, `
		INSERT INTO tod_report (id, circle_id, target_id, kind, died_at, reported_at,
			reporter_membership_id, source, self_confidence)
		VALUES (?, ?, ?, 'kill', ?, ?, ?, 'manual', 'certain')`,
		id.next(t), circleID, targetID, int64(now), int64(now), memberID)
	mustExec(t, ctx, db, `
		INSERT INTO audit_log (id, circle_id, actor_membership_id, action, entity_type,
			detail_json, hash, created_at)
		VALUES (?, ?, ?, 'circle.created', 'circle', '{}', X'01', ?)`,
		id.next(t), circleID, memberID, int64(now))

	// The upgrade an operator runs.
	require.NoError(t, db.Migrate(ctx), "the upgrade failed on a POPULATED database")

	version, err := db.SchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, latest, version)

	for _, table := range []string{
		"circle", "membership", "invite", "tod_report", "audit_log",
	} {
		var count int
		// The table name is a literal from the list above, never caller input.
		row := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table)
		require.NoError(t, row.Scan(&count))
		require.Equal(t, 1, count, "the %s row did not survive the rebuild", table)
	}

	require.NoError(t, db.ForeignKeyCheck(ctx))
	require.NoError(t, db.IntegrityCheck(ctx))

	// The new column is there and every existing circle is LIVE. A rebuild that defaulted it to
	// anything else would delete every circle on the instance at upgrade time, silently.
	var live int
	require.NoError(t, db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM circle WHERE deleted_at IS NULL`).Scan(&live))
	require.Equal(t, 1, live)

	// And the trigger the rebuild had to drop is back and FIRING. Asserting that it aborts rather
	// than that it appears in `sqlite_master` is the point: a rebuild leaves the catalogue looking
	// right. Without it `circle.server` would be immutable only for callers who came through the
	// API, which is the half of ADR-0009 that is not a `WHERE` clause someone forgets.
	require.Error(t, exec(t, ctx, db,
		`UPDATE circle SET server = 'green' WHERE id = ?`, circleID),
		"trg_circle_server_is_immutable must have survived the table rebuild")
}
