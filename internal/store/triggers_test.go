package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// TestAppendOnly_TriggersFire_AfterAllMigrations is the point of the trigger migration.
//
// It runs after EVERY migration has applied, because SQLite rebuilds a table for any ALTER it
// cannot do in place and a rebuild drops every trigger on that table, silently. A test that ran
// against migration 2 would keep passing while migration 9 quietly repealed it.
//
// It asserts the trigger ABORTS a write. Asserting that a row exists in sqlite_master would pass
// for a trigger that exists and does not fire, which is the same shape of "green over nothing" the
// repository gates against everywhere else.
func TestAppendOnly_TriggersFire_AfterAllMigrations(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	seedAppendOnlyRows(t, ctx, db, f)

	for _, table := range appendOnlyTables(t) {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			before := count(t, ctx, db, table)
			require.Positive(t, before,
				"%s has no row, so a FOR EACH ROW trigger would not fire and this test would "+
					"pass over an empty table", table)

			// `SET c = c` changes nothing, which is the point: even a write that would have been
			// a no-op is refused, so there is no "harmless UPDATE" anybody can argue for.
			column := firstColumn(t, ctx, db, table)
			err := exec(t, ctx, db, fmt.Sprintf("UPDATE %s SET %s = %s", table, column, column))
			require.Error(t, err, "UPDATE %s was permitted; the append-only trigger is gone", table)
			require.Contains(t, err.Error(), "append-only", "UPDATE %s: %v", table, err)

			err = exec(t, ctx, db, "DELETE FROM "+table)
			require.Error(t, err, "DELETE FROM %s was permitted; the append-only trigger is gone", table)
			require.Contains(t, err.Error(), "append-only", "DELETE %s: %v", table, err)

			require.Equal(t, before, count(t, ctx, db, table), "%s lost rows", table)
		})
	}
}

// The two normative documents name the append-only set in two places: the Mutability column of the
// domain model's scope tables, and the sentence in canonical section 10. They are compared here
// rather than one of them being trusted, because the copy that quietly shrinks is the one that
// stops a table being protected.
func TestAppendOnly_TheTwoDocuments_NameTheSameTables(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	known := map[string]bool{}
	tables, err := db.Tables(ctx)
	require.NoError(t, err)
	for _, tbl := range tables {
		known[tbl.Name] = true
	}

	doc, err := canondoc.LoadCanonical()
	require.NoError(t, err)
	named, err := doc.BacktickedListAfter("**Append-only, enforced by database trigger.**")
	require.NoError(t, err)

	// The sentence also backticks `UPDATE` and `DELETE`. Keeping only identifiers that are real
	// tables is what makes reading a prose rule mechanical rather than fragile.
	var fromCanonical []string
	for _, name := range named {
		if known[name] {
			fromCanonical = append(fromCanonical, name)
		}
	}
	sort.Strings(fromCanonical)

	if diff := cmp.Diff(appendOnlyTables(t), fromCanonical); diff != "" {
		t.Errorf("the append-only set differs between the domain model and canonical section 10 "+
			"(-domain model +canonical):\n%s", diff)
	}
}

// An append-only table has no updated_at, and a mutable one has one. Canonical section 8 states it
// as a convention; this is what makes it true of the applied schema, in both directions — a
// mutable table with no updated_at is a row whose age nothing can report.
func TestSchema_UpdatedAt_IsPresentExactlyOnMutableTables(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)

	appendOnly := map[string]bool{}
	for _, name := range appendOnlyTables(t) {
		appendOnly[name] = true
	}

	tables, err := db.Tables(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tables)

	for _, tbl := range tables {
		columns, err := db.Columns(ctx, tbl.Name)
		require.NoError(t, err)
		has := false
		for _, c := range columns {
			if c.Name == "updated_at" {
				has = true
			}
		}
		require.Equal(t, !appendOnly[tbl.Name], has,
			"%s: updated_at must be present exactly on mutable tables", tbl.Name)
	}
}

// A circle is pinned to one server, immutably (ADR-0009). The edge answers 422 field_immutable;
// this is what makes that answer true rather than merely usual.
func TestCircleServer_Update_IsRefusedByTheDatabase(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)

	err := exec(t, ctx, db, "UPDATE circle SET server = ? WHERE id = ?",
		schemaenum.ServerGreen, f.CircleID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "immutable")

	// Everything else about a circle is editable, so the trigger must not be a blanket refusal.
	mustExec(t, ctx, db, "UPDATE circle SET description = 'Blue crew' WHERE id = ?", f.CircleID)

	// And rewriting server to the value it already holds is not a change, so it is permitted:
	// otherwise an UPDATE that names every column would fail for no reason a caller could act on.
	mustExec(t, ctx, db, "UPDATE circle SET server = ? WHERE id = ?",
		schemaenum.ServerBlue, f.CircleID)
}

// A `local` identity can never be linked: 04-identity section 3. Silently unifying an unverified
// identity with a verified one would let anyone who can assert a display name inherit another
// person's standing -- and because a link revokes across the whole set, it would do so while the
// officers believed the opposite.
func TestIdentityLink_LocalProvider_Rejected(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	id := newIDs(rand.Reader)

	err := exec(t, ctx, db, `
		INSERT INTO identity_link (id, primary_identity_id, linked_identity_id, method,
			linked_by_membership_id, linked_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id.next(t), f.IdentityID, f.LocalIdentID, schemaenum.IdentityLinkMethodOfficerAsserted,
		f.MembershipID, int64(now))
	require.Error(t, err)
	require.Contains(t, err.Error(), "verifiable_subject")

	// Two verifiable identities link fine, so the trigger is a gate rather than a wall.
	mustExec(t, ctx, db, `
		INSERT INTO identity_link (id, primary_identity_id, linked_identity_id, method,
			linked_by_membership_id, linked_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id.next(t), f.IdentityID, f.OIDCIdentID, schemaenum.IdentityLinkMethodOfficerAsserted,
		f.MembershipID, int64(now))
}

// A credential_ticket is redeemable once. A second redemption would mint a second PAT for one
// authorization, which is the whole thing the ticket exists to prevent -- so it is refused by the
// database rather than only by the `WHERE consumed_at IS NULL` a caller might forget.
func TestCredentialTicket_SecondConsumption_Refused(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	id := newIDs(rand.Reader)
	hash := []byte("ticket-hash-0001")

	mustExec(t, ctx, db, `
		INSERT INTO credential_ticket (id, ticket_hash, provider_id, subject, display_name,
			guild_roles_json, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, '1234567890', 'Tankguy', '{}', ?, ?, ?)`,
		id.next(t), hash, f.ProviderID, int64(now)+120*1_000_000, int64(now), int64(now))

	mustExec(t, ctx, db, "UPDATE credential_ticket SET consumed_at = ? WHERE ticket_hash = ?",
		int64(now), hash)

	err := exec(t, ctx, db, "UPDATE credential_ticket SET consumed_at = ? WHERE ticket_hash = ?",
		int64(now)+1, hash)
	require.Error(t, err)
	require.Contains(t, err.Error(), "single-use")
}

// The TTL is a CHECK rather than a Go comparison, so a ticket that outlives 120 seconds cannot be
// written at all. TestCredentialTicket_After120s_Refused checks the clock; this checks that there
// is no row for it to check.
func TestCredentialTicket_LongerThan120s_IsUnrepresentable(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)

	err := exec(t, ctx, db, `
		INSERT INTO credential_ticket (id, ticket_hash, provider_id, subject, display_name,
			guild_roles_json, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, '1234567890', 'Tankguy', '{}', ?, ?, ?)`,
		newIDs(rand.Reader).next(t), []byte("ticket-hash-0002"), f.ProviderID,
		int64(now)+3600*1_000_000, int64(now), int64(now))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ck_credential_ticket_ttl")
}

// The facts on a ticket are what the provider said at the callback, and the guild roles on it ARE
// the guild gate's input. A gate evaluated against an edited copy is a gate that is not evaluated.
func TestCredentialTicket_Facts_CannotBeEditedAfterMinting(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	hash := []byte("ticket-hash-0003")

	mustExec(t, ctx, db, `
		INSERT INTO credential_ticket (id, ticket_hash, provider_id, subject, display_name,
			guild_roles_json, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, '1234567890', 'Tankguy', '{"g":["r1"]}', ?, ?, ?)`,
		newIDs(rand.Reader).next(t), hash, f.ProviderID, int64(now)+120*1_000_000,
		int64(now), int64(now))

	err := exec(t, ctx, db,
		`UPDATE credential_ticket SET guild_roles_json = '{"g":["r1","r2"]}' WHERE ticket_hash = ?`,
		hash)
	require.Error(t, err)
	require.Contains(t, err.Error(), "immutable")
}

// The partial unique index is the entire revocation mechanism: a revoked person redeeming a fresh
// invite must hit the EXISTING row rather than create a second one. There is no delete-membership
// operation at all, so if this index goes, revocation goes with it.
func TestMembership_SecondRowForOneIdentity_IsUnrepresentable(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)

	mustExec(t, ctx, db, "UPDATE membership SET revoked_at = ?, revoked_by_membership_id = ? WHERE id = ?",
		int64(now), f.MembershipID, f.MembershipID)

	err := exec(t, ctx, db, `
		INSERT INTO membership (id, circle_id, identity_id, kind, display_name, display_name_norm,
			role, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'Tankguy', 'tankguy', ?, ?, ?, ?)`,
		newIDs(rand.Reader).next(t), f.CircleID, f.IdentityID, schemaenum.MembershipKindHuman,
		schemaenum.MembershipRoleMember, int64(now), int64(now), int64(now))
	require.Error(t, err, "a revoked identity got a second membership row")
	// SQLite names the columns rather than the index, so this is the shape of the failure the
	// partial unique index produces: the pair (circle_id, identity_id) already exists.
	require.Contains(t, err.Error(), "membership.circle_id")
	require.Contains(t, err.Error(), "membership.identity_id")
}

// appendOnlyTables reads the set out of the domain model's own Mutability column. Parsed rather
// than copied: a list typed into this file would make the test agree with the copy, and the pair
// that drifts is the schema and the document.
func appendOnlyTables(t *testing.T) []string {
	t.Helper()
	doc, err := canondoc.LoadDomainModel()
	require.NoError(t, err)

	var out []string
	for _, heading := range []string{"Instance-scoped tables", "Circle-scoped tables"} {
		table, err := doc.TableUnder(heading, 0)
		require.NoError(t, err)
		names, err := table.Column("Table")
		require.NoError(t, err)
		mutability, err := table.Column("Mutability")
		require.NoError(t, err)
		for i, m := range mutability {
			if strings.Contains(m, "append-only") {
				out = append(out, canondoc.Unquote(names[i]))
			}
		}
	}
	require.NotEmpty(t, out, "the domain model lists no append-only table; the parse is wrong")
	sort.Strings(out)
	return out
}

// seedAppendOnlyRows puts exactly one row into each append-only table, so that a FOR EACH ROW
// trigger has something to fire on. A trigger test over an empty table passes for the wrong reason.
func seedAppendOnlyRows(t *testing.T, ctx context.Context, db *DB, f fixture) {
	t.Helper()
	id := newIDs(rand.Reader)

	insertReport(t, ctx, db, f, now)

	mustExec(t, ctx, db, `
		INSERT INTO quake_event (id, circle_id, occurred_at, reported_at,
			reported_by_membership_id, source, note)
		VALUES (?, ?, ?, ?, ?, ?, '')`,
		id.next(t), f.CircleID, int64(now), int64(now), f.MembershipID,
		schemaenum.TodReportSourceManual)

	inviteID := id.next(t)
	mustExec(t, ctx, db, `
		INSERT INTO invite (id, circle_id, code_hash, code_prefix, role, max_uses, uses,
			expires_at, created_by_membership_id, minted_by_kind, note, created_at, updated_at)
		VALUES (?, ?, ?, 'TODI-4KQ7M', ?, 1, 0, ?, ?, ?, '', ?, ?)`,
		inviteID, f.CircleID, []byte("code-hash-0001"), schemaenum.MembershipRoleMember,
		int64(now)+3600*1_000_000, f.MembershipID, schemaenum.InviteMintedByKindSession,
		int64(now), int64(now))
	mustExec(t, ctx, db, `
		INSERT INTO invite_redemption (id, circle_id, invite_id, membership_id, identity_id,
			created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id.next(t), f.CircleID, inviteID, f.MembershipID, f.IdentityID, int64(now))

	mustExec(t, ctx, db, `
		INSERT INTO identity_link (id, primary_identity_id, linked_identity_id, method,
			linked_by_membership_id, linked_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id.next(t), f.IdentityID, f.OIDCIdentID, schemaenum.IdentityLinkMethodOfficerAsserted,
		f.MembershipID, int64(now))

	mustExec(t, ctx, db, `
		INSERT INTO audit_log (id, circle_id, actor_membership_id, action, entity_type, entity_id,
			detail_json, prev_hash, hash, created_at)
		VALUES (?, ?, ?, 'circle.created', 'circle', ?, '{}', NULL, ?, ?)`,
		id.next(t), f.CircleID, f.MembershipID, f.CircleID, []byte("hash-0001"), int64(now))

	mustExec(t, ctx, db, `
		INSERT INTO event_outbox (id, circle_id, kind, payload_json, created_at)
		VALUES (?, ?, 'tod.changed', '{}', ?)`,
		id.next(t), f.CircleID, int64(now))
}

func count(t *testing.T, ctx context.Context, db *DB, table string) int {
	t.Helper()
	var n int
	// The table name comes from the applied schema, never from a request. See DB.Columns.
	row := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table) //nolint:gosec // schema-derived
	require.NoError(t, row.Scan(&n))
	return n
}

func firstColumn(t *testing.T, ctx context.Context, db *DB, table string) string {
	t.Helper()
	columns, err := db.Columns(ctx, table)
	require.NoError(t, err)
	require.NotEmpty(t, columns)
	return columns[0].Name
}

// SQLite treats a CHECK whose expression evaluates to NULL as SATISFIED. The domain model's three
// window rules, spelled the obvious way, therefore accept a fixed timer with a NULL close offset,
// an unknown timer that kept a close offset, and any row whose ordering comparison went NULL —
// each of which reaches the consensus derivation as a window it cannot read.
//
// Both tables carry the same four constraints, so both are driven through the same table.
func TestTimerWindow_MalformedRows_AreRefused(t *testing.T) {
	t.Parallel()

	type offsets struct{ open, close *int64 }
	at := func(v int64) *int64 { return &v }

	tests := []struct {
		name   string
		kind   string
		window offsets
		accept bool
		why    string
	}{
		{
			name: "fixed, one instant", kind: schemaenum.RaidTargetTimerWindowKindFixed,
			window: offsets{at(604800), at(604800)}, accept: true,
			why: "a fixed timer is a point, and fixed_grace_seconds is what makes it renderable",
		},
		{
			name: "variance, a real band", kind: schemaenum.RaidTargetTimerWindowKindVariance,
			window: offsets{at(561600), at(648000)}, accept: true,
			why: `"7 days plus or minus 12h", as two offsets rather than a sign convention`,
		},
		{
			name: "unknown, no offsets at all", kind: schemaenum.RaidTargetTimerWindowKindUnknown,
			window: offsets{nil, nil}, accept: true,
			why: "an unseeded instance reports no_timer and still records ToDs correctly",
		},
		{
			name: "fixed with no offsets", kind: schemaenum.RaidTargetTimerWindowKindFixed,
			window: offsets{nil, nil},
			why:    "a fixed timer with nothing to be fixed at is not a timer",
		},
		{
			name: "fixed with only an open offset", kind: schemaenum.RaidTargetTimerWindowKindFixed,
			window: offsets{at(604800), nil},
			why:    "the NULL comparison the old CHECK made here was read as satisfied",
		},
		{
			name: "variance with only an open offset", kind: schemaenum.RaidTargetTimerWindowKindVariance,
			window: offsets{at(561600), nil},
			why:    "an offset alone is not a window; the band has no far edge",
		},
		{
			name: "variance with only a close offset", kind: schemaenum.RaidTargetTimerWindowKindVariance,
			window: offsets{nil, at(648000)},
			why:    "the unknown check only ever inspected the open offset",
		},
		{
			name: "unknown that kept a close offset", kind: schemaenum.RaidTargetTimerWindowKindUnknown,
			window: offsets{nil, at(648000)},
			why:    "an unknown window that half-answers is worse than one that says nothing",
		},
		{
			name: "variance whose offsets are equal", kind: schemaenum.RaidTargetTimerWindowKindVariance,
			window: offsets{at(604800), at(604800)},
			why:    "equal offsets IS a fixed timer, and calling it variance loses spawn_at",
		},
		{
			name: "variance closing before it opens", kind: schemaenum.RaidTargetTimerWindowKindVariance,
			window: offsets{at(648000), at(561600)},
			why:    "a window that closes before it opens renders as permanently overdue",
		},
		{
			name: "fixed whose offsets differ", kind: schemaenum.RaidTargetTimerWindowKindFixed,
			window: offsets{at(561600), at(648000)},
			why:    "spawn_at is present iff the timer is fixed, so a fixed band would lie",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, db := openMigrated(t)
			f := seed(t, ctx, db)

			catalogue := exec(t, ctx, db, `
				INSERT INTO raid_target_timer (target_id, server, window_kind,
					window_open_offset_seconds, window_close_offset_seconds, fixed_grace_seconds,
					note, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 900, '', ?, ?)`,
				f.TargetID, schemaenum.ServerBlue, tc.kind, tc.window.open, tc.window.close,
				int64(now), int64(now))

			// circle_timer_override carries the same window columns and must answer identically:
			// an officer overriding a disputed timer is the likeliest source of a malformed one.
			override := exec(t, ctx, db, `
				INSERT INTO circle_timer_override (circle_id, target_id, window_kind,
					window_open_offset_seconds, window_close_offset_seconds, fixed_grace_seconds,
					note, created_by_membership_id, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 900, '', ?, ?, ?)`,
				f.CircleID, f.TargetID, tc.kind, tc.window.open, tc.window.close,
				f.MembershipID, int64(now), int64(now))

			if tc.accept {
				require.NoError(t, catalogue, "raid_target_timer: %s", tc.why)
				require.NoError(t, override, "circle_timer_override: %s", tc.why)
				return
			}
			require.Error(t, catalogue, "raid_target_timer accepted it: %s", tc.why)
			require.Error(t, override, "circle_timer_override accepted it: %s", tc.why)
		})
	}
}
