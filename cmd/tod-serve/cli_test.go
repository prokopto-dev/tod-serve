package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// migratedStore opens a migrated database in t.TempDir(). Real SQLite, because every rule these
// commands touch is a rule about rows.
func migratedStore(t *testing.T) *store.DB {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tod.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(t.Context()))
	return db
}

// bootstrappedStore is a migrated database with the instance row `serve` reads its public URL
// from. The OAuth callback has to know where to send a browser, and this binary refuses to invent
// one — see [spaJoinURL].
func bootstrappedStore(t *testing.T) *store.DB {
	t.Helper()
	db := migratedStore(t)
	_, err := db.Queries().CreateInstance(t.Context(), sqlitegen.CreateInstanceParams{
		Name: "Test Instance", PublicUrl: "https://tod.example.com", Timezone: "UTC",
		SelfServiceCircleCreation: 0, CreatedAt: 1, UpdatedAt: 1,
	})
	require.NoError(t, err)
	return db
}

func TestVersion_PrintsOnlyTheVersion(t *testing.T) {
	t.Parallel()
	out, err := captureCLI(t, "version")
	require.NoError(t, err)
	require.Equal(t, version, strings.TrimSpace(out))
}

// The banner's job is to name the verbs that work and the environment they need. An operator who
// runs the binary with no arguments must not have to read the source to find out that `serve`
// refuses to start without a pepper.
func TestNoArgs_SaysWhatWorksAndWhatItNeeds(t *testing.T) {
	t.Parallel()
	out, err := captureCLI(t)
	require.NoError(t, err)
	for _, want := range []string{
		"serve", "migrate", "init", "circle", "doctor",
		envTokenPepper, envSessionKey, "make status", "ROADMAP.md",
	} {
		require.Contains(t, out, want, "the banner does not mention %q", want)
	}
}

// The migrate verb is the whole reason goose is embedded rather than installed: an officer
// double-clicking tod-serve.exe has no migration CLI on their PATH.
func TestMigrate_AppliesTheEmbeddedMigrationsAndSaysWhereItGot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tod.db")

	out, err := captureCLI(t, "migrate", "--db", path)
	require.NoError(t, err)
	require.Contains(t, out, "schema version")

	// Running it again is a no-op rather than an error: every deploy calls this.
	_, err = captureCLI(t, "migrate", "--db", path)
	require.NoError(t, err)
}

func TestDatabasePath_Sources_PreferTheFlagThenTheEnvironment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		flag, env string
		want      string
	}{
		{"the flag wins", "flag.db", "env.db", "flag.db"},
		{"then the environment", "", "env.db", "env.db"},
		{"then the default", "", "", defaultDBPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, resolveDatabasePath(tt.flag, tt.env))
		})
	}
}

// `init` is the bootstrap and there is no other way in on a fresh database, so the failures below
// have to be loud rather than partial.
func TestInit_WhatIsRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "a circle with no server, which ADR-0009 makes permanent",
			args:    []string{"--circle", "Riot Blue"},
			wantErr: "--server",
		},
		{
			name:    "enabling local without acknowledging what it costs",
			args:    []string{"--local"},
			wantErr: "acknowledge-weak-revocation",
		},
		{
			name:    "accepting local without acknowledging what it costs",
			args:    []string{"--accept-local"},
			wantErr: "acknowledge-weak-revocation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "tod.db")
			require.NoError(t, runCLI(t, "migrate", "--db", path))

			_, err := captureCLI(t, append([]string{"init", "--db", path}, tt.args...)...)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// A second `init` is refused rather than silently rewriting the singleton, because the thing it
// would rewrite is the instance every existing member's circle belongs to.
func TestInit_Twice_IsRefused(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tod.db")
	require.NoError(t, runCLI(t, "migrate", "--db", path))
	require.NoError(t, runCLI(t, "init", "--db", path, "--name", "Test Instance"))

	err := runCLI(t, "init", "--db", path, "--name", "Something Else")
	require.Error(t, err)
	require.Contains(t, err.Error(), "already initialised")
}

// `init` with no circle says what to run next. An operator left on a database with an instance row
// and no way in should be told, not left to find `make status`.
func TestInit_WithNoCircle_SaysWhatToRunNext(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tod.db")
	require.NoError(t, runCLI(t, "migrate", "--db", path))

	out, err := captureCLI(t, "init", "--db", path, "--name", "Test Instance")
	require.NoError(t, err)
	require.Contains(t, out, "tod-serve circle create")
	require.NotRegexp(t, codePattern, out, "there is no circle yet, so there is no owner code")
}

// Every command refuses to run against a database the migrations have not reached, and says which
// command fixes it. A verb that half-worked against an old schema is how a partial upgrade is
// discovered by a user rather than by the operator.
func TestVerbs_AgainstAnUnmigratedDatabase_SayToMigrateFirst(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"init", "--name", "Test Instance"},
		{"circle", "create", "--name", "Riot Blue", "--server", "blue"},
	} {
		path := filepath.Join(t.TempDir(), "tod.db")
		_, err := captureCLI(t, append(args, "--db", path)...)
		require.Error(t, err, "%v ran against an unmigrated database", args)
		require.Contains(t, err.Error(), "tod-serve migrate")
	}
}

func TestCircleCreate_WhatIsRequired(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no name", []string{"--server", "blue"}, "--name"},
		{"no server", []string{"--name", "Riot Blue"}, "--server"},
		{
			name:    "a server outside the enum",
			args:    []string{"--name", "Riot Blue", "--server", "purple"},
			wantErr: "server",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "tod.db")
			require.NoError(t, runCLI(t, "migrate", "--db", path))
			require.NoError(t, runCLI(t, "init", "--db", path, "--name", "Test Instance"))

			_, err := captureCLI(t,
				append([]string{"circle", "create", "--db", path}, tt.args...)...)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// `doctor` is a diagnostic for the failures that stop the server starting, so it must be useful on
// a database that has nothing on it — a report that needed the thing it diagnoses would be a
// report for the wrong problem.
func TestDoctor_OnAFreshDatabase_NamesTheMissingPieces(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tod.db")
	require.NoError(t, runCLI(t, "migrate", "--db", path))

	out, err := captureCLI(t, "doctor", "--db", path)
	require.Error(t, err, "a database with no instance row is not healthy")
	require.Contains(t, out, "tod-serve init")
	require.Contains(t, out, "no identity provider is enabled")
	// The third thing a fresh database is missing, and the one nothing said before: no identity
	// holds `instance.security.manage`, so nobody can administer it over the API. It is named
	// here as well as in the end-to-end walk because `.github/workflows/deploy.yml` subtracts
	// exactly these three strings on a first installation and fails red on a fourth.
	require.Contains(t, out, "nobody can administer this instance")
	require.Contains(t, out, "integrity check passed")
	require.Contains(t, out, "foreign key check passed")
}

// And on a bootstrapped one it says the instance is fine AND keeps saying that `local` revocation
// is advisory. That warning is repeated on every run deliberately: the damage is the officers'
// belief that revocation worked, and a message shown once at setup does not reach them.
func TestDoctor_OnABootstrappedInstance_IsHealthyAndStillWarnsAboutLocal(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tod.db")
	require.NoError(t, runCLI(t, "migrate", "--db", path))
	require.NoError(t, runCLI(t, "init", "--db", path,
		"--name", "Test Instance", "--public-url", "https://tod.example.com",
		"--circle", "Riot Blue", "--server", "blue",
		"--local", "--accept-local", "--acknowledge-weak-revocation"))
	// `init` is not the end of the bootstrap. It prints the grant as the last step and this is
	// that step: without it the instance has no administrator, which is a PROBLEM of its own and
	// would make "no problems found" below assert nothing about `local` or the public URL.
	finishBootstrap(t, path)

	out, err := captureCLI(t, "doctor", "--db", path)
	require.NoError(t, err)
	require.Contains(t, out, "no problems found")
	require.Contains(t, out, "ADVISORY")
	require.Contains(t, out, "https://tod.example.com")
}

// A public URL is a warning rather than a problem: everything works without one except the OAuth
// redirect, and an instance running only `local` never needs one.
func TestDoctor_WithNoPublicURL_WarnsWithoutFailing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tod.db")
	require.NoError(t, runCLI(t, "migrate", "--db", path))
	require.NoError(t, runCLI(t, "init", "--db", path, "--name", "Test Instance",
		"--circle", "Riot Blue", "--server", "blue",
		"--local", "--accept-local", "--acknowledge-weak-revocation"))
	finishBootstrap(t, path)

	out, err := captureCLI(t, "doctor", "--db", path)
	require.NoError(t, err)
	require.Contains(t, out, "no public URL")
	require.Contains(t, out, "no problems found")
}
