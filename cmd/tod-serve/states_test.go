package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// TestStates_OnAFreshInstance_RunAndSayNothingIsWrong.
//
// Both verbs are safe to run against anything, which is the point of a cache that is never
// authority: `DELETE FROM target_state_cache` followed by `rebuild-states` is a supported thing to
// do to a production database.
func TestStates_OnAFreshInstance_RunAndSayNothingIsWrong(t *testing.T) {
	t.Parallel()
	db := migratedStore(t)

	out, err := captureCLI(t, "rebuild-states", "--db", db.Path())
	require.NoError(t, err)
	require.Contains(t, out, "rebuilt 0 target states")

	out, err = captureCLI(t, "verify-states", "--db", db.Path())
	require.NoError(t, err, "a healthy instance exits zero")
	require.Contains(t, out, "checked 0 targets across 0 circles")
}

// TestVerifyStates_WhenItRepairsSomething_ExitsNonZero is what makes the nightly job an ALERT
// rather than a log line: a cron entry that mails on failure, a systemd timer that goes degraded
// and a CI job all notice a non-zero exit, and none of them notice an ERROR in a log nobody tails.
//
// The repair has already happened by the time the status is set — the recomputation always wins —
// so the exit code says "something drifted, find out why", not "the board is broken".
func TestVerifyStates_WhenItRepairsSomething_ExitsNonZero(t *testing.T) {
	t.Parallel()
	db := seededCircleWithACorruptCacheRow(t)

	out, err := captureCLI(t, "verify-states", "--db", db.Path())
	require.Error(t, err, "a run that repaired something must not exit zero")
	require.Contains(t, err.Error(), "1 states repaired")
	require.Contains(t, out, "drift: ")
	require.Contains(t, out, "status", "the summary names the field that disagreed")
	require.Contains(t, out, projection.AlertMessage, "and the alert reaches stderr")

	// The row on disk is the recomputation's, and a second run is clean.
	out, err = captureCLI(t, "verify-states", "--db", db.Path())
	require.NoError(t, err)
	require.NotContains(t, out, "drift: ")
}

// TestStates_AgainstAnUnmigratedDatabase_SayToMigrateFirst. A maintenance verb pointed at a fresh
// file must say what to run, not fail with a missing-table error.
func TestStates_AgainstAnUnmigratedDatabase_SayToMigrateFirst(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{verbRebuildStates, verbVerifyStates} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			path := t.TempDir() + "/tod.db"
			_, err := captureCLI(t, verb, "--db", path)
			require.Error(t, err)
			require.Contains(t, err.Error(), "tod-serve migrate")
		})
	}
}

// seededCircleWithACorruptCacheRow builds the smallest database that can drift: one circle, one
// target, one report, and a cached row claiming something the log does not say.
func seededCircleWithACorruptCacheRow(t *testing.T) *store.DB {
	t.Helper()
	db := migratedStore(t)
	ctx := t.Context()
	q := db.Queries()

	const at = int64(1_755_483_247_000_000)
	circleID := "01K3TGT8N9M4X0Q7R2VB6C5D1E"
	targetID := "01K3TGT8N9M4X0Q7R2VB6C5D2F"
	memberID := "01K3TGT8N9M4X0Q7R2VB6C5D3G"
	reportID := "01K3TGT8N9M4X0Q7R2VB6C5D4H"
	providerID := "01K3TGT8N9M4X0Q7R2VB6C5D5J"
	identityID := "01K3TGT8N9M4X0Q7R2VB6C5D6K"

	_, err := q.CreateCircle(ctx, sqlitegen.CreateCircleParams{
		CircleID: circleID, Name: "Riot", NameNorm: "riot", Server: schemaenum.ServerBlue,
		Timezone: "UTC", MinReportersToSupersede: 1, RevokeInvalidatesInvites: 1,
		State: schemaenum.CircleStateActive, CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	_, err = q.CreateIdentityProvider(ctx, sqlitegen.CreateIdentityProviderParams{
		ID: providerID, Key: "local", Kind: schemaenum.IdentityProviderKindLocal,
		DisplayName: "Local", Enabled: 1, VerifiableSubject: 0, CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	_, err = q.CreateIdentity(ctx, sqlitegen.CreateIdentityParams{
		ID: identityID, ProviderID: providerID, Subject: identityID,
		DisplayName: "Tankguy", CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	_, err = q.CreateMembership(ctx, sqlitegen.CreateMembershipParams{
		ID: memberID, CircleID: circleID, IdentityID: &identityID,
		Kind: schemaenum.MembershipKindHuman, DisplayName: "Tankguy", DisplayNameNorm: "tankguy",
		Role: schemaenum.MembershipRoleMember, JoinedAt: at, CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	_, err = q.CreateRaidTarget(ctx, sqlitegen.CreateRaidTargetParams{
		ID: targetID, Name: "Vulak`Aerr", NameNorm: "vulakaerr",
		Zone: "Temple of Veeshan", ZoneNorm: "templeofveeshan",
		Expansion: schemaenum.RaidTargetExpansionVelious,
		Category:  schemaenum.RaidTargetCategoryNToV,
		State:     schemaenum.RaidTargetStateActive, CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	_, err = q.CreateTodReport(ctx, sqlitegen.CreateTodReportParams{
		ID: reportID, CircleID: circleID, TargetID: targetID,
		Kind: schemaenum.TodReportKindKill, DiedAt: at - 3_600_000_000, ReportedAt: at,
		ReporterMembershipID: memberID, Source: schemaenum.TodReportSourceLogLine,
		SelfConfidence: schemaenum.TodReportSelfConfidenceCertain,
	})
	require.NoError(t, err)

	// The drift: a cached row saying the target is in its window, on an instance with no timer at
	// all — which is a board a raid leader would act on.
	_, err = q.PutTargetState(ctx, sqlitegen.PutTargetStateParams{
		CircleID: circleID, TargetID: targetID, ComputedAt: at,
		Status:      schemaenum.TargetStateStatusInWindow,
		Confidence:  schemaenum.TargetStateConfidenceHigh,
		ReportCount: 9, CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	return db
}
