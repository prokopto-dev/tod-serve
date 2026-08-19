package invite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/invite"
)

func TestCreate_ASessionMintedInvite_CarriesWhatWasAskedFor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer,
		Role: authz.RoleOfficer, MaxUses: 5, TTL: 72 * time.Hour, Note: "raid night",
	})
	require.NoError(t, err)
	require.Equal(t, string(authz.RoleOfficer), minted.Role)
	require.Equal(t, 5, minted.MaxUses)
	require.Equal(t, 0, minted.Uses)
	require.Equal(t, "raid night", minted.Note)
	require.Empty(t, minted.CappedBy, "nothing narrowed this request, so nothing should say it did")
	require.Equal(t, fixtureNow.Add(72*time.Hour), minted.ExpiresAt)
	require.True(t, minted.Live)

	// The code is returned once and the row holds only its hash and its display prefix.
	require.NotEmpty(t, minted.Code)
	require.Equal(t, minted.Code.Prefix(), minted.CodePrefix)
	listed, err := f.service.List(t.Context(), f.circle)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, minted.CodePrefix, listed[0].CodePrefix)
}

func TestCreate_Defaults_AreOneUseAndSevenDays(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer,
	})
	require.NoError(t, err)
	require.Equal(t, string(authz.RoleMember), minted.Role)
	require.Equal(t, invite.DefaultMaxUses, minted.MaxUses)
	require.Equal(t, fixtureNow.Add(invite.DefaultTTL), minted.ExpiresAt)
}

// Canonical §6: `invite.create` is deliberately outside the capability floor while `token.mint` is
// inside it, and that trade is ONLY defensible because a PAT-minted invite is hard-narrowed
// whatever the request asks for. All three axes, and the response says so.
func TestCreate_ByAPAT_IsClampedOnEveryAxisAndSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer, MintedByPAT: true,
		Role: authz.RoleOfficer, MaxUses: 50, TTL: invite.MaxTTL,
	})
	require.NoError(t, err)
	require.Equal(t, 1, minted.MaxUses, "a token may not mint a multi-use invite")
	require.Equal(t, fixtureNow.Add(24*time.Hour), minted.ExpiresAt,
		"a token may not mint an invite that outlives a day")
	require.Equal(t, string(authz.RoleMember), minted.Role,
		"a token may not mint an invite above member")
	require.Equal(t, invite.CappedByPAT, minted.CappedBy,
		"a clamped request must say what clamped it; never hide a row silently")
	require.Equal(t, "pat", minted.MintedByKind)
}

// Each axis on its own, so a clamp that stopped working on one of them cannot hide behind the
// other two.
func TestCreate_ByAPAT_EachAxisIsClampedIndependently(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		request invite.CreateRequest
		check   func(*testing.T, invite.Minted)
	}{
		{
			name:    "uses alone",
			request: invite.CreateRequest{MaxUses: 10, TTL: time.Hour, Role: authz.RoleMember},
			check: func(t *testing.T, got invite.Minted) {
				require.Equal(t, 1, got.MaxUses)
				require.Equal(t, fixtureNow.Add(time.Hour), got.ExpiresAt)
			},
		},
		{
			name:    "time alone",
			request: invite.CreateRequest{MaxUses: 1, TTL: 48 * time.Hour, Role: authz.RoleMember},
			check: func(t *testing.T, got invite.Minted) {
				require.Equal(t, fixtureNow.Add(24*time.Hour), got.ExpiresAt)
				require.Equal(t, 1, got.MaxUses)
			},
		},
		{
			name:    "role alone",
			request: invite.CreateRequest{MaxUses: 1, TTL: time.Hour, Role: authz.RoleOfficer},
			check: func(t *testing.T, got invite.Minted) {
				require.Equal(t, string(authz.RoleMember), got.Role)
			},
		},
		{
			name:    "an observer invite is below the cap and is left alone",
			request: invite.CreateRequest{MaxUses: 1, TTL: time.Hour, Role: authz.RoleObserver},
			check: func(t *testing.T, got invite.Minted) {
				require.Equal(t, string(authz.RoleObserver), got.Role)
				require.Empty(t, got.CappedBy)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			req := tt.request
			req.CircleID, req.Actor, req.MintedByPAT = f.circle, f.officer, true
			minted, err := f.service.Create(t.Context(), req)
			require.NoError(t, err)
			tt.check(t, minted)
		})
	}
}

// The middle of the range for the boundary itself: exactly at the cap is NOT clamped, and one over
// it is. A suite that only tried "far above" would pass with an off-by-one that clamps every
// legitimate request.
func TestCreate_ByAPAT_ExactlyAtTheCap_IsNotReportedAsClamped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	at, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer, MintedByPAT: true,
		Role: authz.RoleMember, MaxUses: 1, TTL: 24 * time.Hour,
	})
	require.NoError(t, err)
	require.Empty(t, at.CappedBy)
	require.Equal(t, fixtureNow.Add(24*time.Hour), at.ExpiresAt)

	over, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer, MintedByPAT: true,
		Role: authz.RoleMember, MaxUses: 1, TTL: 24*time.Hour + time.Second,
	})
	require.NoError(t, err)
	require.Equal(t, invite.CappedByPAT, over.CappedBy)
	require.Equal(t, fixtureNow.Add(24*time.Hour), over.ExpiresAt)
}

// `local` has no credential to re-present, so `POST /sessions` cannot work for it and every lost
// token becomes a new invite. One use is the mitigation, and it applies to any invite into a
// circle that accepts an unverifiable provider — which is exactly the set `local` can redeem.
func TestCreate_IntoACircleAcceptingAWeakProvider_IsOneUseAndSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.acceptProvider(f.circle)

	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer, MaxUses: 25,
		WeakProviderAccepted: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, minted.MaxUses)
	require.Equal(t, invite.CappedByWeakProvider, minted.CappedBy)
}

// When both clamps apply the response names the PAT, because the PAT clamp is at least as strong
// on every axis. Naming the weaker one would tell a bot author that raising `max_uses` would work
// if only the circle dropped `local`.
func TestCreate_BothClamps_ReportTheStrongerOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer, MintedByPAT: true,
		MaxUses: 25, WeakProviderAccepted: true,
	})
	require.NoError(t, err)
	require.Equal(t, invite.CappedByPAT, minted.CappedBy)
}

// `CHECK (role <> 'owner')` makes an owner invite unrepresentable, so there is no value to clamp to
// that would be what the caller asked for. It is refused, and the message points at the one path
// that does make an owner.
func TestCreate_AnOwnerInvite_IsRefusedForEverybody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, byPAT := range []bool{false, true} {
		_, err := f.service.Create(t.Context(), invite.CreateRequest{
			CircleID: f.circle, Actor: f.officer, MintedByPAT: byPAT, Role: authz.RoleOwner,
		})
		require.Error(t, err)
		require.True(t, apierr.HasCode(err, apierr.CodeValidationFailed))
	}
}

func TestCreate_ValuesOutsideTheBounds_AreRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		request invite.CreateRequest
	}{
		{"zero is the default, but negative is not", invite.CreateRequest{MaxUses: -1}},
		{"above the ceiling", invite.CreateRequest{MaxUses: invite.MaxUsesCeiling + 1}},
		{"a negative lifetime", invite.CreateRequest{TTL: -time.Second}},
		{"longer than the maximum", invite.CreateRequest{TTL: invite.MaxTTL + time.Second}},
		{"a role that is not one", invite.CreateRequest{Role: authz.Role("archmage")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			req := tt.request
			req.CircleID, req.Actor = f.circle, f.officer
			_, err := f.service.Create(t.Context(), req)
			require.Error(t, err)
			require.True(t, apierr.HasCode(err, apierr.CodeValidationFailed), "got %v", err)
		})
	}
}

// Exactly at each bound is accepted. Testing only "over" would pass with a comparison that refuses
// the largest legitimate request.
func TestCreate_ExactlyAtEachBound_IsAccepted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer,
		MaxUses: invite.MaxUsesCeiling, TTL: invite.MaxTTL,
	})
	require.NoError(t, err)
	require.Equal(t, invite.MaxUsesCeiling, minted.MaxUses)
	require.Equal(t, fixtureNow.Add(invite.MaxTTL), minted.ExpiresAt)
}

func TestRevoke_ALiveInvite_StopsResolving(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer,
	})
	require.NoError(t, err)

	revoked, err := f.service.Revoke(t.Context(), f.circle, minted.ID)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
	require.False(t, revoked.Live)

	_, err = invite.Resolve(
		t.Context(), f.store.Queries(), string(minted.Code), f.clock.Now())
	require.True(t, apierr.HasCode(err, apierr.CodeInviteRevoked), "got %v", err)

	// Revoking twice is 404 rather than 409: the query names `revoked_at IS NULL`, and a second
	// read whose only purpose is to say what the list already shows is a read nobody needs.
	_, err = f.service.Revoke(t.Context(), f.circle, minted.ID)
	require.True(t, apierr.HasCode(err, apierr.CodeNotFound), "got %v", err)
}

func TestCountLive_CountsOnlyWhatCanStillBeRedeemed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	live, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer,
	})
	require.NoError(t, err)
	expiring, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer, TTL: time.Hour,
	})
	require.NoError(t, err)
	revoked, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer,
	})
	require.NoError(t, err)
	_, err = f.service.Revoke(t.Context(), f.circle, revoked.ID)
	require.NoError(t, err)

	count, err := f.service.CountLive(t.Context(), f.circle)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Expiry needs no sweep to take effect: the count is a question about now.
	f.clock.Advance(2 * time.Hour)
	count, err = f.service.CountLive(t.Context(), f.circle)
	require.NoError(t, err)
	require.Equal(t, 1, count, "the invite that expired is no longer live")
	_ = live
	_ = expiring
}

// An invite belongs to one circle, and a lookup by (circle, id) is what stops circle B revoking
// circle A's invite even with a valid id.
func TestGetAndRevoke_AnotherCirclesInvite_IsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	other := f.seedCircle("Rival Green", "green")

	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer,
	})
	require.NoError(t, err)

	_, err = f.service.Get(t.Context(), other, minted.ID)
	require.True(t, apierr.HasCode(err, apierr.CodeNotFound), "got %v", err)
	_, err = f.service.Revoke(t.Context(), other, minted.ID)
	require.True(t, apierr.HasCode(err, apierr.CodeNotFound), "got %v", err)
}
