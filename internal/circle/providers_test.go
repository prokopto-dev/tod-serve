package circle_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// Accepting an unverifiable provider needs an explicit acknowledgement. The failure it exists for
// is not technical: an officer revokes a leaker, the leaker redeems another invite as "Tanky", and
// is reading the same ToDs a minute later while the officers believe the problem is handled.
func TestSetProviders_AcceptingAWeakProvider_NeedsAnAcknowledgement(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	f.provider("local", schemaenum.IdentityProviderKindLocal, true)
	view := f.create("Riot Blue", schemaenum.ServerBlue)

	_, err := f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
		Providers: []circle.AcceptedProvider{{Key: "discord"}, {Key: "local"}},
	})
	require.True(t, apierr.HasCode(err, apierr.CodeAcknowledgementRequired), "got %v", err)

	// Refusing left the circle alone rather than half-applying the request.
	after, err := f.service.Get(t.Context(), view.ID)
	require.NoError(t, err)
	require.Len(t, after.AcceptedProviders, 1)
	require.Equal(t, "discord", after.AcceptedProviders[0].Key)

	_, err = f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
		Providers:                 []circle.AcceptedProvider{{Key: "discord"}, {Key: "local"}},
		AcknowledgeWeakRevocation: true,
	})
	require.NoError(t, err)
}

// Removing a provider stops NEW joins through it and revokes NOBODY. Mass-revoke on removal is a
// footgun that eventually deletes a guild's whole roster with one click.
func TestSetProviders_RemovingOne_RevokesNoMembership(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	f.provider("authentik", schemaenum.IdentityProviderKindOIDC, true)
	view := f.create("Riot Blue", schemaenum.ServerBlue)
	require.Len(t, view.AcceptedProviders, 2)

	updated, err := f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
		Providers: []circle.AcceptedProvider{{Key: "discord"}},
	})
	require.NoError(t, err)
	require.Len(t, updated.AcceptedProviders, 1)

	// A new join through the removed provider is refused; nothing else changed.
	_, err = circle.Accepted(t.Context(), f.store.Queries(), view.ID, "authentik")
	require.True(t, apierr.HasCode(err, apierr.CodeProviderNotAccepted), "got %v", err)
	_, err = circle.Accepted(t.Context(), f.store.Queries(), view.ID, "discord")
	require.NoError(t, err)
}

// The gate lives on `circle_provider` because the instance owns the application and the CIRCLE
// owns the gate: two circles on one instance may point at two different guilds.
func TestSetProviders_TheGuildGate_IsPerCircleAndRoundTrips(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	first := f.create("Riot Blue", schemaenum.ServerBlue)
	second := f.create("Rival Green", schemaenum.ServerGreen)

	_, err := f.service.SetProviders(t.Context(), first.ID, circle.SetProvidersRequest{
		Providers: []circle.AcceptedProvider{{
			Key: "discord", DiscordGuildID: "111", DiscordRequiredRoleIDs: []string{"raider", "officer"},
		}},
	})
	require.NoError(t, err)
	_, err = f.service.SetProviders(t.Context(), second.ID, circle.SetProvidersRequest{
		Providers: []circle.AcceptedProvider{{Key: "discord", DiscordGuildID: "222"}},
	})
	require.NoError(t, err)

	firstProvider, err := circle.Accepted(t.Context(), f.store.Queries(), first.ID, "discord")
	require.NoError(t, err)
	require.Equal(t, "111", firstProvider.Gate().GuildID)
	require.Equal(t, []string{"raider", "officer"}, firstProvider.Gate().RequiredRoleIDs)

	secondProvider, err := circle.Accepted(t.Context(), f.store.Queries(), second.ID, "discord")
	require.NoError(t, err)
	require.Equal(t, "222", secondProvider.Gate().GuildID)
	require.Empty(t, secondProvider.Gate().RequiredRoleIDs,
		"an empty list means anyone in the guild, and it must round-trip as empty rather than null")
	require.NotNil(t, secondProvider.Gate().RequiredRoleIDs)
}

// A gate on a provider with no guilds would be a gate nothing evaluates, which reads to an owner
// as a gate that is on.
func TestSetProviders_AGuildGateOnANonDiscordProvider_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("authentik", schemaenum.IdentityProviderKindOIDC, true)
	view := f.create("Riot Blue", schemaenum.ServerBlue)

	_, err := f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
		Providers: []circle.AcceptedProvider{{Key: "authentik", DiscordGuildID: "111"}},
	})
	require.True(t, apierr.HasCode(err, apierr.CodeValidationFailed), "got %v", err)

	_, err = f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
		Providers: []circle.AcceptedProvider{{
			Key: "authentik", DiscordRequiredRoleIDs: []string{"raider"},
		}},
	})
	require.True(t, apierr.HasCode(err, apierr.CodeValidationFailed), "got %v", err)
}

func TestSetProviders_WhatCannotBeAccepted_IsRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		providers []circle.AcceptedProvider
		want      apierr.Code
	}{
		{
			name:      "a provider this instance does not have",
			providers: []circle.AcceptedProvider{{Key: "keycloak"}},
			want:      apierr.CodeValidationFailed,
		},
		{
			name:      "a provider the instance has disabled",
			providers: []circle.AcceptedProvider{{Key: "retired"}},
			want:      apierr.CodeProviderDisabled,
		},
		{
			name:      "the same provider twice",
			providers: []circle.AcceptedProvider{{Key: "discord"}, {Key: "discord"}},
			want:      apierr.CodeValidationFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
			f.provider("retired", schemaenum.IdentityProviderKindOIDC, false)
			view := f.create("Riot Blue", schemaenum.ServerBlue)

			_, err := f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
				Providers: tt.providers,
			})
			require.True(t, apierr.HasCode(err, tt.want), "got %v", err)
		})
	}
}

// An empty list is a legitimate request — a circle that accepts nothing admits nobody — and it is
// distinct from a client that sent no field at all, which the edge would not turn into this call.
func TestSetProviders_AnEmptyList_LeavesTheCircleAcceptingNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	view := f.create("Riot Blue", schemaenum.ServerBlue)

	updated, err := f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{})
	require.NoError(t, err)
	require.Empty(t, updated.AcceptedProviders)
}

func TestAcceptsWeakProvider_IsTrueOnlyForAnEnabledUnverifiableOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	local := f.provider("local", schemaenum.IdentityProviderKindLocal, true)
	view := f.create("Riot Blue", schemaenum.ServerBlue)

	weak, err := f.service.AcceptsWeakProvider(t.Context(), view.ID)
	require.NoError(t, err)
	require.False(t, weak)

	_, err = f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
		Providers:                 []circle.AcceptedProvider{{Key: "discord"}, {Key: "local"}},
		AcknowledgeWeakRevocation: true,
	})
	require.NoError(t, err)
	weak, err = f.service.AcceptsWeakProvider(t.Context(), view.ID)
	require.NoError(t, err)
	require.True(t, weak)

	// Disabled at the instance, it admits nobody, so it no longer forces the one-use ceiling.
	f.disableProvider(local)
	weak, err = f.service.AcceptsWeakProvider(t.Context(), view.ID)
	require.NoError(t, err)
	require.False(t, weak)
}

// A provider the instance has since disabled reports `available: false` and answers
// `409 provider_disabled` at join. The row is marked, not hidden.
func TestAccepted_ADisabledProvider_IsRefusedRatherThanAbsent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	view := f.create("Riot Blue", schemaenum.ServerBlue)
	f.disableProvider(id)

	_, err := circle.Accepted(t.Context(), f.store.Queries(), view.ID, "discord")
	require.True(t, apierr.HasCode(err, apierr.CodeProviderDisabled), "got %v", err)

	after, err := f.service.Get(t.Context(), view.ID)
	require.NoError(t, err)
	require.Len(t, after.AcceptedProviders, 1)
	require.False(t, after.AcceptedProviders[0].Available)
}
