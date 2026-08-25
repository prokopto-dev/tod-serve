package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// healthyInstance writes an instance with one enabled OIDC provider, at the given origins.
//
// It is a fully HEALTHY instance apart from whatever the caller makes disagree, because a doctor
// test that leaves other problems in place would pass on its exit code without the check under test
// ever firing.
func healthyInstance(t *testing.T, publicURL, redirectURI string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tod.db")
	_, err := captureCLI(t, "migrate", "--db", path)
	require.NoError(t, err)

	db := openCopy(t, path)
	_, err = db.Queries().CreateInstance(t.Context(), sqlitegen.CreateInstanceParams{
		Name: "Host Test", PublicUrl: publicURL, Timezone: "UTC",
		SelfServiceCircleCreation: 0, CreatedAt: 1, UpdatedAt: 1,
	})
	require.NoError(t, err)

	clientID := "1234567890"
	issuer := "https://issuer.example.com"
	_, err = db.Queries().CreateIdentityProvider(t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: "01M0000000000000000000000A", Key: "oidc", Kind: "oidc",
			DisplayName: "OIDC", Enabled: 1, VerifiableSubject: 1,
			Issuer: &issuer, ClientID: &clientID, RedirectUri: &redirectURI,
			CreatedAt: 1, UpdatedAt: 1,
		})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path
}

// TestDoctor_APublicURLTheProvidersDisagreeWith_IsAProblem — the hostname is written down five
// times and nothing compared any of them.
//
// Two of those copies are visible to this binary, and they are the two whose disagreement is
// SILENT: `spaJoinURL` prefers $TOD_PUBLIC_URL over the stored row, so an instance with a wrong
// `public_url` still hands out join links that look right, and the first symptom is an opaque OAuth
// error days later.
//
// The assertion is on the EXIT CODE, not on the printed text, because the exit code is what a
// deploy reads — `.github/workflows/deploy.yml` runs this verb after `/healthz` and `/readyz` and
// fails the deploy red on it.
//
// The agreeing case is the vacuity guard, and it is the one that matters: without it every
// assertion here would pass over a check that never fired.
func TestDoctor_APublicURLTheProvidersDisagreeWith_IsAProblem(t *testing.T) {
	// No t.Parallel: t.Setenv, which is the only way to drive the $TOD_PUBLIC_URL copy.
	const host = "https://tod.example.com"

	tests := []struct {
		name        string
		publicURL   string
		redirectURI string
		env         string
		wantProblem bool
		why         string
	}{
		{
			name:        "all three agree",
			publicURL:   host,
			redirectURI: host + "/api/v1/auth/callback/oidc",
			env:         host,
			wantProblem: false,
			why: "the vacuity guard: a check that fires on everything proves nothing about the " +
				"cases that are supposed to fail",
		},
		{
			name:        "the stored URL and the environment disagree",
			publicURL:   "https://old.example.com",
			redirectURI: host + "/api/v1/auth/callback/oidc",
			env:         host,
			wantProblem: true,
			why: "spaJoinURL prefers the environment, so the join links look right and the stored " +
				"row is quietly wrong",
		},
		{
			name:        "the stored URL and a redirect URI disagree",
			publicURL:   host,
			redirectURI: "https://old.example.com/api/v1/auth/callback/oidc",
			env:         "",
			wantProblem: true,
			why: "this instance serves the callback route, so a callback sent to another origin " +
				"never arrives",
		},
		{
			name:        "a redirect URI on the right host but the wrong scheme",
			publicURL:   host,
			redirectURI: "http://tod.example.com/api/v1/auth/callback/oidc",
			env:         host,
			wantProblem: true,
			why:         "the scheme is part of the origin, and http is not what the cookie needs",
		},
		{
			name:        "a redirect URI on the right host and a different port",
			publicURL:   host,
			redirectURI: "https://tod.example.com:8443/api/v1/auth/callback/oidc",
			env:         host,
			wantProblem: true,
			why:         "the port is part of the origin too",
		},
		{
			// The middle of the range rather than its ends: these two strings are DIFFERENT and
			// name the SAME origin. Reporting them would be a false positive on a correct
			// configuration, and a check with false positives is one somebody switches off.
			name:        "an explicit default port, which is the same origin",
			publicURL:   "https://tod.example.com:443",
			redirectURI: host + "/api/v1/auth/callback/oidc",
			env:         "HTTPS://TOD.EXAMPLE.COM",
			wantProblem: false,
			why:         "scheme and host are case-insensitive and :443 is https's default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := healthyInstance(t, tt.publicURL, tt.redirectURI)
			t.Setenv(envPublicURL, tt.env)

			out, err := captureCLI(t, "doctor", "--db", path)
			if tt.wantProblem {
				require.Error(t, err, "doctor exited 0: %s\n%s", tt.why, out)
				require.Contains(t, err.Error(), "problem")
				return
			}
			require.NoError(t, err, "doctor found a problem it should not have: %s\n%s", tt.why, out)
		})
	}
}

// And the origin reduction on its own, because it is the half that decides everything above.
func TestOriginOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want string
	}{
		{"https://tod.example.com", "https://tod.example.com"},
		{"https://tod.example.com/", "https://tod.example.com"},
		{"https://tod.example.com/api/v1/auth/callback/discord", "https://tod.example.com"},
		{"HTTPS://TOD.EXAMPLE.COM/x", "https://tod.example.com"},
		{"https://tod.example.com:443/x", "https://tod.example.com"},
		{"http://tod.example.com:80/x", "http://tod.example.com"},
		{"https://tod.example.com:8443/x", "https://tod.example.com:8443"},
		{"http://localhost:8080", "http://localhost:8080"},
		{"http://[::1]:8080/x", "http://[::1]:8080"},
		{"  https://tod.example.com  ", "https://tod.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			got, err := originOf(tt.raw)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOriginOf_WhatIsRefused(t *testing.T) {
	t.Parallel()

	// A value that names no origin is reported as unusable rather than compared as one — comparing
	// "" against "" would make two broken copies agree.
	for _, raw := range []string{"", "tod.example.com", "/api/v1", "https://", "not a url at all"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := originOf(raw)
			require.Error(t, err, "%q was accepted as an origin", raw)
		})
	}
}

// A brand-new instance has one copy of the URL and nothing to compare it with. That must not be a
// problem, and it must not be a silent pass either.
func TestDoctor_OneCopyOfTheURL_IsNotADisagreement(t *testing.T) {
	t.Parallel()

	claims := []hostClaim{{source: "the instance row", raw: "https://tod.example.com"}}
	var oks, bads []string
	checkHostAgreement(claims, func(s string) { oks = append(oks, s) }, func(s string) { bads = append(bads, s) })

	require.Empty(t, bads)
	require.Len(t, oks, 1)
	require.Contains(t, oks[0], "nothing can disagree yet",
		"a gate reporting success over an empty search space has to say the space was empty")
}

// openCopy is shared with backup_test.go; this asserts the helper above really wrote a store.
func TestHealthyInstance_IsActuallyHealthy(t *testing.T) {
	t.Parallel()

	path := healthyInstance(t, "https://tod.example.com", "https://tod.example.com/cb")
	db := openCopy(t, path)
	require.NoError(t, db.Ready(t.Context()))
	row, err := db.Queries().GetInstance(t.Context())
	require.NoError(t, err)
	require.Equal(t, "https://tod.example.com", row.PublicUrl)
	require.False(t, store.IsNotFound(err))
}
