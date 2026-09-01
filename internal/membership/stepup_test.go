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

// joinDiscord is the shape every test below starts from: a circle gated on nothing, and a member
// who joined through a ticket carrying a Discord subject. The subject is what makes a step-up
// possible at all — a `local` one is minted fresh each time and can never be re-presented.
func joinDiscord(
	t *testing.T, f *fixture, view circle.Circle, providerID, code, subject, name string,
) membership.Joined {
	t.Helper()
	joined, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: code, ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, subject, name, discord.GuildFacts{}),
		},
		IdempotencyKey: "join-" + subject,
	})
	require.NoError(t, err)
	return joined
}

// The whole point of the operation, asserted as the property it exists for: a re-proof adds no row
// to `api_token`.
//
// It is counted over the table rather than by reading the response, because "the response carried
// no token" and "no token was minted" are different claims and the device list is drawn from the
// second one. ADR-0024.
func TestStepUp_ReProvesTheIdentity_AndMintsNoDevice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	owner := joinDiscord(t, f, view, providerID, string(f.ownerGrant(view)), "snowflake-1", "Tankguy")

	before := f.tokenCount(owner.Membership.ID)
	require.Equal(t, 1, before, "joining mints one device, which is what /join is for")

	f.clock.Advance(time.Hour)
	got, err := f.members.StepUp(t.Context(), membership.StepUpRequest{
		MembershipID: owner.Membership.ID,
		ProviderKey:  "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
	})
	require.NoError(t, err)
	require.Equal(t, owner.Membership.ID, got.MembershipID)
	require.Equal(t, view.ID, got.CircleID)
	require.Equal(t, f.clock.Now(), got.SteppedUpAt)

	require.Equal(t, before, f.tokenCount(owner.Membership.ID),
		"stepping up minted a device; that is the bug the operation exists to remove")
}

// A credential that verifies and belongs to somebody else must not step this session up.
//
// This is the check with no analogue on `/sessions`, which resolves the membership FROM the
// credential. Here the membership comes off the caller's cookie and the credential is compared
// against it, so the two have to be matched explicitly or the route would be a way to refresh
// anybody's session with your own login.
func TestStepUp_ACredentialForAnotherIdentity_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	owner := joinDiscord(t, f, view, providerID, string(f.ownerGrant(view)), "snowflake-1", "Tankguy")

	minted, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID, Role: authz.RoleOfficer,
	})
	require.NoError(t, err)
	joinDiscord(t, f, view, providerID, string(minted.Code), "snowflake-2", "Sneakco")

	// The officer's own, valid, freshly minted credential against the owner's membership.
	_, err = f.members.StepUp(t.Context(), membership.StepUpRequest{
		MembershipID: owner.Membership.ID,
		ProviderKey:  "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-2", "Sneakco", discord.GuildFacts{}),
		},
	})
	require.True(t, apierr.HasCode(err, apierr.CodeCredentialInvalid), "got %v", err)

	// And an identity this instance has never seen answers identically, so the route reports
	// nothing about which subjects exist.
	_, err = f.members.StepUp(t.Context(), membership.StepUpRequest{
		MembershipID: owner.Membership.ID,
		ProviderKey:  "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-nobody", "Ghost", discord.GuildFacts{}),
		},
	})
	require.True(t, apierr.HasCode(err, apierr.CodeCredentialInvalid), "got %v", err)
}

// `local` mints a new subject on every verification, so a step-up through it can never succeed.
// Saying so is the point: `credential_invalid` would read as "try again", and there is no second
// attempt that works.
func TestStepUp_AProviderWithNoVerifiableSubject_SaysSoRatherThanRefusingTheCredential(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Operator")
	require.NoError(t, err)

	_, err = f.members.StepUp(t.Context(), membership.StepUpRequest{
		MembershipID: owner.Membership.ID,
		ProviderKey:  "local",
		Credential:   identity.Credential{Kind: identity.CredentialNone},
		DisplayName:  "Operator",
	})
	require.True(t, apierr.HasCode(err, apierr.CodeProviderUnverifiable), "got %v", err)
}

// A gate checked only at join is a gate somebody walks around by re-authing, and a step-up IS a
// re-auth. The guild the circle requires is read live, so a member who left the Discord server
// since signing in cannot refresh their proof.
func TestStepUp_AGateTheIdentityNoLongerPasses_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "guild-1", nil)
	inGuild := discord.GuildFacts{"guild-1": {Member: true}}

	joined, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(f.ownerGrant(view)), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", inGuild),
		},
		IdempotencyKey: "join",
	})
	require.NoError(t, err)

	// The same subject, now carrying a fact that says they are not in the guild.
	_, err = f.members.StepUp(t.Context(), membership.StepUpRequest{
		MembershipID: joined.Membership.ID,
		ProviderKey:  "discord",
		Credential: identity.Credential{
			Kind: identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy",
				discord.GuildFacts{"guild-1": {Member: false}}),
		},
	})
	require.True(t, apierr.HasCode(err, apierr.CodeGuildMembershipRequired), "got %v", err)

	// And a ticket carrying no fact for the gated guild at all answers the OTHER code: the
	// evaluator will not claim somebody is out of a guild it was told nothing about, so "we hold
	// no fact" is its own answer rather than a denial dressed up as one.
	_, err = f.members.StepUp(t.Context(), membership.StepUpRequest{
		MembershipID: joined.Membership.ID,
		ProviderKey:  "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-1", "Tankguy", discord.GuildFacts{}),
		},
	})
	require.True(t, apierr.HasCode(err, apierr.CodeGuildRoleRequired), "got %v", err)
}

// A revoked membership cannot step up. Revocation controls access, and a proof of identity is
// access — the edge refuses the credential on the next request anyway, and this is the same rule
// stated where the operation can also be called from inside the process.
func TestStepUp_ARevokedMembership_IsMembershipRevoked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	owner := joinDiscord(t, f, view, providerID, string(f.ownerGrant(view)), "snowflake-1", "Tankguy")

	minted, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID, Role: authz.RoleOfficer,
	})
	require.NoError(t, err)
	officer := joinDiscord(t, f, view, providerID, string(minted.Code), "snowflake-2", "Sneakco")

	_, err = f.members.Revoke(
		t.Context(), view.ID, officer.Membership.ID, owner.Membership.ID, "")
	require.NoError(t, err)

	_, err = f.members.StepUp(t.Context(), membership.StepUpRequest{
		MembershipID: officer.Membership.ID,
		ProviderKey:  "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, "snowflake-2", "Sneakco", discord.GuildFacts{}),
		},
	})
	require.True(t, apierr.HasCode(err, apierr.CodeMembershipRevoked), "got %v", err)
}

// tokenCount is how many `api_token` rows a membership holds, revoked ones included: the question
// is whether a row was WRITTEN, not whether it still works.
func (f *fixture) tokenCount(id core.MembershipID) int {
	f.t.Helper()
	rows, err := f.store.Queries().ListAPITokensForMembership(f.t.Context(), id.String())
	require.NoError(f.t, err)
	return len(rows)
}
