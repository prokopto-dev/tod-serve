package instancegrant_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
)

// grant is the shorthand every test below uses; it fails rather than returning an error, because a
// setup step that did not happen makes the assertion after it meaningless.
func (f *fixture) grant(id core.IdentityID, p authz.Permission) instancegrant.Grant {
	f.t.Helper()
	out, err := f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
		IdentityID: id, Permission: p, Decision: instancegrant.DecisionGranted,
	})
	require.NoError(f.t, err)
	return out
}

func (f *fixture) revoke(id core.IdentityID, p authz.Permission) instancegrant.Grant {
	f.t.Helper()
	out, err := f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
		IdentityID: id, Permission: p, Decision: instancegrant.DecisionRevoked,
	})
	require.NoError(f.t, err)
	return out
}

func (f *fixture) effective(id core.IdentityID) authz.Set {
	f.t.Helper()
	set, err := f.service.Effective(f.t.Context(), id)
	require.NoError(f.t, err)
	return set
}

func TestEffective_EveryInstancePermission_IsGrantableAndRevocable(t *testing.T) {
	t.Parallel()
	// Every key in the realm, not a chosen one: the failure this catches is a permission the
	// column's CHECK refuses, which would make it grantable in the catalogue and not in practice.
	for _, permission := range authz.InstancePermissions() {
		t.Run(string(permission), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)

			require.False(t, f.effective(f.alice).Has(permission))
			f.grant(f.alice, permission)
			require.True(t, f.effective(f.alice).Has(permission))
			// A grant is about one identity. Nobody else acquires it.
			require.False(t, f.effective(f.bob).Has(permission))

			f.revoke(f.alice, permission)
			require.False(t, f.effective(f.alice).Has(permission))
		})
	}
}

// The middle of the range, not just absent-and-exact: grant, revoke, grant again. The second grant
// is the case a capability list with a DELETE would get right by accident and a ledger has to get
// right on purpose, because by then the pair has three rows and only the last one counts.
func TestDecide_GrantRevokeGrant_LeavesTheLastDecisionInForce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := authz.PermissionOpsRead

	first := f.grant(f.alice, p)
	require.True(t, first.Supersedes.IsZero(), "the first decision supersedes nothing")

	revoked := f.revoke(f.alice, p)
	require.Equal(t, first.ID, revoked.Supersedes)
	require.False(t, f.effective(f.alice).Has(p))

	again := f.grant(f.alice, p)
	require.Equal(t, revoked.ID, again.Supersedes)
	require.True(t, f.effective(f.alice).Has(p))

	// Three rows, all of them still there. The revocation is as durable as the grants around it.
	history, err := f.service.History(f.t.Context())
	require.NoError(t, err)
	require.Len(t, history, 3)
	require.Equal(t, []instancegrant.Decision{
		instancegrant.DecisionGranted,
		instancegrant.DecisionRevoked,
		instancegrant.DecisionGranted,
	}, []instancegrant.Decision{history[0].Decision, history[1].Decision, history[2].Decision})

	// And exactly one of them is current, which is the property the two unique indexes buy.
	current, err := f.service.Current(f.t.Context())
	require.NoError(t, err)
	require.Len(t, current, 1)
	require.Equal(t, again.ID, current[0].ID)
}

// Granting what is already granted, and revoking what was never granted, are both refused. An
// audit record whose rows include ones where nothing happened is one somebody has to filter before
// reading, and the filtering is where a real revocation gets missed.
func TestDecide_ADecisionThatChangesNothing_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := authz.PermissionCatalogueManage

	_, err := f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
		IdentityID: f.alice, Permission: p, Decision: instancegrant.DecisionRevoked,
	})
	require.ErrorIs(t, err, instancegrant.ErrNoChange)

	f.grant(f.alice, p)
	_, err = f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
		IdentityID: f.alice, Permission: p, Decision: instancegrant.DecisionGranted,
	})
	require.ErrorIs(t, err, instancegrant.ErrNoChange)

	f.revoke(f.alice, p)
	_, err = f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
		IdentityID: f.alice, Permission: p, Decision: instancegrant.DecisionRevoked,
	})
	require.ErrorIs(t, err, instancegrant.ErrNoChange)

	// Two rows, not four. A refused decision leaves nothing behind.
	history, err := f.service.History(f.t.Context())
	require.NoError(t, err)
	require.Len(t, history, 2)
}

// A circle-realm key is refused by this package AND unrepresentable in the column, which is two
// mechanisms for one rule on purpose: the CHECK protects the database and the service protects the
// caller, who gets a sentence rather than a constraint name. The database half is
// TestInstanceGrant_ACircleRealmPermission_IsRefusedByTheSchema in internal/store, where raw SQL
// lives — without it this test would prove only that the Go branch exists, which is the shape of
// gate the invariants document calls a tautology.
func TestDecide_ACircleRealmPermission_IsRefusedByBothMechanisms(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	for _, def := range authz.Permissions() {
		if def.Realm == authz.RealmInstance {
			continue
		}
		_, err := f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
			IdentityID: f.alice, Permission: def.Key,
			Decision: instancegrant.DecisionGranted,
		})
		require.ErrorIs(t, err, authz.ErrUnknownPermission,
			"%q is circle-realm and the ledger accepted it", def.Key)
	}

	// And a permission outside the catalogue entirely.
	_, err := f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
		IdentityID: f.alice, Permission: authz.Permission("instance.destroy"),
		Decision: instancegrant.DecisionGranted,
	})
	require.ErrorIs(t, err, authz.ErrUnknownPermission)
}

func TestDecide_AnIdentityThatDoesNotExist_IsNamedRatherThanAForeignKey(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
		IdentityID: f.newIdentityID(), Permission: authz.PermissionOpsRead,
		Decision: instancegrant.DecisionGranted,
	})
	require.ErrorIs(t, err, instancegrant.ErrUnknownIdentity)

	_, err = f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
		Permission: authz.PermissionOpsRead, Decision: instancegrant.DecisionGranted,
	})
	require.ErrorIs(t, err, instancegrant.ErrUnknownIdentity)
}

// A service membership has no identity, so asking what it holds at the instance level is a normal
// question with a normal answer. Returning an error would make every request by a bot a failure.
func TestEffective_TheZeroIdentity_HoldsNothingAndIsNotAnError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	set, err := f.service.Effective(f.t.Context(), core.IdentityID{})
	require.NoError(t, err)
	require.Zero(t, set.Len())
}

// A decision written at the console records no decider, which reads as "the operator at the
// console" and is a different fact from a person having decided it.
func TestDecide_TheDecider_IsRecordedOrIsTheConsole(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	console := f.grant(f.alice, authz.PermissionInstanceOwner)
	require.True(t, console.ByConsole())
	require.True(t, console.DecidedBy.IsZero())

	byPerson, err := f.service.Decide(f.t.Context(), instancegrant.DecideRequest{
		IdentityID: f.bob, Permission: authz.PermissionOpsRead,
		Decision: instancegrant.DecisionGranted, DecidedBy: f.alice,
		Reason: "on-call dashboard",
	})
	require.NoError(t, err)
	require.False(t, byPerson.ByConsole())
	require.Equal(t, f.alice, byPerson.DecidedBy)
	require.Equal(t, "on-call dashboard", byPerson.Reason)
}

// Current returns one row per (identity, permission) with a decision, revoked ones included, and
// DecisionsFor narrows that to one identity. Nothing is hidden: an operator asking who holds
// `ops.read` is also asking who used to.
func TestCurrent_TheListing_ShowsEveryPairWithADecision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.grant(f.alice, authz.PermissionInstanceOwner)
	f.grant(f.alice, authz.PermissionOpsRead)
	f.revoke(f.alice, authz.PermissionOpsRead)
	f.grant(f.bob, authz.PermissionCatalogueManage)

	current, err := f.service.Current(f.t.Context())
	require.NoError(t, err)
	require.Len(t, current, 3)

	mine, err := f.service.DecisionsFor(f.t.Context(), f.alice)
	require.NoError(t, err)
	require.Len(t, mine, 2)
	got := []authz.Permission{mine[0].Permission, mine[1].Permission}
	if diff := cmp.Diff([]authz.Permission{
		authz.PermissionInstanceOwner, authz.PermissionOpsRead,
	}, got); diff != "" {
		t.Errorf("decisions for one identity (-want +got):\n%s", diff)
	}
	// And only the granted one is effective.
	require.True(t, f.effective(f.alice).Has(authz.PermissionInstanceOwner))
	require.False(t, f.effective(f.alice).Has(authz.PermissionOpsRead))
}

// fixedEntropy yields the same byte forever, so a generator built over it mints an id with a known
// entropy tail. It is what lets this file put two ids in a CHOSEN order inside one millisecond.
type fixedEntropy byte

func (f fixedEntropy) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(f)
	}
	return len(p), nil
}

// TestDecide_TwoGeneratorsInOneMillisecond_CanStillAppend is the regression test for a LOCKOUT.
//
// `tod-serve instance grant` builds a fresh `core.Generator` per invocation, and a ULID is
// monotonic only within one generator: two invocations inside the same millisecond draw random
// entropy, so the later row can sort BELOW the earlier one. The chain tail used to be
// `ORDER BY id DESC LIMIT 1`, which then returned the earlier row forever — the next append reused
// a `prev_hash` its successor had already claimed, `ux_instance_grant_chain` refused it, and the
// ledger stopped accepting decisions on an instance nobody could then administer.
//
// The ids here are forced into the wrong order rather than raced for, because a race reproduces it
// only sometimes and proves nothing on the run where it does not.
func TestDecide_TwoGeneratorsInOneMillisecond_CanStillAppend(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// The first console invocation mints a HIGH id, the second a LOW one, at the same instant.
	first := f.serviceWithEntropy(fixedEntropy(0xFF))
	second := f.serviceWithEntropy(fixedEntropy(0x00))

	a, err := first.Decide(t.Context(), instancegrant.DecideRequest{
		IdentityID: f.alice, Permission: authz.PermissionOpsRead,
		Decision: instancegrant.DecisionGranted,
	})
	require.NoError(t, err)
	b, err := second.Decide(t.Context(), instancegrant.DecideRequest{
		IdentityID: f.bob, Permission: authz.PermissionOpsRead,
		Decision: instancegrant.DecisionGranted,
	})
	require.NoError(t, err)

	require.Less(t, b.ID.String(), a.ID.String(),
		"this test only exercises the bug when the SECOND row sorts below the first")
	require.Equal(t, a.DecidedAt, b.DecidedAt, "both rows must share one instant")

	// The third append is the one that used to be impossible.
	c, err := second.Decide(t.Context(), instancegrant.DecideRequest{
		IdentityID: f.alice, Permission: authz.PermissionCatalogueManage,
		Decision: instancegrant.DecisionGranted,
	})
	require.NoError(t, err, "the ledger can no longer be appended to")

	// And the chain is right: the third row names the SECOND row's hash, which is the one that was
	// actually written last, not the one with the greatest id.
	rows := f.rawByID(map[string]bool{a.ID.String(): true, b.ID.String(): true, c.ID.String(): true})
	require.Nil(t, rows[a.ID.String()].prevHash, "the first decision has no predecessor")
	require.Equal(t, rows[a.ID.String()].hash, rows[b.ID.String()].prevHash)
	require.Equal(t, rows[b.ID.String()].hash, rows[c.ID.String()].prevHash)

	// Every decision is still readable and in force, which is the point of being able to append.
	require.True(t, f.effective(f.alice).Has(authz.PermissionOpsRead))
	require.True(t, f.effective(f.alice).Has(authz.PermissionCatalogueManage))
	require.True(t, f.effective(f.bob).Has(authz.PermissionOpsRead))

	// A fourth append, from a THIRD generator, still works — the fix is not "one more row".
	third := f.serviceWithEntropy(fixedEntropy(0x7F))
	_, err = third.Decide(t.Context(), instancegrant.DecideRequest{
		IdentityID: f.bob, Permission: authz.PermissionOpsRead,
		Decision: instancegrant.DecisionRevoked,
	})
	require.NoError(t, err)
	require.False(t, f.effective(f.bob).Has(authz.PermissionOpsRead))
}

// rawRow is the ledger as the database holds it. It reads the columns directly rather than through
// the service, so a bug in the conversion cannot hide one in the chain.
type rawRow struct {
	hash     []byte
	prevHash []byte
}

// rawByID returns the named rows keyed by id, so an assertion about the chain does not depend on
// the order a listing happens to return them in — which is the very thing this file's regression
// test is about.
func (f *fixture) rawByID(want map[string]bool) map[string]rawRow {
	f.t.Helper()
	rows, err := f.store.Queries().ListInstanceGrantHistory(f.t.Context())
	require.NoError(f.t, err)
	out := map[string]rawRow{}
	for _, row := range rows {
		if want[row.ID] {
			out[row.ID] = rawRow{hash: row.Hash, prevHash: row.PrevHash}
		}
	}
	require.Len(f.t, out, len(want), "not every named row came back")
	return out
}
