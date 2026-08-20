package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// The timer numbers in this file are invented and obviously so. They are not P99 data and must
// never be replaced with any: canonical §15 keeps those out of this repository, SEED001 is the
// grep, and a test fixture is the first place somebody copies a value from.
const testSeedSource = "a CLI test, not the wiki"

// writeSeed puts a seed file in t.TempDir() and returns its path.
//
// It is written at run time rather than checked in, deliberately. A fixture file living in this
// repository is a file somebody fills with real numbers, and SEED001 exists because that is the
// obvious next step for a well-meaning contributor.
func writeSeed(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timers.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// TestSeedTargets_LoadsTheEmbeddedIdentityAndSaysTimersAreMissing.
//
// The second half is the part that matters. An operator who runs this, opens the board and sees
// `no_timer` on every row needs the next step in front of them at the moment they ran the command
// — not in a document they have not opened.
func TestSeedTargets_LoadsTheEmbeddedIdentityAndSaysTimersAreMissing(t *testing.T) {
	t.Parallel()
	db := migratedStore(t)

	out, err := captureCLI(t, "seed", "targets", "--db", db.Path())
	require.NoError(t, err)
	require.Contains(t, out, "targets:")
	require.Contains(t, out, "aliases:")
	require.Contains(t, out, "timers are NOT bundled",
		"the operator is left to discover an empty board on their own")
	require.Contains(t, out, "tod-serve seed timers --file")
	require.Contains(t, out, "no_timer")

	rows, err := db.Queries().ListAllRaidTargets(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, len(catalogue.Embedded()))

	// And nothing wrote a timer, which is the licence invariant seen from the outside.
	for _, server := range []string{"blue", "green", "red"} {
		timers, listErr := db.Queries().ListRaidTargetTimersForServer(t.Context(), server)
		require.NoError(t, listErr)
		require.Empty(t, timers, "seeding identity wrote a timer for %s", server)
	}
}

// TestSeedTargets_RunTwice_IsSafeAndSaysSo. A seed an operator dares not re-run stops being applied
// after the first install, which is when it matters least.
func TestSeedTargets_RunTwice_IsSafeAndSaysSo(t *testing.T) {
	t.Parallel()
	db := migratedStore(t)

	_, err := captureCLI(t, "seed", "targets", "--db", db.Path())
	require.NoError(t, err)
	out, err := captureCLI(t, "seed", "targets", "--db", db.Path())
	require.NoError(t, err)
	require.Contains(t, out, "0 added")

	rows, err := db.Queries().ListAllRaidTargets(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, len(catalogue.Embedded()), "the second run duplicated rows")
}

// TestSeedTimers_WithNoFile_IsRefused. There is no bundled file for the flag to fall back on and no
// default path, because a default path is an invitation to put one in this repository.
func TestSeedTimers_WithNoFile_IsRefused(t *testing.T) {
	t.Parallel()
	db := migratedStore(t)

	_, err := captureCLI(t, "seed", "timers", "--db", db.Path())
	require.Error(t, err)
	require.Contains(t, err.Error(), "file")
}

// TestSeedTimers_AWellFormedFile_WritesTheWindowsAndNamesTheSource.
func TestSeedTimers_AWellFormedFile_WritesTheWindowsAndNamesTheSource(t *testing.T) {
	t.Parallel()
	db := migratedStore(t)
	_, err := captureCLI(t, "seed", "targets", "--db", db.Path())
	require.NoError(t, err)

	path := writeSeed(t, `{
	  "version": 1,
	  "source": "`+testSeedSource+`",
	  "timers": [
	    {"target": "Naggy", "server": "blue", "window_kind": "variance",
	     "window_open_offset_seconds": 60, "window_close_offset_seconds": 120},
	    {"target": "Vox", "server": "blue", "window_kind": "fixed",
	     "window_open_offset_seconds": 300, "window_close_offset_seconds": 300}
	  ]
	}`)

	out, err := captureCLI(t, "seed", "timers", "--db", db.Path(), "--file", path)
	require.NoError(t, err)
	require.Contains(t, out, "2 timers written")
	require.Contains(t, out, testSeedSource,
		"the run does not say which seed it applied, so an operator has only the filename")

	timers, err := db.Queries().ListRaidTargetTimersForServer(t.Context(), "blue")
	require.NoError(t, err)
	require.Len(t, timers, 2)
}

// TestSeedTimers_Check_ValidatesAndWritesNothing. An operator handed a seed file by somebody else
// needs a way to look at it that cannot change their instance.
func TestSeedTimers_Check_ValidatesAndWritesNothing(t *testing.T) {
	t.Parallel()
	db := migratedStore(t)
	_, err := captureCLI(t, "seed", "targets", "--db", db.Path())
	require.NoError(t, err)

	path := writeSeed(t, `{"version": 1, "source": "`+testSeedSource+`", "timers": [
	  {"target": "Naggy", "server": "blue", "window_kind": "unknown"}]}`)

	out, err := captureCLI(t, "seed", "timers", "--db", db.Path(), "--file", path, "--check")
	require.NoError(t, err)
	require.Contains(t, out, "Nothing was written")

	timers, err := db.Queries().ListRaidTargetTimersForServer(t.Context(), "blue")
	require.NoError(t, err)
	require.Empty(t, timers, "--check wrote to the database")
}

// TestSeedTimers_ABadFile_FailsAndLeavesTheCatalogueUnchanged is the acceptance criterion, seen
// from the command line: a malformed or partial seed must fail loudly and change nothing.
//
// The last case is the one a parse check cannot catch — every row is individually well formed, and
// the file fails against THIS instance's catalogue on the last one. A write-as-you-go loop would
// leave two windows behind that the operator did not choose and cannot attribute to a run.
func TestSeedTimers_ABadFile_FailsAndLeavesTheCatalogueUnchanged(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "not JSON", body: "Naggy respawns in 7 days"},
		{
			name: "no version",
			body: `{"source": "x", "timers": [{"target": "Naggy", "server": "blue",
			        "window_kind": "unknown"}]}`,
		},
		{
			name: "no source",
			body: `{"version": 1, "timers": [{"target": "Naggy", "server": "blue",
			        "window_kind": "unknown"}]}`,
		},
		{
			name: "a window the schema would refuse",
			body: `{"version": 1, "source": "x", "timers": [{"target": "Naggy", "server": "blue",
			        "window_kind": "fixed", "window_open_offset_seconds": 60,
			        "window_close_offset_seconds": 120}]}`,
		},
		{
			name: "two good rows and then a target this instance has never heard of",
			body: `{"version": 1, "source": "x", "timers": [
			  {"target": "Naggy", "server": "blue", "window_kind": "unknown"},
			  {"target": "Vox", "server": "blue", "window_kind": "unknown"},
			  {"target": "Emperor Ssraeshza", "server": "blue", "window_kind": "unknown"}]}`,
		},
		{
			name: "two good rows and then an ambiguous name",
			body: `{"version": 1, "source": "x", "timers": [
			  {"target": "Naggy", "server": "blue", "window_kind": "unknown"},
			  {"target": "Vox", "server": "blue", "window_kind": "unknown"},
			  {"target": "Lord", "server": "blue", "window_kind": "unknown"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := migratedStore(t)
			_, err := captureCLI(t, "seed", "targets", "--db", db.Path())
			require.NoError(t, err)

			_, err = captureCLI(t, "seed", "timers", "--db", db.Path(),
				"--file", writeSeed(t, tt.body))
			require.Error(t, err, "a bad seed was accepted")

			timers, listErr := db.Queries().ListRaidTargetTimersForServer(t.Context(), "blue")
			require.NoError(t, listErr)
			require.Empty(t, timers,
				"the seed failed and left %d timers behind; it is half-applied", len(timers))
		})
	}
}

// TestSeedTimers_AMissingFile_SaysWhichOne. An operator who mistyped a path gets the path back.
func TestSeedTimers_AMissingFile_SaysWhichOne(t *testing.T) {
	t.Parallel()
	db := migratedStore(t)
	missing := filepath.Join(t.TempDir(), "nope.json")

	_, err := captureCLI(t, "seed", "timers", "--db", db.Path(), "--file", missing)
	require.Error(t, err)
	require.Contains(t, err.Error(), missing)
}

// TestSeed_WithNoVerb_SaysWhichHalfShipsAndWhichDoesNot. The two verbs exist because the licence
// boundary does, and the help is where an operator meets that fact.
func TestSeed_WithNoVerb_SaysWhichHalfShipsAndWhichDoesNot(t *testing.T) {
	t.Parallel()
	out, err := captureCLI(t, "seed")
	require.NoError(t, err)
	require.Contains(t, out, "targets")
	require.Contains(t, out, "timers")
	require.True(t,
		strings.Contains(out, "do NOT") || strings.Contains(out, "tod-serve-p99-seed"),
		"the help does not say that timer data ships from somewhere else: %s", out)
}

// TestSeedTimers_RecomputesEveryBoardTheWindowsMoved is the invalidation gate for the one write
// path that has no route.
//
// `seed timers --file` writes `raid_target_timer`, which is instance-wide and per-server, so a
// single run moves the window for every circle pinned to that server. It sits OUTSIDE the route
// registry, so the architectural gate that holds every timer-writing ROUTE to pushing an
// invalidation cannot see this command — and the failure it would miss is the largest one of its
// kind: an operator seeds sixty windows and every board on that server goes on serving the old
// ones until the nightly job catches it, up to twenty-four hours later.
//
// So the assertion is about the derived state, not about the timer row: the board must show the
// seeded window on the very next read.
func TestSeedTimers_RecomputesEveryBoardTheWindowsMoved(t *testing.T) {
	t.Parallel()
	db := migratedStore(t)
	_, err := captureCLI(t, "seed", "targets", "--db", db.Path())
	require.NoError(t, err)

	circleID, targets, died := seedACircleWithAToD(t, db, "Naggy", "Vox")
	targetID := targets[0]

	// Before the seed there is no timer anywhere, so the cached board says `no_timer`.
	before, err := db.Queries().GetTargetState(t.Context(), sqlitegen.GetTargetStateParams{
		CircleID: circleID, TargetID: targetID,
	})
	require.NoError(t, err)
	require.Equal(t, schemaenum.TargetStateStatusNoTimer, before.Status)
	require.Nil(t, before.WindowOpenAt)

	const openOffsetSeconds = 4 * 60 * 60
	path := writeSeed(t, `{
	  "version": 1,
	  "source": "`+testSeedSource+`",
	  "timers": [
	    {"target": "Naggy", "server": "blue", "window_kind": "variance",
	     "window_open_offset_seconds": 14400, "window_close_offset_seconds": 28800},
	    {"target": "Vox", "server": "blue", "window_kind": "variance",
	     "window_open_offset_seconds": 14400, "window_close_offset_seconds": 28800}
	  ]
	}`)

	out, err := captureCLI(t, "seed", "timers", "--db", db.Path(), "--file", path)
	require.NoError(t, err)
	require.Contains(t, out, "2 timers written")
	// EVERY window this run moved, not merely one of them. The invariant the timer routes hold is
	// "after a non-5xx answer the projection has been told", and the zero exit above is this
	// command's version of that answer — so a run that recomputed 1 of 2 must not reach it.
	require.Contains(t, out, "2 of 2 moved windows recomputed",
		"a seed that wrote windows and invalidated nothing leaves every board on that server stale")

	for _, id := range targets {
		row, stateErr := db.Queries().GetTargetState(t.Context(), sqlitegen.GetTargetStateParams{
			CircleID: circleID, TargetID: id,
		})
		require.NoError(t, stateErr)
		require.NotEqual(t, schemaenum.TargetStateStatusNoTimer, row.Status,
			"every board the seed moved was recomputed, not just the first")
	}

	after, err := db.Queries().GetTargetState(t.Context(), sqlitegen.GetTargetStateParams{
		CircleID: circleID, TargetID: targetID,
	})
	require.NoError(t, err)
	require.NotEqual(t, schemaenum.TargetStateStatusNoTimer, after.Status,
		"the board picked up the seeded window without waiting for the nightly job")
	// Which window status it lands in depends on the wall clock — this verb runs on the system
	// clock, deliberately, because it is a real command an operator runs. The bound below is the
	// assertion that matters and it is exact.
	require.NotNil(t, after.WindowOpenAt)
	require.Equal(t, died+openOffsetSeconds*int64(core.MicrosPerSecond), *after.WindowOpenAt)
	require.NotNil(t, after.ChangeReason)
	require.Equal(t, schemaenum.TargetStateChangeReasonTimerChange, *after.ChangeReason,
		"and says why, because nothing was reported")
}

// seedACircleWithAToD builds one circle with one member and a kill report about each of
// `targetNames`, then derives its board once so there are cached rows for the seed to move. It
// returns the circle id, the target ids and the `died_at` the windows are measured from.
func seedACircleWithAToD(
	t *testing.T, db *store.DB, targetNames ...string,
) (string, []string, int64) {
	t.Helper()
	ctx := t.Context()
	q := db.Queries()
	const at = int64(1_755_483_247_000_000)
	died := at - int64(time.Hour/time.Microsecond)

	ids := []string{
		"01K3TGT8N9M4X0Q7R2VB6C5E1E", "01K3TGT8N9M4X0Q7R2VB6C5E2F",
		"01K3TGT8N9M4X0Q7R2VB6C5E3G", "01K3TGT8N9M4X0Q7R2VB6C5E4H",
		"01K3TGT8N9M4X0Q7R2VB6C5E5J",
	}
	circleID, memberID, reportID, providerID, identityID := ids[0], ids[1], ids[2], ids[3], ids[4]

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

	targetIDs := make([]string, 0, len(targetNames))
	for i, name := range targetNames {
		target, lookupErr := q.GetRaidTargetByAliasNorm(ctx, core.Normalise(name))
		require.NoError(t, lookupErr)
		// The report ids differ only in their last character, which is enough for a fixture and
		// keeps them readable beside each other in a failure.
		_, err = q.CreateTodReport(ctx, sqlitegen.CreateTodReportParams{
			ID:       reportID[:len(reportID)-1] + string(rune('A'+i)),
			CircleID: circleID, TargetID: target.ID,
			Kind: schemaenum.TodReportKindKill, DiedAt: died, ReportedAt: at,
			ReporterMembershipID: memberID, Source: schemaenum.TodReportSourceLogLine,
			SelfConfidence: schemaenum.TodReportSelfConfidenceCertain,
		})
		require.NoError(t, err)
		targetIDs = append(targetIDs, target.ID)
	}

	// Derive once, so the seed has cached rows to move rather than an empty table that would make
	// this test pass for the wrong reason.
	_, err = captureCLI(t, "rebuild-states", "--db", db.Path())
	require.NoError(t, err)
	return circleID, targetIDs, died
}
