package api_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// updateSpec rewrites the checked-in OpenAPI document. A package-level flag variable is the only
// shape the testing package offers; it is written once by flag parsing and read thereafter.
var updateSpec = flag.Bool("update", false, "rewrite openapi/openapi.json")

// The document is checked in so that a change to the API surface is a diff a reviewer sees, and so
// that `oasdiff` has a base to compare against. It is generated from the handlers, so it cannot
// describe an operation nobody serves.
func TestOpenAPISpec_Generated_MatchesTheCheckedInFile(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	path := filepath.Join(root, api.SpecPath)

	want, err := api.SpecJSON()
	require.NoError(t, err)

	if *updateSpec {
		// Refused under CI for the same reason the golden corpus refuses it: the fastest route to
		// a green build must never be rewriting what the build checks.
		require.Empty(t, os.Getenv("CI"), "-update is refused in CI")
		require.NoError(t, os.WriteFile(path, want, 0o644))
		return
	}

	got, err := os.ReadFile(path)
	require.NoError(t, err, "run `make gen-openapi`")
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s is stale (-generated +checked-in):\n%s\nregenerate it with `make gen-openapi`",
			api.SpecPath, diff)
	}
}

// A generated file that differs between two runs of the generator is a file whose diff nobody can
// read, and a check that fails at random.
func TestOpenAPISpec_GeneratedTwice_IsByteIdentical(t *testing.T) {
	t.Parallel()
	first, err := api.SpecJSON()
	require.NoError(t, err)
	second, err := api.SpecJSON()
	require.NoError(t, err)
	require.Equal(t, string(first), string(second))
}
