package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureRun drives run() against a real *os.File, because run takes one rather than an io.Writer.
// A temp file is closer to what main() actually passes than a bytes.Buffer would be.
func captureRun(t *testing.T, args []string) string {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatalf("create temp output: %v", err)
	}
	defer func() { _ = f.Close() }()

	if err := run(args, f); err != nil {
		t.Fatalf("run(%v): %v", args, err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read back output: %v", err)
	}
	return string(b)
}

func TestRun_VersionFlag_PrintsOnlyTheVersion(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"version", "--version"} {
		got := captureRun(t, []string{arg})
		if strings.TrimSpace(got) != version {
			t.Errorf("run(%q) = %q, want %q", arg, strings.TrimSpace(got), version)
		}
	}
}

// The banner is the only thing this binary does, and its job is to stop someone concluding the
// repository is broken when it is merely unimplemented. If it stops saying so, that has failed.
func TestRun_NoArgs_SaysThereIsNoWorkingSoftware(t *testing.T) {
	t.Parallel()
	got := captureRun(t, nil)
	for _, want := range []string{"design phase", "no working software", "make status", "ROADMAP.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner does not mention %q; got:\n%s", want, got)
		}
	}
}
