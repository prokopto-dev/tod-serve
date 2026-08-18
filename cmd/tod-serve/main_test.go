package main

import (
	"bytes"
	"errors"
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

// The banner is the only thing this binary does, and its job is to stop someone concluding the
// repository is broken when it is merely unimplemented. If it stops saying so, that has failed.
func TestRun_NoArgs_SaysThereIsNoWorkingSoftware(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := run(nil, &buf); err != nil {
		t.Fatalf("run(nil): %v", err)
	}
	for _, want := range []string{"design phase", "no working software", "make status", "ROADMAP.md"} {
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
