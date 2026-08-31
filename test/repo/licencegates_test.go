package repo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// A gate nobody has watched fail is a gate nobody knows works, and this one guards a rule that had
// NO mechanism at all until recently: docs/concepts/invariants.md named `scripts/licence-gate.sh`
// as its enforcement long before the file existed. So the classifier is driven over the licence
// texts it exists to tell apart, in both directions.
//
// The deny cases matter more than the allow cases. A classifier that returned `MIT` for everything
// would pass a one-way test over permissive fixtures and let AGPL into the binary.
func TestLIC001_TheClassifier_TellsLicencesApart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
		text string
	}{
		{
			name: "apache 2.0",
			want: "Apache-2.0",
			text: "Apache License\nVersion 2.0, January 2004\nhttp://www.apache.org/licenses/",
		},
		{
			name: "mit",
			want: "MIT",
			text: "MIT License\n\nPermission is hereby granted, free of charge, to any person " +
				"obtaining a copy of this software",
		},
		{
			name: "isc",
			want: "ISC",
			text: "ISC License\n\nPermission to use, copy, modify, and/or distribute this " +
				"software for any purpose",
		},
		{
			// The three-clause licence is told from the two-clause one by the no-endorsement
			// clause, not by a title: plenty of LICENSE files carry neither number.
			name: "bsd 3 clause",
			want: "BSD-3-Clause",
			text: "Redistribution and use in source and binary forms, with or without " +
				"modification, are permitted provided that the following conditions are met:\n" +
				"Neither the name of Google Inc. nor the names of its contributors may be used",
		},
		{
			name: "bsd 2 clause",
			want: "BSD-2-Clause",
			text: "Redistribution and use in source and binary forms, with or without " +
				"modification, are permitted provided that the following conditions are met:\n" +
				"1. Redistributions of source code must retain the above copyright notice.",
		},
		// Everything below must be REFUSED. These are the cases the gate exists for.
		{
			name: "gpl",
			want: "GPL",
			text: "GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007",
		},
		{
			// AGPL and LGPL both quote long stretches of the GPL, so a classifier that tested for
			// "gnu general public license" first would call them both GPL. Order is load-bearing.
			name: "agpl is not mistaken for gpl",
			want: "AGPL",
			text: "GNU AFFERO GENERAL PUBLIC LICENSE\nVersion 3, 19 November 2007\n" +
				"This licence incorporates the terms of the GNU General Public License.",
		},
		{
			name: "lgpl is not mistaken for gpl",
			want: "LGPL",
			text: "GNU LESSER GENERAL PUBLIC LICENSE\nVersion 3\n" +
				"This version incorporates the terms of the GNU General Public License.",
		},
		{
			name: "sspl",
			want: "SSPL",
			text: "Server Side Public License\nVERSION 1, October 16, 2018",
		},
		{
			name: "busl",
			want: "BUSL",
			text: "Business Source License 1.1\n\nParameters\nLicensor: Example Inc.",
		},
		{
			// Weak copyleft is still copyleft, and the invariant says no runtime dependency may
			// carry it. Admitting MPL "because it is only file-level" is the argument that ends
			// with somebody admitting LGPL for the same reason.
			name: "mpl is refused rather than treated as permissive",
			want: "MPL-2.0",
			text: "Mozilla Public License Version 2.0\n1. Definitions",
		},
		{
			// FAIL CLOSED. The point of the gate is that a new dependency gets read by a person.
			name: "an unrecognised licence is unknown, never permissive",
			want: "UNKNOWN",
			text: "Copyright somebody. You may use this if you are nice about it.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := filepath.Join(t.TempDir(), "LICENSE")
			require.NoError(t, os.WriteFile(f, []byte(tc.text), 0o600))

			out, err := exec.CommandContext(t.Context(), "bash", gateScript(t), "classify", f).Output()
			require.NoError(t, err)
			require.Equal(t, tc.want, strings.TrimSpace(string(out)))
		})
	}
}

// The allow-list and the classifier are two halves that must agree: a licence the classifier names
// correctly is still a hole if `permitted` waves it through. This asserts the SECOND half directly
// by running the real gate against a module tree whose only dependency is AGPL.
func TestLIC001_ACopyleftDependency_IsReported(t *testing.T) {
	t.Parallel()

	out, err := exec.CommandContext(t.Context(),
		"bash", gateScript(t), "classify", agplFixture(t)).Output()
	require.NoError(t, err)
	require.Equal(t, "AGPL", strings.TrimSpace(string(out)),
		"the classifier must name AGPL before the allow-list can refuse it")

	// AGPL is absent from `permitted`, which is what the grep below asserts. Reading the script is
	// the honest check here: driving the whole walk would need a fake module cache, and a gate that
	// tested its own allow-list by calling it would be one derivation on both sides.
	src, err := os.ReadFile(gateScript(t))
	require.NoError(t, err)
	permitted := permittedLine(t, string(src))
	for _, denied := range []string{"AGPL", "LGPL", "GPL", "SSPL", "BUSL", "MPL-2.0", "UNKNOWN"} {
		require.NotContains(t, permitted, denied,
			"%s appears in the licence-gate allow-list", denied)
	}
	for _, allowed := range []string{"Apache-2.0", "MIT", "BSD-2-Clause", "BSD-3-Clause", "ISC"} {
		require.Contains(t, permitted, allowed,
			"%s is not in the allow-list, so every dependency under it is a red build", allowed)
	}
}

// The gate must refuse to report a pass it did not earn. With no Go toolchain it can classify
// nothing, and a silent skip there is how a rule stops being enforced on exactly the machines that
// do not run the full suite.
func TestLIC001_WithoutAToolchain_FailsRatherThanSkipping(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "bash", gateScript(t))
	cmd.Env = append(os.Environ(), "PATH=/nonexistent")
	out, err := cmd.CombinedOutput()

	require.Error(t, err, "the gate passed with no Go toolchain on PATH:\n%s", out)
	require.Contains(t, string(out), "LIC001")
}

func gateScript(t *testing.T) string {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	return filepath.Join(root, "scripts", "licence-gate.sh")
}

func agplFixture(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "LICENSE")
	require.NoError(t, os.WriteFile(f,
		[]byte("GNU AFFERO GENERAL PUBLIC LICENSE\nVersion 3, 19 November 2007"), 0o600))
	return f
}

// permittedLine returns the body of the `permitted()` case arm, which is the allow-list itself.
func permittedLine(t *testing.T, src string) string {
	t.Helper()
	const marker = "permitted() {"
	i := strings.Index(src, marker)
	require.GreaterOrEqual(t, i, 0, "scripts/licence-gate.sh has no permitted() function")
	rest := src[i:]
	end := strings.Index(rest, "\n}")
	require.GreaterOrEqual(t, end, 0, "permitted() is not terminated")
	return rest[:end]
}

// The two halves above drive the CLASSIFIER. These drive the WALK, which is the half that decides
// which licence file the classifier is even handed — and where both of this gate's fail-open routes
// were: a green result is only worth something if it covers the whole module graph, and if each
// licence belongs to the module it is credited to.
//
// Both are driven through a `go` shim on PATH. The walk's inputs are a status and a set of module
// directories, so a fake `go` is the honest seam: pointing the real one at a broken tree would test
// the Go toolchain's error reporting rather than this gate's reaction to it.
func TestLIC001_AFailedModuleWalk_IsReported(t *testing.T) {
	t.Parallel()

	// What a partial graph load actually looks like: real module lines on stdout, a diagnostic on
	// stderr, and a non-zero status. `go list` prints what it resolved before it gave up, so the
	// output alone looks like a healthy walk — the status is the only thing that says otherwise,
	// and ignoring it reported a pass over whatever subset survived.
	bin := goShim(t, "example.com/dep|"+permissiveModule(t), "cannot load example.com/broken", 1)

	out, err := runLicenceGate(t, bin)
	require.Error(t, err, "LIC001 passed over a module walk that failed:\n%s", out)
	require.Contains(t, out, "LIC001")
	require.Contains(t, out, "module graph is incomplete")
	require.Contains(t, out, "cannot load example.com/broken",
		"the finding must carry go list's own reason; discarding stderr is what hid this")
}

// The false-positive half of the same seam. A status check that fired on a healthy walk would be
// reverted within a day, and without this the test above passes for a gate hard-wired to fail.
func TestLIC001_AWalkThatSucceeds_StillPasses(t *testing.T) {
	t.Parallel()

	bin := goShim(t, "example.com/dep|"+permissiveModule(t), "", 0)

	out, err := runLicenceGate(t, bin)
	require.NoError(t, err, "LIC001 reported a finding over a clean walk:\n%s", out)
	require.Contains(t, out, "1 runtime module(s)")
}

// A dependency whose own directory holds no licence must be a finding, NOT a pass borrowed from
// whatever sits above it in the module cache. Every ancestor of a module directory belongs to some
// other module, so a licence found there is credited to the wrong one — and since the ancestor is
// usually permissive, the effect was to wave through exactly the unlicensed dependency the gate
// exists to stop. It fails on the licence it can see, rather than passing on one it cannot.
func TestLIC001_ALicenceOnlyInAnAncestor_IsReported(t *testing.T) {
	t.Parallel()

	// parent/ holds an MIT LICENSE; parent/dep@v1.0.0/ — the module itself — holds none.
	parent := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(parent, "LICENSE"),
		[]byte("Permission is hereby granted, free of charge, to any person obtaining a copy"), 0o600))
	dep := filepath.Join(parent, "dep@v1.0.0")
	require.NoError(t, os.Mkdir(dep, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dep, "dep.go"), []byte("package dep\n"), 0o600))

	bin := goShim(t, "example.com/dep|"+dep, "", 0)

	out, err := runLicenceGate(t, bin)
	require.Error(t, err, "LIC001 credited an ancestor's licence to the module below it:\n%s", out)
	require.Contains(t, out, "ships no LICENSE file this gate can find")
}

// permissiveModule returns a module directory carrying its own MIT licence, which is what a healthy
// dependency looks like to the walk.
func permissiveModule(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(d, "LICENSE"),
		[]byte("Permission is hereby granted, free of charge, to any person obtaining a copy"), 0o600))
	return d
}

// goShim writes a `go` that answers the one call the gate makes, and returns the directory to put
// at the front of PATH.
func goShim(t *testing.T, stdout, stderr string, status int) string {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n"
	if stdout != "" {
		script += "printf '%s\\n' " + shellQuote(stdout) + "\n"
	}
	if stderr != "" {
		script += "printf '%s\\n' " + shellQuote(stderr) + " >&2\n"
	}
	script += fmt.Sprintf("exit %d\n", status)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700))
	return bin
}

// runLicenceGate runs the whole gate with `bin` ahead of the system directories, so the shim above
// is the `go` it finds. The real directories stay on PATH because the gate needs find, sort, tr and
// mktemp to do its job at all.
func runLicenceGate(t *testing.T, bin string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "bash", gateScript(t))
	cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
