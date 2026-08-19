package invite_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// An owner grant is not an invite, and this is the constraint that makes it a separate thing:
// `CHECK (role <> 'owner')` refuses an owner invite at the database, so a circle's first owner
// cannot come through the invite path at all.
func TestGrant_AnOwnerInvite_IsUnrepresentableAtTheDatabase(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, err := invite.Mint(rand.Reader)
	require.NoError(t, err)

	_, err = f.store.Queries().CreateInvite(t.Context(), sqlitegen.CreateInviteParams{
		ID: f.newID("membership"), CircleID: f.circle.String(),
		CodeHash: invite.Hash(code), CodePrefix: code.Prefix(),
		Role: string(authz.RoleOwner), MaxUses: 1,
		ExpiresAt: int64(fixtureNow.Add(time.Hour)), CreatedByMembershipID: f.officer.String(),
		MintedByKind: "session", Note: "",
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.Error(t, err, "the database accepted an owner invite; ck_invite_role_is_not_owner is gone")
}

func TestGrant_Minted_ResolvesAsAnOwnerCode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, expiresAt, err := f.service.MintOwnerGrant(t.Context(), f.circle)
	require.NoError(t, err)
	require.Equal(t, fixtureNow+invite.DefaultGrantTTL, expiresAt)

	got, err := invite.Resolve(t.Context(), f.store.Queries(), string(code), f.clock.Now())
	require.NoError(t, err)
	require.Equal(t, invite.KindOwnerGrant, got.Kind)
	require.Equal(t, f.circle, got.CircleID)
	require.Equal(t, authz.RoleOwner, got.Role)
	require.Equal(t, 1, got.MaxUses)
	require.True(t, got.InviteID.IsZero(), "a grant is not an invite and has no invite id")
}

// It is the ONLY way a circle gets its first owner, so it must work — and it must be single-use,
// because the code is printed on a terminal an operator may leave open.
func TestGrant_ASecondRedemption_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, _, err := f.service.MintOwnerGrant(t.Context(), f.circle)
	require.NoError(t, err)

	require.NoError(t, f.consume(code))

	// The second attempt does not even resolve: a consumed grant reads as exhausted, which is the
	// same fact `uses >= max_uses` records for an invite.
	_, err = invite.Resolve(t.Context(), f.store.Queries(), string(code), f.clock.Now())
	require.True(t, apierr.HasCode(err, apierr.CodeInviteExhausted), "got %v", err)

	// And consuming it directly is refused by the compare-and-swap rather than by the read above,
	// so a caller that skipped the resolve still cannot make a second owner.
	require.ErrorIs(t, f.consume(code), invite.ErrGrantConsumed)
}

func TestGrant_AfterItsTTL_IsExpired(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, expiresAt, err := f.service.MintOwnerGrant(t.Context(), f.circle)
	require.NoError(t, err)

	_, err = invite.Resolve(t.Context(), f.store.Queries(), string(code), expiresAt-1)
	require.NoError(t, err, "one microsecond before expiry it is still live")

	_, err = invite.Resolve(t.Context(), f.store.Queries(), string(code), expiresAt)
	require.True(t, apierr.HasCode(err, apierr.CodeInviteExpired), "got %v", err)
}

// Two grants for two circles do not collide, and one does not resolve to the other's circle.
func TestGrant_TwoCircles_HaveIndependentGrants(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	other := f.seedCircle("Rival Green", "green")

	first, _, err := f.service.MintOwnerGrant(t.Context(), f.circle)
	require.NoError(t, err)
	second, _, err := f.service.MintOwnerGrant(t.Context(), other)
	require.NoError(t, err)
	require.NotEqual(t, first, second)

	got, err := invite.Resolve(t.Context(), f.store.Queries(), string(second), f.clock.Now())
	require.NoError(t, err)
	require.Equal(t, other, got.CircleID)

	require.NoError(t, f.consume(second))
	// Consuming one leaves the other alone: they are separate rows keyed by separate hashes.
	got, err = invite.Resolve(t.Context(), f.store.Queries(), string(first), f.clock.Now())
	require.NoError(t, err)
	require.Equal(t, f.circle, got.CircleID)
}

// A grant is stored by the HASH of its code. A database read must not yield a working credential —
// the same rule `invite.code_hash` and `api_token.token_hash` obey.
func TestGrant_TheCode_IsNeverStored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, _, err := f.service.MintOwnerGrant(t.Context(), f.circle)
	require.NoError(t, err)

	// The key is the hash's hex and the value is the grant's JSON. Neither contains the code, and
	// asserting that over the whole row is stronger than asserting it over the fields we expect.
	row, err := f.store.Queries().GetMeta(t.Context(), "owner_grant/"+hex.EncodeToString(invite.Hash(code)))
	require.NoError(t, err)
	require.NotContains(t, row.Key, string(code))
	require.NotContains(t, row.Value, string(code))
}

func (f *fixture) consume(code invite.Code) error {
	f.t.Helper()
	return f.store.InTx(f.t.Context(), func(ctx context.Context, q *sqlitegen.Queries) error {
		_, err := invite.ConsumeGrant(ctx, q, code, f.clock.Now())
		return err
	})
}
