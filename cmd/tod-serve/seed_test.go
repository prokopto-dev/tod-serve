package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
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
