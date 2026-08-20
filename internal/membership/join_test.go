package membership_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
)

func TestJoin_AnOwnerGrant_MakesTheCirclesFirstOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")

	joined, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	require.True(t, joined.Created)
	require.Equal(t, string(authz.RoleOwner), joined.Membership.Role)
	require.Equal(t, "Tankguy", joined.Membership.DisplayName)
	require.Equal(t, view.ID, joined.Circle.ID)
	require.NotEmpty(t, joined.Token.Secret)
	require.Equal(t, joined.Token.Prefix, joined.Token.Secret[len("tods_pat_"):][:8])

	// A `local` membership is individually weak, and the representation says so rather than
	// leaving an officer to infer it from the circle.
	require.Equal(t, string(identity.StrengthWeak), joined.Membership.RevocationStrength)
	require.Equal(t, []string{"local"}, joined.Membership.WeakProviders)
	require.Equal(t, "local", joined.Membership.ProviderKey)

	// A grant leaves no `invite_redemption` row — that table's `invite_id` is NOT NULL — so the
	// audit log is where the first owner's arrival is recorded.
	audit, err := f.store.Queries().GetLatestAuditLogEntry(t.Context(), view.ID.String())
	require.NoError(t, err)
	require.Equal(t, "owner_grant.redeemed", audit.Action)
}

func TestJoin_AnInvite_CreatesTheMembershipAndConsumesOneUse(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)

	minted, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID, Role: authz.RoleMember,
	})
	require.NoError(t, err)

	joined, err := f.joinLocal(minted.Code, "Sneakco")
	require.NoError(t, err)
	require.True(t, joined.Created)
	require.Equal(t, string(authz.RoleMember), joined.Membership.Role)
	require.Equal(t, minted.ID.String(), joined.Membership.AdmittedByInviteID)

	after, err := f.invites.Get(t.Context(), view.ID, minted.ID)
	require.NoError(t, err)
	require.Equal(t, 1, after.Uses)
	require.False(t, after.Live)

	redemptions, err := f.store.Queries().ListInviteRedemptions(t.Context(),
		listRedemptionParams(view.ID, minted.ID))
	require.NoError(t, err)
	require.Len(t, redemptions, 1)
	require.Equal(t, joined.Membership.ID.String(), redemptions[0].MembershipID)
}

// The partial unique index `ux_membership_identity` is the entire revocation mechanism: a revoked
// person redeeming a fresh invite hits the EXISTING row rather than creating a second one, sees
// `revoked_at IS NOT NULL`, and gets `403 membership_revoked`. There is no second row, there is no
// delete-then-insert path, and there is no delete-membership operation at all.
func TestJoin_ARevokedMember_WithAFreshInvite_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	owner, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(f.ownerGrant(view)), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "owner", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "owner",
	})
	require.NoError(t, err)

	first, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID,
	})
	require.NoError(t, err)
	leaker, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(first.Code), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "leaker", "Leaker", discord.GuildFacts{}),
		},
		IdempotencyKey: "leaker",
	})
	require.NoError(t, err)

	_, err = f.members.Revoke(
		t.Context(), view.ID, leaker.Membership.ID, owner.Membership.ID, "leaked the board")
	require.NoError(t, err)

	second, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID,
	})
	require.NoError(t, err)
	_, err = f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(second.Code), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind: identity.CredentialProviderTicket,
			// The same subject, presented again — which is what a verifiable provider hands a
			// revoked person for free, and exactly the case the index exists for.
			Ticket: f.ticket(providerID, "leaker", "Not The Leaker", discord.GuildFacts{}),
		},
		IdempotencyKey: "rejoin",
	})
	require.True(t, apierr.HasCode(err, apierr.CodeMembershipRevoked), "got %v", err)

	// The invite is untouched: a refused join must not spend a use somebody else was owed.
	after, err := f.invites.Get(t.Context(), view.ID, second.ID)
	require.NoError(t, err)
	require.Equal(t, 0, after.Uses)

	// And there is still exactly one membership for that identity. A second row is what the index
	// makes unrepresentable, and this is the assertion that the index is doing it.
	members, err := f.members.List(t.Context(), view.ID)
	require.NoError(t, err)
	seen := 0
	for _, m := range members {
		if m.IdentityID == leaker.Membership.IdentityID {
			seen++
			require.NotNil(t, m.RevokedAt)
		}
	}
	require.Equal(t, 1, seen)
}

// The honest half of `local`, stated as a test rather than left to be discovered: a revoked member
// who redeems a fresh invite through the unverifiable provider IS a new person to this server, and
// gets in. That is not a bug to fix at the door — the subject is minted here, so there is nothing
// to recognise — it is why `local` ships disabled, is never auto-accepted, forces one-use invites,
// and makes every circle that accepts it report `revocation_strength: weak` on every read.
func TestJoin_ARevokedLocalMember_ReturnsUnderANewName_AndTheCircleSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	require.Equal(t, "weak", owner.Circle.RevocationStrength)

	first, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID,
	})
	require.NoError(t, err)
	leaker, err := f.joinLocal(first.Code, "Leaker")
	require.NoError(t, err)
	_, err = f.members.Revoke(
		t.Context(), view.ID, leaker.Membership.ID, owner.Membership.ID, "leaked the board")
	require.NoError(t, err)

	// `revoke_invalidates_invites` defaults on for a weak circle, so the invite the leaker used —
	// and every other outstanding one — went with them. That is the mitigation, and the response
	// said so at revocation time.
	fresh, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID,
	})
	require.NoError(t, err)
	again, err := f.joinLocal(fresh.Code, "Tanky")
	require.NoError(t, err, "a local identity mints a new subject, so this is a different person")
	require.NotEqual(t, leaker.Membership.IdentityID, again.Membership.IdentityID)
	require.Equal(t, "weak", again.Membership.RevocationStrength)
}

func TestJoin_ADeadInvite_IsRefusedWithTheReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kill func(*fixture, invite.Minted)
		want apierr.Code
	}{
		{
			name: "revoked",
			kill: func(f *fixture, m invite.Minted) {
				_, err := f.invites.Revoke(f.t.Context(), m.CircleID, m.ID)
				require.NoError(f.t, err)
			},
			want: apierr.CodeInviteRevoked,
		},
		{
			name: "expired",
			kill: func(f *fixture, _ invite.Minted) { f.clock.Advance(8 * 24 * time.Hour) },
			want: apierr.CodeInviteExpired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			view := f.localCircle("Riot Blue")
			owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
			require.NoError(t, err)
			minted, err := f.invites.Create(t.Context(), invite.CreateRequest{
				CircleID: view.ID, Actor: owner.Membership.ID,
			})
			require.NoError(t, err)

			tt.kill(f, minted)
			_, err = f.joinLocal(minted.Code, "Latecomer")
			require.True(t, apierr.HasCode(err, tt.want), "got %v", err)
		})
	}
}

// `403 identity_blocked` is the INSTANCE operator's decision, and it is checked at join as well as
// at ticket redemption so that a second circle is not a second door.
func TestJoin_BlockedIdentity_Refused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	grant := f.ownerGrant(view)

	joined, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(grant), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "first",
	})
	require.NoError(t, err)
	f.blockIdentity(joined.Membership.IdentityID, joined.Membership.ID)

	// A second circle on the same instance, whose officers have never heard of them.
	second, err := f.circles.Create(t.Context(), circleRequest("Rival Blue"))
	require.NoError(t, err)
	_, err = f.circles.SetProviders(t.Context(), second.ID, providersRequest("discord"))
	require.NoError(t, err)
	secondGrant, _, err := f.invites.MintOwnerGrant(t.Context(), second.ID)
	require.NoError(t, err)

	_, err = f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(secondGrant), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "second",
	})
	require.True(t, apierr.HasCode(err, apierr.CodeIdentityBlocked), "got %v", err)
}

// **The named test.** The gate is evaluated in BOTH `/join` and `/sessions`, through the one
// evaluator, against the facts on the 120-second ticket. A gate checked only at join is a gate
// somebody walks around by re-authing on a new device the day after they leave the guild.
func TestGuildGate_EvaluatedOnJoinAndSessions(t *testing.T) {
	t.Parallel()
	const guild = "guild-1"

	tests := []struct {
		name  string
		roles []string
		facts discord.GuildFacts
		want  apierr.Code
	}{
		{
			name:  "in the guild, no roles required",
			facts: discord.GuildFacts{guild: {Member: true}},
		},
		{
			name:  "in the guild, holding one of the required roles",
			roles: []string{"raider", "officer"},
			facts: discord.GuildFacts{guild: {Member: true, RoleIDs: []string{"officer"}}},
		},
		{
			name:  "not in the guild",
			roles: nil,
			facts: discord.GuildFacts{guild: {Member: false}},
			want:  apierr.CodeGuildMembershipRequired,
		},
		{
			name:  "in the guild, holding none of the required roles",
			roles: []string{"raider"},
			facts: discord.GuildFacts{guild: {Member: true, RoleIDs: []string{"guest"}}},
			want:  apierr.CodeGuildRoleRequired,
		},
		{
			name:  "no member object at all: an absent fact rejects, it never skips",
			roles: []string{"raider"},
			facts: discord.GuildFacts{},
			want:  apierr.CodeGuildRoleRequired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// --- /join --------------------------------------------------------------------------
			joinFixture := newFixture(t)
			view, providerID := joinFixture.discordCircle("Riot Blue", guild, tt.roles)
			joined, err := joinFixture.members.Join(t.Context(), membership.JoinRequest{
				Code: string(joinFixture.ownerGrant(view)), ProviderKey: "discord",
				Credential: identity.Credential{
					Kind: identity.CredentialProviderTicket,
					Ticket: joinFixture.ticket(
						providerID, "snowflake-1", "Tankguy", tt.facts),
				},
				IdempotencyKey: "join",
			})
			if tt.want == "" {
				require.NoError(t, err)
				require.True(t, joined.Created)
			} else {
				require.True(t, apierr.HasCode(err, tt.want), "join: got %v", err)
			}

			// --- /sessions ----------------------------------------------------------------------
			// The membership is created with the gate OPEN, so that the only thing under test on
			// the re-auth path is the gate itself. Anything else would let a `/sessions` that
			// never evaluated it pass by failing earlier.
			sessionFixture := newFixture(t)
			reView, reProvider := sessionFixture.discordCircle("Riot Blue", guild, nil)
			first, err := sessionFixture.members.Join(t.Context(), membership.JoinRequest{
				Code: string(sessionFixture.ownerGrant(reView)), ProviderKey: "discord",
				Credential: identity.Credential{
					Kind: identity.CredentialProviderTicket,
					Ticket: sessionFixture.ticket(reProvider, "snowflake-1", "Tankguy",
						discord.GuildFacts{guild: {Member: true}}),
				},
				IdempotencyKey: "join",
			})
			require.NoError(t, err)
			_, err = sessionFixture.circles.SetProviders(t.Context(), reView.ID,
				gatedProviders(guild, tt.roles))
			require.NoError(t, err)

			again, err := sessionFixture.members.Authenticate(t.Context(),
				membership.AuthenticateRequest{
					CircleID: reView.ID, ProviderKey: "discord",
					Credential: identity.Credential{
						Kind: identity.CredentialProviderTicket,
						Ticket: sessionFixture.ticket(
							reProvider, "snowflake-1", "Tankguy", tt.facts),
					},
					IdempotencyKey: "sessions",
				})
			if tt.want == "" {
				require.NoError(t, err)
				require.False(t, again.Created, "re-auth creates no membership")
				require.Equal(t, first.Membership.ID, again.Membership.ID)
				require.NotEqual(t, first.Token.Prefix, again.Token.Prefix,
					"a new device gets a new token")
			} else {
				require.True(t, apierr.HasCode(err, tt.want), "sessions: got %v", err)
			}
		})
	}
}

// `/sessions` answers 404 for a circle that does not exist, a circle this identity is not in, and
// a circle it never heard of — one answer, so the route confirms nothing about which.
func TestAuthenticate_WithNoMembership_IsNotFoundWhateverTheReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	_, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(f.ownerGrant(view)), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "join",
	})
	require.NoError(t, err)

	unknown, err := core.ParseID[core.Circle]("01K3TGT8N9M4X0Q7R2VB6C5D1E")
	require.NoError(t, err)
	other, err := f.circles.Create(t.Context(), circleRequest("Rival Blue"))
	require.NoError(t, err)
	_, err = f.circles.SetProviders(t.Context(), other.ID, providersRequest("discord"))
	require.NoError(t, err)

	for _, tc := range []struct {
		name     string
		circleID core.CircleID
		subject  string
	}{
		{"a circle that does not exist", unknown, "snowflake-1"},
		{"a circle this identity is not in", other.ID, "snowflake-1"},
		{"an identity this instance has never seen", view.ID, "snowflake-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.members.Authenticate(t.Context(), membership.AuthenticateRequest{
				CircleID: tc.circleID, ProviderKey: "discord",
				Credential: identity.Credential{
					Kind:   identity.CredentialProviderTicket,
					Ticket: f.ticket(providerID, tc.subject, "Tankguy", discord.GuildFacts{}),
				},
				IdempotencyKey: "sessions-" + tc.name,
			})
			require.True(t, apierr.HasCode(err, apierr.CodeNotFound), "got %v", err)
		})
	}
}

func TestAuthenticate_ARevokedMembership_IsMembershipRevoked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	owner, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(f.ownerGrant(view)), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "join",
	})
	require.NoError(t, err)

	// A second owner, so revoking the first is not `last_owner`.
	minted, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID, Role: authz.RoleOfficer,
	})
	require.NoError(t, err)
	officer, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(minted.Code), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-2", "Sneakco", discord.GuildFacts{}),
		},
		IdempotencyKey: "officer",
	})
	require.NoError(t, err)

	_, err = f.members.Revoke(
		t.Context(), view.ID, officer.Membership.ID, owner.Membership.ID, "")
	require.NoError(t, err)

	_, err = f.members.Authenticate(t.Context(), membership.AuthenticateRequest{
		CircleID: view.ID, ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-2", "Sneakco", discord.GuildFacts{}),
		},
		IdempotencyKey: "sessions",
	})
	require.True(t, apierr.HasCode(err, apierr.CodeMembershipRevoked), "got %v", err)
}

// A circle that does not accept a provider refuses a join through it, and one the INSTANCE has
// disabled refuses with the other code — two different fixes, two different codes.
func TestJoin_AProviderTheCircleDoesNotAccept_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	f.provider("authentik", "oidc")

	_, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(f.ownerGrant(view)), ProviderKey: "authentik",
		Credential:     identity.Credential{Kind: identity.CredentialIDToken, IDToken: "x", Nonce: "y"},
		IdempotencyKey: "join",
	})
	require.True(t, apierr.HasCode(err, apierr.CodeProviderNotAccepted), "got %v", err)
}

// The same `Idempotency-Key` and the same request replays the SAME token rather than minting a
// second one — which is what makes a dropped response survivable for a client that has no other
// way back in.
func TestJoin_ARetryWithTheSameKey_ReplaysTheSameResponse(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	owner, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(f.ownerGrant(view)), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "join",
	})
	require.NoError(t, err)

	// A ticket is single-use, so the retry below uses the re-auth path — which is the path a
	// non-browser client actually retries on. The `provider_ticket` retry is `401
	// auth_ticket_invalid` by design, and that is the design's answer rather than a gap.
	first, err := f.members.Authenticate(t.Context(), membership.AuthenticateRequest{
		CircleID: view.ID, ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "device-1",
	})
	require.NoError(t, err)
	require.False(t, first.Replayed)

	again, err := f.members.Authenticate(t.Context(), membership.AuthenticateRequest{
		CircleID: view.ID, ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "device-1",
	})
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Equal(t, first.Token.Prefix, again.Token.Prefix,
		"a replay hands back the token the first call minted, not a second one")
	require.Equal(t, owner.Membership.ID, again.Membership.ID)
}

// A member whose circle has since DROPPED their provider is told that, not told they are not a
// member. The two answers send them to different places — "another way in may still work" versus
// "go and get invited" — and answering 404 to somebody who is standing in the circle is the
// confident mistake this ordering exists to prevent.
//
// It is still not an oracle: the provider error is surfaced only after a credential has verified
// AND a live membership has been found, so nobody learns anything about a circle they are not in.
func TestAuthenticate_AMemberWhoseProviderTheCircleDropped_IsNotToldTheyAreNotAMember(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	member, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(f.ownerGrant(view)), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "join",
	})
	require.NoError(t, err)

	// The owner drops Discord. Existing memberships are untouched — that is the rule — so this
	// person is still a member of a circle they can no longer re-authenticate into that way.
	_, err = f.circles.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{})
	require.NoError(t, err)

	_, err = f.members.Authenticate(t.Context(), membership.AuthenticateRequest{
		CircleID: view.ID, ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "sessions",
	})
	require.True(t, apierr.HasCode(err, apierr.CodeProviderNotAccepted),
		"a member was told they are not a member; got %v", err)

	// And they really are still a member: revocation is what removes somebody, not a provider
	// change, and this is the assertion that the two did not get confused.
	view2, err := f.members.Get(t.Context(), view.ID, member.Membership.ID)
	require.NoError(t, err)
	require.Nil(t, view2.RevokedAt)
}

// The other half of the same ordering: somebody who is NOT a member still gets 404, even when the
// circle would have refused their provider anyway. The provider error must never be the thing that
// confirms a circle exists to a stranger.
func TestAuthenticate_ANonMember_StillGets404_EvenWhenTheProviderIsAlsoWrong(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	_, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(f.ownerGrant(view)), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "owner", "Tankguy", discord.GuildFacts{}),
		},
		IdempotencyKey: "join",
	})
	require.NoError(t, err)
	_, err = f.circles.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{})
	require.NoError(t, err)

	_, err = f.members.Authenticate(t.Context(), membership.AuthenticateRequest{
		CircleID: view.ID, ProviderKey: "discord",
		Credential: identity.Credential{
			Kind: identity.CredentialProviderTicket,
			// A verified subject with no membership in this circle at all.
			Ticket: f.ticket(providerID, "a-stranger", "Sneakco", discord.GuildFacts{}),
		},
		IdempotencyKey: "stranger",
	})
	require.True(t, apierr.HasCode(err, apierr.CodeNotFound),
		"the provider error leaked a circle's existence to a stranger; got %v", err)
}
