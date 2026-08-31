package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// healthyInstance writes an instance with one enabled OIDC provider, at the given origins.
//
// It is a fully HEALTHY instance apart from whatever the caller makes disagree, because a doctor
// test that leaves other problems in place would pass on its exit code without the check under test
// ever firing. That includes an ADMINISTRATOR: an instance nobody holds `instance.security.manage`
// on is a problem in its own right, and one that would otherwise fail every case below.
func healthyInstance(t *testing.T, publicURL, redirectURI string) string {
	t.Helper()
	path := instanceWithNoAdministrator(t, publicURL, redirectURI)
	db := openCopy(t, path)
	grantAdministrator(t, db, testProviderID, testCircleID, authz.PermissionInstanceOwner)
	require.NoError(t, db.Close())
	return path
}

// instanceWithNoAdministrator is [healthyInstance] up to the last bootstrap step: an instance row
// and one enabled provider, and nobody who can administer it. It is what `tod-serve init` leaves
// behind, which is why it is a fixture of its own rather than an inlined half of the one above.
func instanceWithNoAdministrator(t *testing.T, publicURL, redirectURI string) string {
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
			ID: testProviderID, Key: "oidc", Kind: "oidc",
			DisplayName: "OIDC", Enabled: 1, VerifiableSubject: 1,
			Issuer: &issuer, ClientID: &clientID, RedirectUri: &redirectURI,
			CreatedAt: 1, UpdatedAt: 1,
		})
	require.NoError(t, err)
	_, err = db.Queries().CreateCircle(t.Context(), sqlitegen.CreateCircleParams{
		CircleID: testCircleID, Name: "Ops", NameNorm: "ops", Server: "blue", Timezone: "UTC",
		MinReportersToSupersede: 2, RevokeInvalidatesInvites: 0,
		State: schemaenum.CircleStateActive, CreatedAt: 1, UpdatedAt: 1,
	})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path
}

const (
	// testProviderID is the provider [instanceWithNoAdministrator] writes, and the one an identity
	// hangs off.
	testProviderID = "01M0000000000000000000000A"
	// testCircleID is the circle an administrator's membership lives in. A grant is not enough on
	// its own — see [checkAdministrable] — so the fixture has to write one.
	testCircleID = "01M0000000000000000000000Z"
)

// grantAdministrator finishes the bootstrap: it creates the identity a redeemed owner code would
// have created, and grants it `instance.owner` through the real ledger.
//
// It exists because "an instance with an instance row and a provider" is NOT a healthy instance —
// nobody can administer it — and a doctor fixture that stopped there would leave every assertion
// about some other check passing over an unrelated PROBLEM. Which is the confusion this whole
// change is about: `init` having run is not the end of the bootstrap.
//
// The identity is inserted directly because an identity is created by JOINING a circle and that
// needs a server; the GRANT goes through `instancegrant.Service` over the real table, so a schema
// the service could not actually write is a red test rather than a green one.
func grantAdministrator(
	t *testing.T, db *store.DB, providerID, circleID string, perms ...authz.Permission,
) core.IdentityID {
	t.Helper()
	return seedIdentity(t, db, providerID, circleID, 0, perms...)
}

// seedIdentity writes the nth identity on this instance, A LIVE HUMAN MEMBERSHIP for it, and each
// permission as granted.
//
// The membership is not decoration. An instance grant is on an identity, but the identity only
// reaches a request through a membership — `Authenticator.membership` reads one on every call and
// refuses a revoked one, or one in a deleted circle. So an identity with a grant and no live
// membership holds a permission nothing can carry, and a fixture that omitted the membership
// would let [checkAdministrable] pass by counting exactly that.
//
// `n` is the caller's, not a counter: package-level mutable state is banned here and a fixture
// that numbered itself would hand two parallel tests the same id. It is the last ULID character,
// so `n` is bounded by the alphabet — which is more identities than any doctor test needs.
func seedIdentity(
	t *testing.T, db *store.DB, providerID, circleID string, n int, perms ...authz.Permission,
) core.IdentityID {
	t.Helper()
	require.Less(t, n, 26, "seedIdentity only has one character to number identities with")
	suffix := string(rune('A' + n))
	id := "01M0000000000000000000000" + suffix
	_, err := db.Queries().CreateIdentity(t.Context(), sqlitegen.CreateIdentityParams{
		ID: id, ProviderID: providerID,
		Subject: "operator-" + suffix, DisplayName: "Operator " + suffix,
		CreatedAt: 1, UpdatedAt: 1,
	})
	require.NoError(t, err)
	_, err = db.Queries().CreateMembership(t.Context(), sqlitegen.CreateMembershipParams{
		ID: membershipIDFor(suffix), CircleID: circleID, IdentityID: &id,
		Kind:        schemaenum.MembershipKindHuman,
		DisplayName: "Operator " + suffix, DisplayNameNorm: "operator-" + suffix,
		Role:     string(authz.RoleOwner),
		JoinedAt: 1, CreatedAt: 1, UpdatedAt: 1,
	})
	require.NoError(t, err)

	identityID, err := core.ParseID[core.Identity](id)
	require.NoError(t, err)
	grants, err := newGrantService(db)
	require.NoError(t, err)
	for _, perm := range perms {
		_, err = grants.Decide(t.Context(), instancegrant.DecideRequest{
			IdentityID: identityID, Permission: perm,
			Decision: instancegrant.DecisionGranted, Reason: "fixture",
		})
		require.NoError(t, err)
	}
	return identityID
}

// membershipIDFor is the membership [seedIdentity] writes for the nth identity.
func membershipIDFor(suffix string) string { return "01M0000000000000000000001" + suffix }

// revokeMembershipOf is one of the two ways a grant outlives the principal that could carry it.
func revokeMembershipOf(t *testing.T, db *store.DB, n int) {
	t.Helper()
	at := int64(2)
	id := membershipIDFor(string(rune('A' + n)))
	_, err := db.Queries().RevokeMembership(t.Context(), sqlitegen.RevokeMembershipParams{
		RevokedAt: &at, RevokedByMembershipID: &id, UpdatedAt: at,
		CircleID: testCircleID, ID: id,
	})
	require.NoError(t, err)
}

// deleteTestCircle is the other: the membership is intact and the circle it is in is a tombstone.
func deleteTestCircle(t *testing.T, db *store.DB) {
	t.Helper()
	at := int64(2)
	_, err := db.Queries().SoftDeleteCircle(t.Context(), sqlitegen.SoftDeleteCircleParams{
		DeletedAt: &at, UpdatedAt: at, CircleID: testCircleID,
	})
	require.NoError(t, err)
}

// finishBootstrap does to an `init`ed database what redeeming the owner code and running
// `tod-serve instance grant` does: it gives the instance somebody who can administer it.
//
// `init` alone does not, and that is the whole shape of the bug this is part of fixing — a
// database `init` has run on looks bootstrapped, answers every HTTP check, and has no
// administrator.
func finishBootstrap(t *testing.T, path string) {
	t.Helper()
	db := openCopy(t, path)
	providers, err := db.Queries().ListIdentityProviders(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, providers,
		"init created no provider, so there is nothing to hang an identity off")
	// The circle `init` created, not a fixture one: the membership has to be somewhere a
	// credential could really be bound, and `checkAdministrable` now reads exactly that.
	circles, err := db.Queries().ListLiveCircles(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, circles, "init created no circle, so a membership has nowhere to live")
	grantAdministrator(t, db, providers[0].ID, circles[0].ID, authz.PermissionInstanceOwner)
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

// TestDoctor_WhoCanAdministerTheInstance_IsAProblemWhenNobodyCan is the gate on an instance
// nobody can administer.
//
// The failure it exists for cost a real setup session. `instance.owner` was grantable and no route
// required it, so the deployment runbook's bootstrap ended with a command that succeeded, wrote an
// audited ledger row, and handed the operator nothing — and then sent them to register the Discord
// provider, which needs `instance.security.manage`. The console hides the Instance nav entry
// rather than explaining it, so nothing on screen pointed at the cause. `doctor` is where an
// operator looks when nothing on screen does.
//
// The `instance.owner` row is the case that matters: doctor has to consult the same expansion the
// request path does, or it reports a problem the operator has already fixed. The `ops.read` row is
// the other half — a grant that is not administration must not count, or every instance with a
// dashboard user looks administrable.
func TestDoctor_WhoCanAdministerTheInstance_IsAProblemWhenNobodyCan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		seed        func(t *testing.T, db *store.DB)
		wantProblem bool
		wantLine    string
		why         string
	}{
		{
			name:        "nobody holds anything",
			seed:        func(*testing.T, *store.DB) {},
			wantProblem: true,
			wantLine:    "nobody can administer this instance",
			why:         "an instance whose only administrator is a shell on the box",
		},
		{
			name: "instance.owner, which is what the runbook says to grant",
			seed: func(t *testing.T, db *store.DB) {
				seedIdentity(t, db, testProviderID, testCircleID, 0, authz.PermissionInstanceOwner)
			},
			wantProblem: false,
			wantLine:    "1 identity can administer this instance",
			why: "instance.owner expands to the instance realm, so doctor must read the " +
				"expansion and not the row",
		},
		{
			name: "the narrower key on its own",
			seed: func(t *testing.T, db *store.DB) {
				seedIdentity(t, db, testProviderID, testCircleID, 0, authz.PermissionInstanceSecurityManage)
			},
			wantProblem: false,
			wantLine:    "1 identity can administer this instance",
			why:         "the key the route actually declares still counts on its own",
		},
		{
			name: "an instance-realm grant that is not administration",
			seed: func(t *testing.T, db *store.DB) {
				seedIdentity(t, db, testProviderID, testCircleID, 0,
					authz.PermissionOpsRead, authz.PermissionInstanceCircleCreate)
			},
			wantProblem: true,
			wantLine:    "nobody can administer this instance",
			why: "ops.read is deliberately not in the capability floor and grants nothing " +
				"else; counting it would call a dashboard user an administrator",
		},
		{
			name: "granted and then revoked",
			seed: func(t *testing.T, db *store.DB) {
				id := seedIdentity(t, db, testProviderID, testCircleID, 0, authz.PermissionInstanceOwner)
				grants, err := newGrantService(db)
				require.NoError(t, err)
				_, err = grants.Decide(t.Context(), instancegrant.DecideRequest{
					IdentityID: id, Permission: authz.PermissionInstanceOwner,
					Decision: instancegrant.DecisionRevoked, Reason: "handed over",
				})
				require.NoError(t, err)
			},
			wantProblem: true,
			wantLine:    "nobody can administer this instance",
			why: "the ledger is append-only, so the revocation is a NEW ROW and the listing " +
				"still contains the grant; counting rows rather than decisions would miss it",
		},
		{
			// The ledger outlives a membership: a grant is on an IDENTITY and the identity only
			// reaches a request through one. `Authenticator.membership` refuses a revoked
			// membership on every call, so this identity holds instance.owner and no credential
			// it could ever present is accepted.
			name: "the only administrator's membership is revoked",
			seed: func(t *testing.T, db *store.DB) {
				seedIdentity(t, db, testProviderID, testCircleID, 0, authz.PermissionInstanceOwner)
				revokeMembershipOf(t, db, 0)
			},
			wantProblem: true,
			wantLine:    "live membership",
			why: "a grant nothing can carry is not an administrator, and reporting one is the " +
				"confidently wrong answer: it says the instance is fine when nobody can log in",
		},
		{
			// The other way the same thing happens, and it is a separate row because it is a
			// separate refusal: the membership is intact and the CIRCLE is a tombstone.
			name: "the only administrator's circle is deleted",
			seed: func(t *testing.T, db *store.DB) {
				seedIdentity(t, db, testProviderID, testCircleID, 0, authz.PermissionInstanceOwner)
				deleteTestCircle(t, db)
			},
			wantProblem: true,
			wantLine:    "live membership",
			why: "a deleted circle stops its members acting on their very next request, so the " +
				"grant is as unreachable as it is under a revocation",
		},
		{
			// And the count is of LIVE administrators. Without this the two rows above could pass
			// by the check refusing everybody the moment any membership is revoked anywhere.
			name: "one administrator revoked and one still live",
			seed: func(t *testing.T, db *store.DB) {
				seedIdentity(t, db, testProviderID, testCircleID, 0, authz.PermissionInstanceOwner)
				revokeMembershipOf(t, db, 0)
				seedIdentity(t, db, testProviderID, testCircleID, 1, authz.PermissionInstanceOwner)
			},
			wantProblem: false,
			wantLine:    "1 identity can administer this instance",
			why:         "the revoked one is not counted and the live one still is",
		},
		{
			name: "two administrators",
			seed: func(t *testing.T, db *store.DB) {
				seedIdentity(t, db, testProviderID, testCircleID, 0, authz.PermissionInstanceOwner)
				seedIdentity(t, db, testProviderID, testCircleID, 1, authz.PermissionInstanceSecurityManage)
			},
			wantProblem: false,
			wantLine:    "2 identities can administer this instance",
			why: "the count is said out loud: one administrator on holiday and several are " +
				"different facts about the same instance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := instanceWithNoAdministrator(t, "https://tod.example.com",
				"https://tod.example.com/api/v1/auth/callback/oidc")
			db := openCopy(t, path)
			tt.seed(t, db)
			require.NoError(t, db.Close())

			out, err := captureCLI(t, "doctor", "--db", path)
			require.Contains(t, out, tt.wantLine, "%s\n%s", tt.why, out)
			if tt.wantProblem {
				require.Error(t, err, "doctor exited 0: %s\n%s", tt.why, out)
				return
			}
			// The exit code, not just the line: the deploy reads the code, and a PROBLEM
			// anywhere else in the report would make the assertion above pass over a red run.
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

// TestDoctor_AnEnabledDiscordProviderWithNoClientSecret_IsAProblem is the gate on the gap between
// what `identity.Provider.Validate` refuses and what `doctor` reports.
//
// `Validate` rejects a `discord` row with no client secret, and the browser flow validates the
// provider BEFORE it redirects — so such a row fails every sign-in. The CHECK constraint ties only
// `client_id` to `kind`, so the row saves cleanly; a legacy row, or one repaired directly in SQL,
// reaches this state. `doctor` reported `ok` on it and exited 0, which is the confident mistake:
// the operator is told the instance is healthy and finds out when somebody clicks the button.
//
// The `oidc` rows are the other half, and they are why this is scoped rather than general. An
// `oidc` provider serving non-browser `id_token` clients legitimately has no secret — reporting it
// would be a false positive on a working configuration, and a check with false positives is one
// somebody switches off.
func TestDoctor_AnEnabledDiscordProviderWithNoClientSecret_IsAProblem(t *testing.T) {
	t.Parallel()

	const host = "https://tod.example.com"

	tests := []struct {
		name        string
		kind        string
		secret      *string
		wantProblem bool
		why         string
	}{
		{
			name:        "discord with a client secret",
			kind:        "discord",
			secret:      ptr("shhh"),
			wantProblem: false,
			why: "the vacuity guard: a check that fires on every discord row proves nothing " +
				"about the one that is broken",
		},
		{
			name:        "discord with no client secret at all",
			kind:        "discord",
			secret:      nil,
			wantProblem: true,
			why:         "discord.New refuses to build a client, so every sign-in is a 500",
		},
		{
			name:        "discord with an empty client secret",
			kind:        "discord",
			secret:      ptr(""),
			wantProblem: true,
			why: "core.Secret treats an empty string as absent, so this row is the NULL case " +
				"wearing a different spelling",
		},
		{
			name:        "oidc with no client secret",
			kind:        "oidc",
			secret:      nil,
			wantProblem: false,
			why: "an oidc provider serving non-browser id_token clients needs no secret; " +
				"reporting it would refuse a configuration that works",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A key of its own: healthyInstance already wrote one called "oidc", and the
			// redirect URI checkRedirectURIs expects is derived from the key.
			key := tt.kind + "2"
			path := healthyInstance(t, host, host+"/api/v1/auth/callback/oidc")
			addProvider(t, path, key, tt.kind, tt.secret, host+"/api/v1/auth/callback/"+key)

			out, err := captureCLI(t, "doctor", "--db", path)
			if tt.wantProblem {
				require.Error(t, err, "doctor exited 0: %s\n%s", tt.why, out)
				require.Contains(t, out, "has no client secret",
					"doctor found a problem but not this one, so the test would pass on an "+
						"unrelated failure:\n%s", out)
				return
			}
			require.NoError(t, err, "doctor found a problem it should not have: %s\n%s", tt.why, out)
		})
	}
}

// addProvider writes a second enabled provider beside the one [healthyInstance] created.
//
// The redirect URI is passed in and correct, so that [checkRedirectURIs] stays quiet and the only
// thing the caller can make wrong is the secret.
func addProvider(t *testing.T, path, key, kind string, secret *string, redirectURI string) {
	t.Helper()
	db := openCopy(t, path)
	clientID := "1234567890"
	issuer := "https://issuer.example.com"
	_, err := db.Queries().CreateIdentityProvider(t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: "01M0000000000000000000000B", Key: key, Kind: kind,
			DisplayName: key, Enabled: 1, VerifiableSubject: 1,
			Issuer: &issuer, ClientID: &clientID, ClientSecret: secret,
			RedirectUri: &redirectURI, CreatedAt: 1, UpdatedAt: 1,
		})
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func ptr(s string) *string { return &s }
