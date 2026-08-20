package circle_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
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

// **Required roles with no guild to require them in are refused.**
//
// A gate is identified by its guild — `discord.Gate.IsZero()` is `GuildID == ""` — and
// `EvaluateGate` returns nil for a zero gate before it looks at anything else. So a `discord` entry
// carrying role ids and no `discord_guild_id` would be stored, rendered back to the owner as a role
// gate, and admit every verified Discord identity, including one with no member object at all.
//
// The damage is not the admission. It is that the circle's own representation shows an admission
// control that is not running, which is the confident mistake this project is built against.
func TestSetProviders_RequiredRolesWithNoGuild_AreRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	view := f.create("Riot Blue", schemaenum.ServerBlue)

	_, err := f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
		Providers: []circle.AcceptedProvider{{
			Key: "discord", DiscordRequiredRoleIDs: []string{"raider"},
		}},
	})
	require.True(t, apierr.HasCode(err, apierr.CodeValidationFailed), "got %v", err)

	// Refusing rolled the whole request back rather than storing half of it. The circle still has
	// what creation auto-accepted — `discord`, with no gate — and in particular has NOT acquired
	// the role list, which is the thing that would have rendered as an admission control.
	after, err := f.service.Get(t.Context(), view.ID)
	require.NoError(t, err)
	require.Len(t, after.AcceptedProviders, 1)
	require.Equal(t, "discord", after.AcceptedProviders[0].Key)
	require.Empty(t, after.AcceptedProviders[0].DiscordRequiredRoleIDs)
	require.Empty(t, after.AcceptedProviders[0].DiscordGuildID)
	require.True(t, after.AcceptedProviders[0].Gate().IsZero())
}

// The end of that rule, driven through the evaluator: a gate the API accepts either names a guild
// and is enforced, or names nothing and admits — and there is no third shape where the two
// disagree. This is the assertion the write-side test above cannot make on its own, because the
// bug was precisely that configuration and enforcement said different things.
func TestSetProviders_EveryStorableGate_EvaluatesTheWayItReads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		guildID    string
		roleIDs    []string
		facts      discord.GuildFacts
		wantGated  bool
		admitsWith bool
	}{
		{
			name: "no guild and no roles: no gate, and it reads as none",
		},
		{
			name: "a guild and no roles: anyone in the guild", guildID: "g1",
			facts: discord.GuildFacts{"g1": {Member: true}}, wantGated: true, admitsWith: true,
		},
		{
			name: "a guild and roles: only a holder", guildID: "g1", roleIDs: []string{"raider"},
			facts:     discord.GuildFacts{"g1": {Member: true, RoleIDs: []string{"raider"}}},
			wantGated: true, admitsWith: true,
		},
		{
			name: "a guild and roles, held by nobody", guildID: "g1", roleIDs: []string{"raider"},
			facts:     discord.GuildFacts{"g1": {Member: true, RoleIDs: []string{"guest"}}},
			wantGated: true, admitsWith: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
			view := f.create("Riot Blue", schemaenum.ServerBlue)

			_, err := f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
				Providers: []circle.AcceptedProvider{{
					Key: "discord", DiscordGuildID: tt.guildID,
					DiscordRequiredRoleIDs: tt.roleIDs,
				}},
			})
			require.NoError(t, err)

			stored, err := circle.Accepted(t.Context(), f.store.Queries(), view.ID, "discord")
			require.NoError(t, err)

			// What the owner is shown, and what the evaluator does, have to be the same statement.
			require.Equal(t, tt.wantGated, !stored.Gate().IsZero(),
				"the circle renders roles %v but the gate is %v",
				stored.DiscordRequiredRoleIDs, stored.Gate())

			gateErr := identity.EvaluateGuildGate(stored.Gate(), tt.facts)
			if tt.admitsWith || !tt.wantGated {
				require.NoError(t, gateErr)
			} else {
				require.Error(t, gateErr)
			}

			// And a subject with NO facts at all is admitted only where there is genuinely no
			// gate. This is the assertion the original bug failed: it admitted everybody while
			// showing a role list.
			empty := identity.EvaluateGuildGate(stored.Gate(), discord.GuildFacts{})
			if tt.wantGated {
				require.Error(t, empty,
					"a gated circle admitted a subject we hold no facts for at all")
			} else {
				require.NoError(t, empty)
			}
		})
	}
}

// The write path cannot produce a roles-without-a-guild row, so a row in that shape was written by
// something else. Reading it is refused loudly rather than repaired: quietly dropping the roles and
// quietly denying everybody both hide which happened.
func TestAccepted_AStoredGateWithRolesAndNoGuild_IsRefusedOnRead(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	providerID := f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	view := f.create("Riot Blue", schemaenum.ServerBlue)

	// Written past the service, the way a hand-edited database or a future path would.
	_, err := f.store.Queries().PutCircleProvider(t.Context(), sqlitegen.PutCircleProviderParams{
		CircleID: view.ID.String(), ProviderID: providerID,
		DiscordGuildID: nil, DiscordRequiredRoleIdsJson: `["raider"]`,
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(t, err)

	_, err = circle.Accepted(t.Context(), f.store.Queries(), view.ID, "discord")
	require.Error(t, err, "a gate that renders as roles and evaluates as nothing was served")
	_, err = f.service.Get(t.Context(), view.ID)
	require.Error(t, err)
}
