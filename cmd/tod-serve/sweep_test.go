package main

import (
	"testing"

	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/stretchr/testify/require"
)

// longExpired is a fixed instant in 2020. It is a constant rather than an offset from the clock
// because this verb runs on `clock.System`, and anything before yesterday is past the grace period
// on every day this test will ever run.
const longExpired = int64(1_577_836_800_000_000)

// TestSweep_OnAFreshInstance_ExitsZeroAndSaysItRemovedNothing. A maintenance verb pointed at an
// empty database is a no-op that reports, not an error.
func TestSweep_OnAFreshInstance_ExitsZeroAndSaysItRemovedNothing(t *testing.T) {
	t.Parallel()
	db := migratedStore(t)

	out, err := captureCLI(t, verbSweep, "--db", db.Path())
	require.NoError(t, err)
	require.Contains(t, out, "swept 0 expired rows")
}

// TestSweep_WhenItRemovesRows_StillExitsZero is the deliberate opposite of
// [TestVerifyStates_WhenItRepairsSomething_ExitsNonZero], and the reason the sweep is its own verb.
//
// `verify-states` exits non-zero when it repaired something because a repair means the cache
// drifted and somebody must look. Removing expired litter is the routine healthy case: a timer that
// went degraded every night because the sweep did its job is a timer somebody switches off, which
// is the cry-wolf failure this repository designs against. Sharing a command would have forced one
// exit-code contract onto both.
func TestSweep_WhenItRemovesRows_StillExitsZero(t *testing.T) {
	t.Parallel()
	db := seededWithExpiredLitter(t)

	out, err := captureCLI(t, verbSweep, "--db", db.Path())
	require.NoError(t, err, "removing expired rows is the healthy case and must not alert")
	require.Contains(t, out, "swept 2 expired rows")
	require.Contains(t, out, "1 auth flows")
	require.Contains(t, out, "1 credential tickets")

	// The rows are gone, and a second run finds nothing left to do.
	out, err = captureCLI(t, verbSweep, "--db", db.Path())
	require.NoError(t, err)
	require.Contains(t, out, "swept 0 expired rows")
}

// TestSweep_AgainstAnUnmigratedDatabase_SaysToMigrateFirst. Pointed at a fresh file it must say
// what to run, not fail with a missing-table error.
func TestSweep_AgainstAnUnmigratedDatabase_SaysToMigrateFirst(t *testing.T) {
	t.Parallel()
	path := t.TempDir() + "/tod.db"
	_, err := captureCLI(t, verbSweep, "--db", path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tod-serve migrate")
}

// seededWithExpiredLitter builds the smallest database that has something to sweep: a provider, and
// one long-expired row in each of the two tables that hang off it.
func seededWithExpiredLitter(t *testing.T) *store.DB {
	t.Helper()
	db := migratedStore(t)
	ctx := t.Context()
	q := db.Queries()

	const providerID = "01K3TGT8N9M4X0Q7R2VB6C5D3G"
	_, err := q.CreateIdentityProvider(ctx, sqlitegen.CreateIdentityProviderParams{
		ID: providerID, Key: "local", Kind: "local", DisplayName: "This server",
		Enabled: 1, VerifiableSubject: 0, CreatedAt: longExpired, UpdatedAt: longExpired,
	})
	require.NoError(t, err)

	_, err = q.CreateAuthFlow(ctx, sqlitegen.CreateAuthFlowParams{
		ID: "01K3TGT8N9M4X0Q7R2VB6C5D4H", State: "stale-state", PkceVerifier: "verifier",
		ProviderID: providerID, ExpiresAt: longExpired,
		CreatedAt: longExpired, UpdatedAt: longExpired,
	})
	require.NoError(t, err)

	// created_at is derived from the expiry, not chosen: `ck_credential_ticket_ttl` pins the
	// 120-second lifetime, so a row with any other one cannot be written at all.
	_, err = q.CreateCredentialTicket(ctx, sqlitegen.CreateCredentialTicketParams{
		ID: "01K3TGT8N9M4X0Q7R2VB6C5D5J", TicketHash: []byte("stale-ticket"),
		ProviderID: providerID, Subject: "subject", DisplayName: "Tankguy",
		GuildRolesJson: "[]", ExpiresAt: longExpired,
		CreatedAt: longExpired - 120*1_000_000, UpdatedAt: longExpired,
	})
	require.NoError(t, err)
	return db
}
