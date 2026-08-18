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

// ROUTE001. AGENTS.md law 1 says HTTP routes are declared only in internal/api; this is the gate,
// and it is stricter than the law — within that package exactly one file may register a route, and
// it takes an operation id from the registry rather than a method and a path.
func TestROUTE001_Routes_AreDeclaredOnlyThroughTheRegistry(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	got, err := repogate.Check(root, scanned(), []repogate.Rule{repogate.RouteRule()})
	require.NoError(t, err)
	require.Positive(t, got.Files)

	for _, f := range got.Findings {
		t.Errorf("%s: %s", f, repogate.RouteRule().Reason)
	}
}

// The gate above is only worth anything if it fires. Both shapes are checked: the obvious call,
// and the aliased import that defeats a grep.
func TestROUTE001_AHandlerRegisteredOutsideTheRegistry_IsReported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "a plain call",
			src: `package circle

import "github.com/danielgtaylor/huma/v2"

func wire(api huma.API) {
	huma.Register(api, huma.Operation{Method: "GET", Path: "/circles"}, nil)
}
`,
		},
		{
			name: "an aliased import, which a grep does not see",
			src: `package circle

import h "github.com/danielgtaylor/huma/v2"

func wire(api h.API) {
	h.Get(api, "/circles", nil)
}
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			found, err := repogate.CheckSource(repogate.RouteRule(), "internal/circle/wire.go", tc.src)
			require.NoError(t, err)
			require.Len(t, found, 1, "ROUTE001 did not fire on %s", tc.name)
			require.Equal(t, "ROUTE001", found[0].Rule)
		})
	}
}

// The allowance is one FILE, not the package. internal/api is full of files that must not be able
// to register a route either — a handler file that called the framework directly would bypass the
// registry just as completely as one in another package.
func TestROUTE001_TheAllowance_IsOneFileNotThePackage(t *testing.T) {
	t.Parallel()
	rule := repogate.RouteRule()

	require.True(t, rule.Allows("internal/api/register.go"))
	require.False(t, rule.Allows("internal/api/handlers.go"))
	require.False(t, rule.Allows("internal/api/register_test.go"))
	require.False(t, rule.Allows("internal/circle/wire.go"))
}

// The analyser has to know that an unaliased `github.com/danielgtaylor/huma/v2` binds `huma` and
// not `v2`. Reading the last path segment would make ROUTE001 search for a package nobody names —
// a gate reporting success over an empty search space, which is what test/repo exists to catch.
func TestRule_VersionedModulePath_BindsThePackageName(t *testing.T) {
	t.Parallel()
	src := `package circle

import "github.com/danielgtaylor/huma/v2"

func wire(api huma.API) { huma.Register(api, huma.Operation{}, nil) }
`
	found, err := repogate.CheckSource(repogate.RouteRule(), "internal/circle/wire.go", src)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "huma.Register", found[0].Ref)
}
