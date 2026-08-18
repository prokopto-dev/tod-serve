package store

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// These tests are white-box — package store rather than store_test — for one reason: proving a
// trigger fires means issuing an UPDATE the store deliberately offers no method for. Exporting a
// "run this SQL" hole so an external test could reach it would put that hole in the product.
//
// Every test opens a real SQLite file in t.TempDir(). There are no mocks of the database anywhere
// in this repository: this schema's invariants are CHECK constraints, partial indexes and
// triggers, and a mock would assert that the mock has them.

// now is a fixed instant, so ids and timestamps are the same on every run. 2026-08-18T02:14:07Z in
// Micros — the timestamp on the Vulak`Aerr log line in the API design, for no better reason than
// that a real one reads better in a failure message than 0 does.
const now = core.Micros(1_786_918_447_000_000)

// testLogger discards output. A store demands a logger rather than defaulting to one, so this is
// how a test says it does not want the migration chatter.
func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// openMigrated returns an open, fully migrated store on a real file in t.TempDir().
func openMigrated(t *testing.T) (context.Context, *DB) {
	t.Helper()
	ctx := t.Context()
	db := openEmpty(t)
	require.NoError(t, db.Migrate(ctx))
	return ctx, db
}

// openEmpty returns an open store with no migrations applied.
func openEmpty(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tod.db")
	db, err := Open(t.Context(), path, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// exec runs a statement the store offers no query for. Trigger tests need it; nothing else does.
func exec(t *testing.T, ctx context.Context, db *DB, query string, args ...any) error {
	t.Helper()
	_, err := db.sql.ExecContext(ctx, query, args...)
	return err
}

// mustExec runs a statement and fails the test if it does not succeed.
func mustExec(t *testing.T, ctx context.Context, db *DB, query string, args ...any) {
	t.Helper()
	require.NoError(t, exec(t, ctx, db, query, args...))
}

// ids mints distinct, valid ULIDs for a test. It is a struct rather than a package-level generator
// because a shared one is package-level mutable state and every test here runs in parallel.
type ids struct{ gen *core.Generator }

func newIDs(entropy io.Reader) *ids { return &ids{gen: core.NewGenerator(entropy)} }

func (i *ids) next(t *testing.T) string {
	t.Helper()
	u, err := i.gen.New(now)
	require.NoError(t, err)
	return u.String()
}

// fixture is the smallest graph the foreign keys will accept: a provider, an identity, a circle, a
// membership and a raid target. Every trigger test needs most of it, because a row that violates
// an invariant still has to satisfy every REFERENCES on the way in.
type fixture struct {
	ProviderID   string
	LocalProvID  string
	OIDCProvID   string
	IdentityID   string
	LocalIdentID string
	OIDCIdentID  string
	CircleID     string
	MembershipID string
	TargetID     string
}

func seed(t *testing.T, ctx context.Context, db *DB) fixture {
	t.Helper()
	id := newIDs(rand.Reader)
	f := fixture{
		ProviderID:   id.next(t),
		LocalProvID:  id.next(t),
		OIDCProvID:   id.next(t),
		IdentityID:   id.next(t),
		LocalIdentID: id.next(t),
		OIDCIdentID:  id.next(t),
		CircleID:     id.next(t),
		MembershipID: id.next(t),
		TargetID:     id.next(t),
	}

	// A discord provider: verifiable, and CHECK ((kind='discord') = (client_id IS NOT NULL)) means
	// it cannot exist without an operator application.
	mustExec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			client_id, created_at, updated_at)
		VALUES (?, 'discord', ?, 'Sign in with Discord', 1, 1, 'app-id', ?, ?)`,
		f.ProviderID, schemaenum.IdentityProviderKindDiscord, int64(now), int64(now))

	// A local provider: unverifiable by CHECK, which is what makes it unlinkable.
	mustExec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			created_at, updated_at)
		VALUES (?, 'local', ?, 'Local account', 0, 0, ?, ?)`,
		f.LocalProvID, schemaenum.IdentityProviderKindLocal, int64(now), int64(now))

	// A second verifiable provider, so identity_link has two legal participants to link. Any
	// number of oidc rows is permitted; discord and local are capped at one each.
	mustExec(t, ctx, db, `
		INSERT INTO identity_provider (id, key, kind, display_name, enabled, verifiable_subject,
			issuer, subject_claim, created_at, updated_at)
		VALUES (?, 'authentik', ?, 'Sign in with Authentik', 1, 1,
			'https://id.example.com', 'sub', ?, ?)`,
		f.OIDCProvID, schemaenum.IdentityProviderKindOIDC, int64(now), int64(now))

	mustExec(t, ctx, db, `
		INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
		VALUES (?, ?, '1234567890', 'Tankguy', ?, ?)`,
		f.IdentityID, f.ProviderID, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
		VALUES (?, ?, 'tankguy@example.com', 'Tankguy', ?, ?)`,
		f.OIDCIdentID, f.OIDCProvID, int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO identity (id, provider_id, subject, display_name, created_at, updated_at)
		VALUES (?, ?, 'tanky', 'Tanky', ?, ?)`,
		f.LocalIdentID, f.LocalProvID, int64(now), int64(now))

	mustExec(t, ctx, db, `
		INSERT INTO circle (id, name, name_norm, description, server, timezone,
			min_reporters_to_supersede, revoke_invalidates_invites, state, created_at, updated_at)
		VALUES (?, 'Riot Blue', 'riotblue', '', ?, 'UTC', 1, 1, ?, ?, ?)`,
		f.CircleID, schemaenum.ServerBlue, schemaenum.CircleStateActive, int64(now), int64(now))

	mustExec(t, ctx, db, `
		INSERT INTO membership (id, circle_id, identity_id, kind, display_name, display_name_norm,
			role, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'Tankguy', 'tankguy', ?, ?, ?, ?)`,
		f.MembershipID, f.CircleID, f.IdentityID, schemaenum.MembershipKindHuman,
		schemaenum.MembershipRoleOwner, int64(now), int64(now), int64(now))

	mustExec(t, ctx, db, `
		INSERT INTO raid_target (id, name, name_norm, zone, zone_norm, expansion, category,
			is_quake_target, state, created_at, updated_at)
		VALUES (?, 'Vulak`+"`"+`Aerr', 'vulakaerr', 'Temple of Veeshan', 'templeofveeshan', ?, ?,
			1, ?, ?, ?)`,
		f.TargetID, schemaenum.RaidTargetExpansionVelious, schemaenum.RaidTargetCategoryNToV,
		schemaenum.RaidTargetStateActive, int64(now), int64(now))

	return f
}

// insertReport puts one kill report in, and returns its id.
func insertReport(t *testing.T, ctx context.Context, db *DB, f fixture, diedAt core.Micros) string {
	t.Helper()
	id := newIDs(rand.Reader).next(t)
	mustExec(t, ctx, db, `
		INSERT INTO tod_report (id, circle_id, target_id, kind, died_at, reported_at,
			reporter_membership_id, source, self_confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, f.CircleID, f.TargetID, schemaenum.TodReportKindKill, int64(diedAt), int64(now),
		f.MembershipID, schemaenum.TodReportSourceLogLine, schemaenum.TodReportSelfConfidenceCertain)
	return id
}
