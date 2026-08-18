package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_VersionFlag_PrintsOnlyTheVersion(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{cmdVersion, "--" + cmdVersion} {
		var buf bytes.Buffer
		if err := run([]string{arg}, &buf); err != nil {
			t.Fatalf("run(%q): %v", arg, err)
		}
		if got := strings.TrimSpace(buf.String()); got != version {
			t.Errorf("run(%q) = %q, want %q", arg, got, version)
		}
	}
}

// The banner's job is to stop someone concluding the repository is broken when it is merely
// unimplemented, and to name the verbs that DO work. If it stops doing either, that has failed.
func TestRun_NoArgs_SaysWhatWorksAndWhatDoesNot(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := run(nil, &buf); err != nil {
		t.Fatalf("run(nil): %v", err)
	}
	for _, want := range []string{"design phase", "no HTTP server", cmdMigrate, "make status", "ROADMAP.md"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("banner does not mention %q; got:\n%s", want, buf.String())
		}
	}
}

// errWriter fails every write, standing in for a closed stdout.
type errWriter struct{}

var errClosed = errors.New("closed")

func (errWriter) Write([]byte) (int, error) { return 0, errClosed }

// A binary that exits 0 having written nothing is the kind of thing a script trusts.
func TestRun_WriteFails_ReturnsTheError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"banner", nil},
		{cmdVersion, []string{cmdVersion}},
	} {
		if err := run(tc.args, errWriter{}); !errors.Is(err, errClosed) {
			t.Errorf("run(%s) error = %v, want it to wrap errClosed", tc.name, err)
		}
	}
}

// The migrate verb is the whole reason goose is embedded rather than installed: an officer
// double-clicking tod-serve.exe has no migration CLI on their PATH. It is exercised against a real
// file, because a migration that only works against :memory: is one nobody finds out about.
func TestRun_Migrate_AppliesTheEmbeddedMigrationsAndSaysWhereItGot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tod.db")

	var buf bytes.Buffer
	if err := run([]string{cmdMigrate, "--db", path}, &buf); err != nil {
		t.Fatalf("run(migrate): %v", err)
	}
	if !strings.Contains(buf.String(), "schema version") {
		t.Errorf("migrate said nothing about the version it reached; got:\n%s", buf.String())
	}

	// Running it again is a no-op rather than an error: every boot calls this.
	buf.Reset()
	if err := run([]string{cmdMigrate, "--db", path}, &buf); err != nil {
		t.Fatalf("run(migrate) second time: %v", err)
	}
}

func TestDatabasePath_Sources_PreferTheFlagThenTheEnvironment(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
		env  string
		want string
	}{
		{"the flag wins", []string{"--db", "flag.db"}, "env.db", "flag.db"},
		{"then the environment", nil, "env.db", "env.db"},
		{"then the default", nil, "", defaultDBPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := databasePath(tc.args, tc.env)
			if err != nil {
				t.Fatalf("databasePath(%v): %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("databasePath(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// An unknown argument is an error rather than a silently ignored one: `--database /srv/tod.db`
// otherwise migrates ./tod.db and reports success.
func TestDatabasePath_UnknownArgument_IsAnError(t *testing.T) {
	t.Parallel()
	if _, err := databasePath([]string{"--database", "x.db"}, ""); err == nil {
		t.Error("an unknown argument was accepted")
	}
	if _, err := databasePath([]string{"--db"}, ""); err == nil {
		t.Error("--db with no path was accepted")
	}
}
