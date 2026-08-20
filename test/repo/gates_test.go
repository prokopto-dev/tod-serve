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

// RAND001. Every injected entropy source is `crypto/rand.Reader`.
//
// This is the gate the identity subsystem asked for by name and could not write itself.
// `identity.New` takes an injected entropy source and refuses a nil one rather than falling back to
// a default, which makes "a generator that quietly reaches for a weak source" a construction error
// instead of a review habit — but the absence of a default only makes the choice DELIBERATE at the
// wiring site. Nothing in the type system forces that site to say `rand.Reader`.
//
// A deliberate choice nothing verifies is a wish, so this verifies it: the analyser parses every
// non-test source file and requires each `Entropy` field and each named entropy sink to be exactly
// the `Reader` of a `crypto/rand` import — not merely non-nil, and not a variable that happens to
// hold it.
func TestRAND001_ProductionWiring_UsesCryptoRandReader(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	got, err := repogate.CheckEntropy(root, scanned())
	require.NoError(t, err)
	require.Greater(t, got.Files, 10, "the analyser found almost no Go files; the roots are wrong")

	for _, f := range got.Findings {
		t.Errorf("%s: an injected entropy source must be crypto/rand.Reader", f)
	}
}

// A gate nobody has seen fail is a gate nobody knows works. Each case below is a way the wiring
// could pass a `nil`-check and still be wrong.
func TestRAND001_AWeakOrIndirectSource_IsReported(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
	}{
		{
			name: "a math/rand reader, which is the failure the whole rule exists for",
			src: `package main
import mrand "math/rand"
import "example/identity"
func wire() identity.Config { return identity.Config{Entropy: mrand.New(nil)} }`,
		},
		{
			name: "a variable that HOLDS crypto/rand.Reader, which a nil-check would accept",
			src: `package main
import "crypto/rand"
import "example/identity"
var source = rand.Reader
func wire() identity.Config { return identity.Config{Entropy: source} }`,
		},
		{
			name: "a wrapper function returning it, which reads fine and cannot be checked",
			src: `package main
import "crypto/rand"
import "example/identity"
func entropy() io.Reader { return rand.Reader }
func wire() identity.Config { return identity.Config{Entropy: entropy()} }`,
		},
		{
			name: "an io.Reader parameter, so the caller decides and this file cannot say",
			src: `package main
import "example/identity"
func wire(source io.Reader) identity.Config { return identity.Config{Entropy: source} }`,
		},
		{
			name: "crypto/rand imported but a different symbol used",
			src: `package main
import "crypto/rand"
import "example/identity"
func wire() identity.Config { return identity.Config{Entropy: rand.Prime} }`,
		},
		{
			name: "the token minter handed something else",
			src: `package main
import "example/auth"
func wire(source io.Reader) { auth.NewMinter(pepper, source) }`,
		},
		{
			name: "the id generator handed something else",
			src: `package main
import "example/core"
func wire(source io.Reader) { core.NewGenerator(source) }`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := repogate.CheckEntropySource("wiring.go", tt.src)
			require.NoError(t, err)
			require.NotEmpty(t, got, "RAND001 did not fire on:\n%s", tt.src)
			require.Equal(t, repogate.EntropyRuleID, got[0].Rule)
		})
	}
}

// And the other direction: the shapes the real wiring uses must NOT be reported, or the gate is
// one somebody turns off. An aliased import is included because that is how the analyser earns
// being a parser rather than a grep.
func TestRAND001_TheRealShapes_AreAccepted(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		`package main
import "crypto/rand"
import "example/identity"
func wire() identity.Config { return identity.Config{Entropy: rand.Reader} }`,
		`package main
import cryptorand "crypto/rand"
import "example/identity"
func wire() identity.Config { return identity.Config{Entropy: cryptorand.Reader} }`,
		`package main
import "crypto/rand"
import "example/auth"
func wire(pepper core.Secret) { auth.NewMinter(pepper, rand.Reader) }`,
		`package main
import "crypto/rand"
import "example/core"
func wire() { core.NewGenerator(rand.Reader) }`,
		// A file that names no entropy sink at all has nothing to report, and a gate that fired on
		// one would be a gate every file had to work around.
		`package main
func wire() int { return 1 }`,
	} {
		got, err := repogate.CheckEntropySource("wiring.go", src)
		require.NoError(t, err)
		require.Empty(t, got, "RAND001 fired on wiring that is correct:\n%s", src)
	}
}
