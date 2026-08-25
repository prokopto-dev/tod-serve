package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// TestServe_PlaceholderSecret_Refused — the mechanism behind `deploy/env.example`.
//
// That file is published in a public repository. The worst thing it can produce is a working
// instance running on a value everybody can read: `TOD_TOKEN_PEPPER` is what makes a stolen
// database file useless on its own, so a copied-and-shipped pepper means anybody holding the
// example file can forge a token against the rows in it.
//
// The prefix is the mechanism, not a convention — and the second half of this test is what keeps
// the two in step: it reads the values THAT FILE actually ships and requires the binary to refuse
// each one. Somebody replacing a placeholder with something that looks realistic is a red test.
func TestServe_PlaceholderSecret_Refused(t *testing.T) {
	// No t.Parallel: t.Setenv, which is how `serve` reads a secret at all.
	path := filepath.Join(t.TempDir(), "tod.db")
	_, err := captureCLI(t, "migrate", "--db", path)
	require.NoError(t, err)

	tests := []struct {
		name          string
		pepper, key   string
		wantMentioned string
	}{
		{
			name:          "the pepper",
			pepper:        placeholderPrefix + "generate_with_openssl_rand_base64_48",
			key:           "a-real-session-key",
			wantMentioned: envTokenPepper,
		},
		{
			name:          "the session key",
			pepper:        "a-real-pepper",
			key:           placeholderPrefix + "generate_with_openssl_rand_base64_48",
			wantMentioned: envSessionKey,
		},
		{
			// What somebody leaves behind after deleting the placeholder's tail and nothing else.
			name:          "the prefix on its own",
			pepper:        placeholderPrefix,
			key:           "a-real-session-key",
			wantMentioned: envTokenPepper,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envTokenPepper, tt.pepper)
			t.Setenv(envSessionKey, tt.key)

			_, err := captureCLI(t, "serve", "--db", path)
			require.Error(t, err, "serve started on a placeholder secret")
			require.Contains(t, err.Error(), tt.wantMentioned,
				"the error must name WHICH variable is still a placeholder")
			require.Contains(t, err.Error(), "openssl rand -base64 48",
				"the error must say how to generate a real one; an operator meeting this is "+
					"mid-deploy and should not have to go looking")
		})
	}
}

// And the values `deploy/env.example` ships are values that binary refuses.
//
// This is the half that cannot be satisfied by agreeing with itself: one side is a Go constant and
// the other is a published file, read off disk. Replacing a `CHANGE_ME_` placeholder with something
// that merely looks like a secret would leave the file bootable and this test red.
func TestEnvExample_EverySecretItShips_IsOneServeRefuses(t *testing.T) {
	t.Parallel()

	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(root, "deploy", "env.example"))
	require.NoError(t, err)

	// The variables that hold a secret. Named here rather than derived from the file, because the
	// question being asked is "does env.example ship a bootable value for each of THESE", and
	// deriving the list from the same file is how a both-directions check becomes a tautology.
	found := map[string]bool{envTokenPepper: false, envSessionKey: false}

	for _, line := range strings.Split(string(body), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if _, wanted := found[name]; !wanted {
			continue
		}
		require.Error(t, refusePlaceholders(map[string]core.Secret{name: core.Secret(value)}),
			"deploy/env.example ships %s=%q, which `serve` would ACCEPT — a bootable secret in a "+
				"published file is the one thing that file must never contain", name, value)
		found[name] = true
	}

	// Over an empty set every assertion above is vacuously true, which is exactly how this gate
	// would stop checking after somebody reformatted the file.
	for name, seen := range found {
		require.True(t, seen, "deploy/env.example carries no %s= line at all", name)
	}
}
