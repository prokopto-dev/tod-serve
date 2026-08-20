package audit_test

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

const fixtureNow = core.Micros(1_755_483_247_000_000)

// The chain is what makes a REMOVAL visible, on top of the trigger that makes one impossible: every
// entry carries the previous entry's hash, so a row deleted by something that bypassed the trigger
// breaks everything after it.
func TestAppend_EachEntry_ChainsToThePreviousOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	first := f.append(audit.Entry{
		CircleID: f.circle, Action: audit.ActionCircleCreated, EntityType: audit.EntityCircle,
		EntityID: f.circle.String(),
	})
	require.Nil(t, first.PrevHash, "the first entry in a circle has nothing to chain to")
	require.NotEmpty(t, first.Hash)

	second := f.append(audit.Entry{
		CircleID: f.circle, Action: audit.ActionMemberJoined,
		EntityType: audit.EntityMembership, EntityID: f.member.String(),
	})
	require.Equal(t, first.Hash, second.PrevHash)
	require.NotEqual(t, first.Hash, second.Hash)
}

// The chain covers every field, not only the id and the timestamp. A chain over less would let the
// action, the actor or the detail be rewritten without breaking it — and the trigger that forbids
// the rewrite is a different mechanism guarding a different failure.
func TestAppend_TheHash_CoversEveryField(t *testing.T) {
	t.Parallel()
	base := audit.Entry{
		Action: audit.ActionMemberRevoked, EntityType: audit.EntityMembership,
		Detail: map[string]any{audit.DetailRole: "member"},
	}
	mutations := []struct {
		name   string
		mutate func(*audit.Entry)
	}{
		{"the action", func(e *audit.Entry) { e.Action = audit.ActionMemberReinstated }},
		{"the entity type", func(e *audit.Entry) { e.EntityType = audit.EntityInvite }},
		{"the detail", func(e *audit.Entry) {
			e.Detail = map[string]any{audit.DetailRole: "officer"}
		}},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			entry := base
			entry.CircleID, entry.Actor, entry.EntityID = f.circle, f.member, f.member.String()
			unchanged := f.append(entry)

			other := newFixture(t)
			mutated := base
			mutated.CircleID, mutated.Actor = other.circle, other.member
			mutated.EntityID = other.member.String()
			tt.mutate(&mutated)
			require.NotEqual(t, unchanged.Hash, other.append(mutated).Hash,
				"changing %s did not change the hash", tt.name)
		})
	}
}

// The fields are length-prefixed, so two entries whose fields differ only in where one string ends
// and the next begins cannot collide — which a plain concatenation would allow.
func TestAppend_FieldsAreLengthPrefixed_SoTwoEntriesCannotCollide(t *testing.T) {
	t.Parallel()
	first := newFixture(t)
	second := newFixture(t)

	a := first.append(audit.Entry{
		CircleID: first.circle, Action: "member.re", EntityType: "instated",
		EntityID: first.member.String(),
	})
	b := second.append(audit.Entry{
		CircleID: second.circle, Action: "member.rein", EntityType: "stated",
		EntityID: second.member.String(),
	})
	require.NotEqual(t, a.Hash, b.Hash)
}

// Two circles keep two chains. An entry in one must not chain to an entry in the other, or reading
// a circle's audit log would depend on what happened in somebody else's.
func TestAppend_TwoCircles_KeepSeparateChains(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	other := f.seedCircle("Rival Green", schemaenum.ServerGreen)

	mine := f.append(audit.Entry{
		CircleID: f.circle, Action: audit.ActionCircleCreated, EntityType: audit.EntityCircle,
	})
	theirs := f.append(audit.Entry{
		CircleID: other, Action: audit.ActionCircleCreated, EntityType: audit.EntityCircle,
	})
	require.Nil(t, theirs.PrevHash, "the other circle's first entry chained to ours")
	require.NotEqual(t, mine.Hash, theirs.Hash)
}

// An entry the audit log cannot describe is refused rather than written blank: a row saying nothing
// happened to nothing is worse than no row, because it is counted.
func TestAppend_AnEntryWithNoActionOrEntity_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, entry := range []audit.Entry{
		{CircleID: f.circle, EntityType: audit.EntityCircle},
		{CircleID: f.circle, Action: audit.ActionCircleCreated},
	} {
		err := f.store.InTx(t.Context(), func(ctx context.Context, q *sqlitegen.Queries) error {
			return audit.Append(ctx, q, f.ids, fixtureNow, entry)
		})
		require.Error(t, err)
	}
}

// `audit_log` carries a UNIQUE index on `hash`, so the same thing happening twice must still
// produce two distinct hashes or the second occurrence is refused by the database.
//
// It does, because the chain covers the entry's own id and its predecessor's hash — both of which
// move. That is worth an assertion rather than an argument: revoking two members for the same
// reason on the same tick is an ordinary Tuesday, and a log that refused the second one would fail
// the transaction the revocation is in.
func TestAppend_TheSameThingTwice_ProducesTwoDistinctHashes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	entry := audit.Entry{
		CircleID: f.circle, Actor: f.member, Action: audit.ActionMemberRevoked,
		EntityType: audit.EntityMembership, EntityID: f.member.String(),
		Detail: map[string]any{audit.DetailRole: "member"},
	}

	first := f.append(entry)
	second := f.append(entry)
	require.NotEqual(t, first.Hash, second.Hash)
	require.Equal(t, first.Hash, second.PrevHash)
	require.NotEqual(t, first.ID, second.ID)
}

type fixture struct {
	t      *testing.T
	store  *store.DB
	ids    *core.Generator
	circle core.CircleID
	member core.MembershipID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tod.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(t.Context()))

	f := &fixture{t: t, store: db, ids: core.NewGenerator(rand.Reader)}
	f.circle = f.seedCircle("Riot Blue", schemaenum.ServerBlue)
	f.member = f.seedMember(f.circle)
	return f
}

func (f *fixture) seedCircle(name, server string) core.CircleID {
	f.t.Helper()
	id, err := core.NewID[core.Circle](f.ids, fixtureNow)
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateCircle(f.t.Context(), sqlitegen.CreateCircleParams{
		CircleID: id.String(), Name: name, NameNorm: core.Normalise(name),
		Server: server, Timezone: "UTC", MinReportersToSupersede: 1,
		RevokeInvalidatesInvites: 1, State: schemaenum.CircleStateActive,
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return id
}

func (f *fixture) seedMember(circleID core.CircleID) core.MembershipID {
	f.t.Helper()
	providerID, err := core.NewID[core.IdentityProvider](f.ids, fixtureNow)
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentityProvider(f.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: providerID.String(), Key: "local", Kind: schemaenum.IdentityProviderKindLocal,
			DisplayName: "This server", Enabled: 1, VerifiableSubject: 0,
			CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
	require.NoError(f.t, err)

	identityID, err := core.NewID[core.Identity](f.ids, fixtureNow)
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentity(f.t.Context(), sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: providerID.String(), Subject: identityID.String(),
		DisplayName: "Tankguy", CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)

	id, err := core.NewID[core.Membership](f.ids, fixtureNow)
	require.NoError(f.t, err)
	subject := identityID.String()
	_, err = f.store.Queries().CreateMembership(f.t.Context(), sqlitegen.CreateMembershipParams{
		ID: id.String(), CircleID: circleID.String(), IdentityID: &subject,
		Kind: schemaenum.MembershipKindHuman, DisplayName: "Tankguy",
		DisplayNameNorm: "tankguy", Role: schemaenum.MembershipRoleOwner,
		JoinedAt: int64(fixtureNow), CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return id
}

// append writes one entry and reads back the row, which is where the chain actually lives.
func (f *fixture) append(entry audit.Entry) sqlitegen.AuditLog {
	f.t.Helper()
	require.NoError(f.t, f.store.InTx(f.t.Context(),
		func(ctx context.Context, q *sqlitegen.Queries) error {
			return audit.Append(ctx, q, f.ids, fixtureNow, entry)
		}))
	row, err := f.store.Queries().GetLatestAuditLogEntry(f.t.Context(), entry.CircleID.String())
	require.NoError(f.t, err)
	return row
}
