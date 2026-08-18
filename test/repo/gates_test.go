// Package repo holds tests about the repository itself rather than about the product. They assert
// that the gates named in docs/concepts/invariants.md actually fire.
package repo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/repogate"
)

// scanned returns the directories every source-level gate walks: everything this module compiles.
func scanned() []string { return []string{"cmd", "internal", "test"} }

// CLOCK001. The grep in scripts/repo-gates.sh runs in the CI job that has no Go toolchain and
// catches the common case; this is the one the canonical conventions describe, and it is the one
// an aliased import does not defeat.
func TestCLOCK001_Repository_HasNoTimeNowOutsideClock(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	got, err := repogate.Check(root, scanned(), []repogate.Rule{repogate.ClockRule()})
	require.NoError(t, err)

	for _, f := range got.Findings {
		t.Errorf("%s: %s", f, repogate.ClockRule().Reason)
	}
}

// A gate reporting success over an empty search space is exactly what this repository is built
// against, so the gate above is asked how much it looked at.
func TestCLOCK001_Analyser_ActuallyReadsTheTree(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	got, err := repogate.Check(root, scanned(), []repogate.Rule{repogate.ClockRule()})
	require.NoError(t, err)
	require.Greater(t, got.Files, 10, "the analyser found almost no Go files; the roots are wrong")
}

// SLEEP001. The grep in scripts/repo-gates.sh has the same blind spot the CLOCK001 grep has, and
// this is the analyser that does not.
func TestSLEEP001_Tests_DoNotSleep(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	got, err := repogate.Check(root, scanned(), []repogate.Rule{repogate.SleepRule()})
	require.NoError(t, err)
	require.Positive(t, got.Files)

	for _, f := range got.Findings {
		t.Errorf("%s: %s", f, repogate.SleepRule().Reason)
	}
}
