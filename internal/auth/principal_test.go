package auth_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
)

// everyScope is what a maximally-scoped token carries. A chosen pair would leave the assertion
// resting on which two somebody picked.
func everyScope() []authz.Scope {
	out := make([]authz.Scope, 0, len(authz.Scopes()))
	for _, def := range authz.Scopes() {
		out = append(out, def.Key)
	}
	return out
}

// TestPrincipal_APATCarryingEveryInstanceGrant_ReachesNoneOfThem is the assertion that makes
// widening `instance.owner` safe.
//
// `instance.owner` now expands to the whole instance realm, so the one way that change could do
// harm is a path by which a leaked personal access token reaches the expansion. There is none:
// [authz.EffectiveForToken] takes no instance set at all, which is ADR-0012's capability floor
// stated as a signature.
//
// The Principal is built with `InstanceGrants` FULLY POPULATED, which is a state the authenticator
// never produces — it reads the ledger only on the session path, and the token path says so in a
// comment. That is the point. The field exists on the struct, an expansion could read it, and this
// asserts the token arm does not: wire the grants into [authz.EffectiveForToken], or make
// [auth.Principal.Effective] expand for a PAT, and this goes red without needing a ledger row.
//
// [auth.Principal.Holds] is asked separately, because it is what decides `forbidden` versus
// `insufficient_scope`. A token answering "you hold it, mint a better scope" about an
// instance-realm key would send somebody after a scope no token can ever have.
func TestPrincipal_APATCarryingEveryInstanceGrant_ReachesNoneOfThem(t *testing.T) {
	t.Parallel()

	grants := authz.NewSet(authz.InstancePermissions()...)
	require.Positive(t, grants.Len(), "the instance realm is empty; this test proves nothing")

	for _, role := range authz.Roles() {
		token := auth.Principal{
			Kind:           auth.KindPAT,
			Role:           role,
			Scopes:         everyScope(),
			InstanceGrants: grants,
		}
		session := auth.Principal{
			Kind:           auth.KindSession,
			Role:           role,
			InstanceGrants: authz.NewSet(authz.PermissionInstanceOwner),
		}

		for _, p := range authz.InstancePermissions() {
			// The vacuity guard: a session with the ONE grant reaches every one of them, so this
			// is not passing because nobody can reach them.
			require.True(t, session.Can(p),
				"a %q session granted instance.owner cannot %q; this test is vacuous", role, p)
			require.True(t, session.Holds(p),
				"a %q session granted instance.owner does not HOLD %q, so a refusal would name "+
					"the wrong half", role, p)

			require.False(t, token.Can(p),
				"a %q token with every scope and every instance grant can %q", role, p)
			require.False(t, token.Holds(p),
				"a %q token is reported as HOLDING the instance-realm %q, which would answer "+
					"insufficient_scope for a scope no token can ever carry", role, p)
		}
	}
}

// And the expansion does not leak sideways into the circle realm. `instance.owner` grants the
// instance realm; it must not promote an observer's role, which would be the implication ADR-0012
// rejected an instance role enum to avoid.
func TestPrincipal_AnInstanceOwnerSession_KeepsItsCircleRole(t *testing.T) {
	t.Parallel()

	for _, role := range authz.Roles() {
		p := auth.Principal{
			Kind:           auth.KindSession,
			Role:           role,
			InstanceGrants: authz.NewSet(authz.PermissionInstanceOwner),
		}
		for _, def := range authz.Permissions() {
			if def.Realm != authz.RealmCircle {
				continue
			}
			require.Equal(t, authz.RolePermissions(role).Has(def.Key), p.Can(def.Key),
				"instance.owner changed what a %q holds in its own circle: %q", role, def.Key)
		}
	}
}
