package invite_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

func TestResolve_ALiveInvite_NamesItsCircleAndRole(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer, Role: authz.RoleObserver, MaxUses: 3,
	})
	require.NoError(t, err)

	got, err := invite.Resolve(
		t.Context(), f.store.Queries(), string(minted.Code), f.clock.Now())
	require.NoError(t, err)
	require.Equal(t, invite.KindInvite, got.Kind)
	require.Equal(t, f.circle, got.CircleID)
	require.Equal(t, minted.ID, got.InviteID)
	require.Equal(t, authz.RoleObserver, got.Role)
	require.Equal(t, 3, got.MaxUses)
	require.Equal(t, 0, got.Uses)
}

// The same generosity `Parse` has, at the lookup: every spelling of one code has to reach one row,
// or somebody who typed it in lower case is told their invite does not exist.
func TestResolve_ACodeTypedAnyWay_ReachesTheSameInvite(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	minted, err := f.service.Create(t.Context(), invite.CreateRequest{
		CircleID: f.circle, Actor: f.officer,
	})
	require.NoError(t, err)

	canonical := string(minted.Code)
	payload := canonical[len(invite.Scheme)+1:]
	for _, typed := range []string{
		canonical,
		lower(canonical),
		payload,
		"  " + canonical + "  ",
		removeDashes(canonical),
	} {
		got, resolveErr := invite.Resolve(
			t.Context(), f.store.Queries(), typed, f.clock.Now())
		require.NoError(t, resolveErr, "typed as %q", typed)
		require.Equal(t, minted.ID, got.InviteID, "typed as %q", typed)
	}
}

// An unknown code, an unparseable one and one from another instance all answer identically. Telling
// them apart would hand a guesser a free syntax check on top of the rate limit.
func TestResolve_WhatIsNotALiveInvite_AnswersTheRightCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code func(*fixture) string
		want apierr.Code
	}{
		{
			name: "never issued",
			code: func(*fixture) string { return "TODI-4KQ7M-9XPB2" },
			want: apierr.CodeInviteInvalid,
		},
		{
			name: "not a code at all",
			code: func(*fixture) string { return "hello" },
			want: apierr.CodeInviteInvalid,
		},
		{
			name: "empty",
			code: func(*fixture) string { return "" },
			want: apierr.CodeInviteInvalid,
		},
		{
			name: "revoked",
			code: func(f *fixture) string {
				minted := f.mint(invite.CreateRequest{})
				_, err := f.service.Revoke(f.t.Context(), f.circle, minted.ID)
				require.NoError(f.t, err)
				return string(minted.Code)
			},
			want: apierr.CodeInviteRevoked,
		},
		{
			name: "expired",
			code: func(f *fixture) string {
				minted := f.mint(invite.CreateRequest{TTL: time.Hour})
				f.clock.Advance(2 * time.Hour)
				return string(minted.Code)
			},
			want: apierr.CodeInviteExpired,
		},
		{
			name: "exhausted",
			code: func(f *fixture) string {
				minted := f.mint(invite.CreateRequest{MaxUses: 1})
				f.redeem(minted)
				return string(minted.Code)
			},
			want: apierr.CodeInviteExhausted,
		},
		{
			name: "revoked AND expired reports revoked, which is the fact an officer acted on",
			code: func(f *fixture) string {
				minted := f.mint(invite.CreateRequest{TTL: time.Hour})
				_, err := f.service.Revoke(f.t.Context(), f.circle, minted.ID)
				require.NoError(f.t, err)
				f.clock.Advance(2 * time.Hour)
				return string(minted.Code)
			},
			want: apierr.CodeInviteRevoked,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			_, err := invite.Resolve(
				t.Context(), f.store.Queries(), tt.code(f), f.clock.Now())
			require.Error(t, err)
			require.True(t, apierr.HasCode(err, tt.want), "got %v", err)
		})
	}
}

// Exactly at the expiry instant is expired. `expires_at` is a deadline, not a grace period, and
// testing only "well past" would pass with a comparison that is off by one tick.
func TestResolve_AtTheExpiryInstant_IsAlreadyExpired(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	minted := f.mint(invite.CreateRequest{TTL: time.Hour})

	_, err := invite.Resolve(t.Context(), f.store.Queries(), string(minted.Code),
		minted.ExpiresAt-1)
	require.NoError(t, err, "one microsecond before expiry it is still live")

	_, err = invite.Resolve(t.Context(), f.store.Queries(), string(minted.Code), minted.ExpiresAt)
	require.True(t, apierr.HasCode(err, apierr.CodeInviteExpired), "got %v", err)
}

// A multi-use invite stays live until the LAST use, not until the first. That boundary is where a
// `>=` and a `>` disagree, and where an officer's five-person invite would otherwise admit one.
func TestRedeem_AMultiUseInvite_StaysLiveUntilTheLastUse(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	minted := f.mint(invite.CreateRequest{MaxUses: 3})

	for i := range 3 {
		got, err := invite.Resolve(
			t.Context(), f.store.Queries(), string(minted.Code), f.clock.Now())
		require.NoError(t, err, "use %d", i+1)
		require.Equal(t, i, got.Uses)
		f.redeem(minted)
	}
	_, err := invite.Resolve(
		t.Context(), f.store.Queries(), string(minted.Code), f.clock.Now())
	require.True(t, apierr.HasCode(err, apierr.CodeInviteExhausted), "got %v", err)
}

// mint is the fixture's shorthand for an invite in its own circle.
func (f *fixture) mint(req invite.CreateRequest) invite.Minted {
	f.t.Helper()
	req.CircleID, req.Actor = f.circle, f.officer
	minted, err := f.service.Create(f.t.Context(), req)
	require.NoError(f.t, err)
	return minted
}

// redeem consumes one use, the way `/join` does: inside a transaction, next to a membership.
func (f *fixture) redeem(minted invite.Minted) {
	f.t.Helper()
	member := f.seedMember(f.circle, "member")
	require.NoError(f.t, f.store.InTx(f.t.Context(), func(ctx context.Context, q *sqlitegen.Queries) error {
		resolved, err := invite.Resolve(ctx, q, string(minted.Code), f.clock.Now())
		if err != nil {
			return err
		}
		return invite.Redeem(ctx, q, resolved, member, "", f.clock.Now(), f.ids)
	}))
}

func lower(s string) string { return strings.ToLower(s) }

func removeDashes(s string) string { return strings.ReplaceAll(s, "-", "") }
