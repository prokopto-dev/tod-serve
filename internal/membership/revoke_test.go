package membership_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// A circle cannot lose its last owner: there is no operation anywhere that creates one out of
// nothing, so a circle without one has nobody who can change its providers or delete it.
func TestRevoke_TheLastOwner_IsLastOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)

	_, err = f.members.Revoke(
		t.Context(), view.ID, owner.Membership.ID, owner.Membership.ID, "")
	require.True(t, apierr.HasCode(err, apierr.CodeLastOwner), "got %v", err)

	// An officer is not an owner: promoting somebody below owner does not unlock it.
	officer := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")
	_, err = f.members.Revoke(
		t.Context(), view.ID, owner.Membership.ID, owner.Membership.ID, "")
	require.True(t, apierr.HasCode(err, apierr.CodeLastOwner), "got %v", err)

	// Promote them, and the first owner may leave.
	role := string(authz.RoleOwner)
	_, err = f.members.Update(t.Context(), view.ID, officer.Membership.ID, owner.Membership.ID,
		membership.UpdateRequest{Role: &role})
	require.NoError(t, err)
	_, err = f.members.Revoke(
		t.Context(), view.ID, owner.Membership.ID, officer.Membership.ID, "stepping down")
	require.NoError(t, err)
}

// A revoked owner does not count towards the one that has to remain.
//
// It is driven through `Revoke` because that is where a circle can still be talked down to its last
// owner: `Update` refuses a self-demotion outright, so the state this asserts is unreachable from
// there. Revocation is the one thing an owner may still do to themselves.
func TestRevoke_ARevokedOwner_IsNotTheOwnerThatRemains(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)

	second := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")
	promote := string(authz.RoleOwner)
	_, err = f.members.Update(t.Context(), view.ID, second.Membership.ID, owner.Membership.ID,
		membership.UpdateRequest{Role: &promote})
	require.NoError(t, err)

	// Two live owners, so the first may stand down — and then there is one again.
	_, err = f.members.Revoke(
		t.Context(), view.ID, second.Membership.ID, owner.Membership.ID, "")
	require.NoError(t, err)
	_, err = f.members.Revoke(
		t.Context(), view.ID, owner.Membership.ID, owner.Membership.ID, "")
	require.True(t, apierr.HasCode(err, apierr.CodeLastOwner),
		"a revoked owner is not an owner; got %v", err)
}

// Revoking a WEAKLY revocable member with `revoke_invalidates_invites` on takes every outstanding
// invite with them, in the same transaction. The invite still lying in Discord scrollback is the
// door they walk back through, and the damage is the officers' belief that revocation worked.
func TestRevoke_AWeakMember_TakesTheCirclesInvitesWithThem(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	require.True(t, view.RevokeInvalidatesInvites, "a weak circle defaults to invalidating invites")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	leaker := f.joinAs(view, owner, authz.RoleMember, "Leaker")

	for range 2 {
		_, err = f.invites.Create(t.Context(), invite.CreateRequest{
			CircleID: view.ID, Actor: owner.Membership.ID,
		})
		require.NoError(t, err)
	}
	live, err := f.invites.CountLive(t.Context(), view.ID)
	require.NoError(t, err)
	require.Equal(t, 2, live)

	revoked, err := f.members.Revoke(
		t.Context(), view.ID, leaker.Membership.ID, owner.Membership.ID, "leaked the board")
	require.NoError(t, err)
	require.Equal(t, string(identity.StrengthWeak), revoked.RevocationStrength)
	require.Equal(t, 2, revoked.InvitesRevoked, "the response has to SAY the invites went")
	require.Equal(t, 0, revoked.ActiveInviteCount)
	require.NotNil(t, revoked.RevokedAt)

	// Every LIVE invite is gone. An already-spent one is left alone: it closes no door, and
	// revoking it would inflate the count the officer is shown by the number of invites that were
	// never a way back in.
	after, err := f.invites.List(t.Context(), view.ID)
	require.NoError(t, err)
	spent := 0
	for _, inv := range after {
		require.False(t, inv.Live, "invite %s is still live after a weak revocation", inv.ID)
		if inv.RevokedAt == nil {
			spent++
			require.Equal(t, inv.MaxUses, inv.Uses,
				"invite %s was left unrevoked and was not already spent", inv.ID)
		}
	}
	require.Equal(t, 1, spent, "the invite the leaker redeemed was already spent")
}

// A DURABLE member's revocation leaves the invites alone: there is no second door to close, and
// revoking a guild's whole invite set every time somebody leaves is its own footgun.
func TestRevoke_ADurableMember_LeavesTheInvitesAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view, providerID := f.discordCircle("Riot Blue", "", nil)
	owner := f.joinTicket(view, providerID, "owner", "Tankguy", f.ownerGrant(view))
	minted, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID, Role: authz.RoleMember,
	})
	require.NoError(t, err)
	member := f.joinTicket(view, providerID, "member", "Sneakco", minted.Code)

	_, err = f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID,
	})
	require.NoError(t, err)

	revoked, err := f.members.Revoke(
		t.Context(), view.ID, member.Membership.ID, owner.Membership.ID, "")
	require.NoError(t, err)
	require.Equal(t, string(identity.StrengthDurable), revoked.RevocationStrength)
	require.Equal(t, 0, revoked.InvitesRevoked)
	require.Equal(t, 1, revoked.ActiveInviteCount,
		"the UI can say 'you also have 1 live invite' without a second warnings channel")
}

// Reinstatement is the only way back in, and it is explicit and audited.
func TestReinstate_ARevokedMember_ComesBackAtTheSameRole(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	member := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")

	_, err = f.members.Revoke(
		t.Context(), view.ID, member.Membership.ID, owner.Membership.ID, "a misunderstanding")
	require.NoError(t, err)

	back, err := f.members.Reinstate(
		t.Context(), view.ID, member.Membership.ID, owner.Membership.ID)
	require.NoError(t, err)
	require.Nil(t, back.RevokedAt)
	require.Empty(t, back.RevokeReason)
	require.Equal(t, string(authz.RoleOfficer), back.Role)

	// Reinstating twice is a conflict rather than a silent no-op: two officers disagreeing about
	// somebody's membership should find out.
	_, err = f.members.Reinstate(
		t.Context(), view.ID, member.Membership.ID, owner.Membership.ID)
	require.True(t, apierr.HasCode(err, apierr.CodeConflict), "got %v", err)

	audit, err := f.store.Queries().GetLatestAuditLogEntry(t.Context(), view.ID.String())
	require.NoError(t, err)
	require.Equal(t, "member.reinstated", audit.Action)
}

func TestRevoke_Twice_IsAConflict(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	member := f.joinAs(view, owner, authz.RoleMember, "Sneakco")

	_, err = f.members.Revoke(
		t.Context(), view.ID, member.Membership.ID, owner.Membership.ID, "")
	require.NoError(t, err)
	_, err = f.members.Revoke(
		t.Context(), view.ID, member.Membership.ID, owner.Membership.ID, "")
	require.True(t, apierr.HasCode(err, apierr.CodeConflict), "got %v", err)
}

// Two UNLINKED memberships sharing a normalised display name are flagged and NOT acted on. The fix
// is a deliberate officer link; merging two people because they picked the same name is a far
// worse mistake than showing both.
func TestList_TwoMembersWithOneName_AreFlaggedAsPossibleDuplicates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)

	// The middle of the range: the same person's name typed three ways. Normalisation is what
	// makes these one name, and a comparison on the raw column would flag none of them.
	f.joinAs(view, owner, authz.RoleMember, "Sneakco")
	f.joinAs(view, owner, authz.RoleMember, "sneak co")
	f.joinAs(view, owner, authz.RoleMember, "Vulak`Aerr")

	members, err := f.members.List(t.Context(), view.ID)
	require.NoError(t, err)

	flagged := map[string]bool{}
	for _, m := range members {
		flagged[m.DisplayName] = m.PossibleDuplicate
	}
	require.True(t, flagged["Sneakco"])
	require.True(t, flagged["sneak co"])
	require.False(t, flagged["Vulak`Aerr"], "a name nobody shares is not a duplicate")
	require.False(t, flagged["Tankguy"])

	// getMember answers the same thing, because `possible_duplicate` is a property of a PAIR and a
	// single read that answered false because it never looked would be a different field with the
	// same name in two responses.
	for _, m := range members {
		one, getErr := f.members.Get(t.Context(), view.ID, m.ID)
		require.NoError(t, getErr)
		require.Equal(t, m.PossibleDuplicate, one.PossibleDuplicate, "member %s", m.DisplayName)
	}
}

// A bot gets a `kind='service'` membership with a human `owner_membership_id`, so the audit always
// names a responsible person — which is what survives of DKP ADR-0011 after ADR-0005 moved tokens
// onto memberships.
func TestCreateService_NamesAResponsibleHumanAndMintsOneToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)

	created, err := f.members.CreateService(t.Context(), membership.CreateServiceRequest{
		CircleID: view.ID, Actor: owner.Membership.ID,
		DisplayName: "Invite Bot", Role: string(authz.RoleMember),
		Scopes: []string{"invite:create", "invite:read"},
	})
	require.NoError(t, err)
	require.Equal(t, schemaenum.MembershipKindService, created.Membership.Kind)
	require.Equal(t, owner.Membership.ID.String(), created.Membership.OwnerMembershipID)
	require.Empty(t, created.Membership.IdentityID, "a bot has no identity; it has an owner")
	require.Equal(t, []string{"invite:create", "invite:read"}, created.Token.Scopes)

	// A service membership has no third-party subject to re-present, so revoking it sticks.
	require.Equal(t, string(identity.StrengthDurable), created.Membership.RevocationStrength)
}

func TestCreateService_WhatIsRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*membership.CreateServiceRequest)
		wantErr apierr.Code
	}{
		{
			name: "an owner bot, whose token could never do an owner's job",
			mutate: func(r *membership.CreateServiceRequest) {
				r.Role = string(authz.RoleOwner)
			},
			wantErr: apierr.CodeValidationFailed,
		},
		{
			name: "an owner who is not in this circle",
			mutate: func(r *membership.CreateServiceRequest) {
				r.OwnerMembershipID = "01K3TGT8N9M4X0Q7R2VB6C5D1E"
			},
			wantErr: apierr.CodeValidationFailed,
		},
		{
			name: "an owner id that is not an id",
			mutate: func(r *membership.CreateServiceRequest) {
				r.OwnerMembershipID = "not-an-id"
			},
			wantErr: apierr.CodeValidationFailed,
		},
		{
			name:    "no display name",
			mutate:  func(r *membership.CreateServiceRequest) { r.DisplayName = "  " },
			wantErr: apierr.CodeValidationFailed,
		},
		{
			name: "a scope this instance does not have",
			mutate: func(r *membership.CreateServiceRequest) {
				r.Scopes = []string{"admin:*"}
			},
			wantErr: apierr.CodeValidationFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			view := f.localCircle("Riot Blue")
			owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
			require.NoError(t, err)

			req := membership.CreateServiceRequest{
				CircleID: view.ID, Actor: owner.Membership.ID, DisplayName: "Invite Bot",
			}
			tt.mutate(&req)
			_, err = f.members.CreateService(t.Context(), req)
			require.True(t, apierr.HasCode(err, tt.wantErr), "got %v", err)
		})
	}
}

// A bot owned by a revoked human is a bot nobody answers for.
func TestCreateService_AnOwnerWhoHasBeenRevoked_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	officer := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")
	_, err = f.members.Revoke(
		t.Context(), view.ID, officer.Membership.ID, owner.Membership.ID, "")
	require.NoError(t, err)

	_, err = f.members.CreateService(t.Context(), membership.CreateServiceRequest{
		CircleID: view.ID, Actor: owner.Membership.ID, DisplayName: "Invite Bot",
		OwnerMembershipID: officer.Membership.ID.String(),
	})
	require.True(t, apierr.HasCode(err, apierr.CodeValidationFailed), "got %v", err)
}

func TestUpdate_ADisplayNameAndARole_Change(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	member := f.joinAs(view, owner, authz.RoleMember, "Sneakco")

	name, role := "Sneak", string(authz.RoleOfficer)
	updated, err := f.members.Update(t.Context(), view.ID, member.Membership.ID,
		owner.Membership.ID, membership.UpdateRequest{DisplayName: &name, Role: &role})
	require.NoError(t, err)
	require.Equal(t, "Sneak", updated.DisplayName)
	require.Equal(t, string(authz.RoleOfficer), updated.Role)

	audit, err := f.store.Queries().GetLatestAuditLogEntry(t.Context(), view.ID.String())
	require.NoError(t, err)
	require.Equal(t, "member.updated", audit.Action)
}

func TestUpdate_ADisplayNameThatIsNotOne_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)

	for _, name := range []string{"", "   ", "---", longString(membership.MaxDisplayNameLen + 1)} {
		_, err := f.members.Update(t.Context(), view.ID, owner.Membership.ID,
			owner.Membership.ID, membership.UpdateRequest{DisplayName: &name})
		require.True(t, apierr.HasCode(err, apierr.CodeValidationFailed), "%q: got %v", name, err)
	}
}

// joinAs admits somebody through a fresh invite at the given role.
func (f *fixture) joinAs(
	view circle.Circle, owner membership.Joined, role authz.Role, displayName string,
) membership.Joined {
	f.t.Helper()
	minted, err := f.invites.Create(f.t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID, Role: role,
	})
	require.NoError(f.t, err)
	joined, err := f.joinLocal(minted.Code, displayName)
	require.NoError(f.t, err)
	return joined
}

// joinTicket admits somebody through the browser path, which is the only way to present the SAME
// verifiable subject twice.
func (f *fixture) joinTicket(
	view circle.Circle, providerID, subject, displayName string, code invite.Code,
) membership.Joined {
	f.t.Helper()
	joined, err := f.members.Join(f.t.Context(), membership.JoinRequest{
		Code: string(code), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind:   identity.CredentialProviderTicket,
			Ticket: f.ticket(providerID, subject, displayName, nil),
		},
		IdempotencyKey: "join-" + subject,
	})
	require.NoError(f.t, err)
	return joined
}

func longString(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}

// **An officer cannot promote anybody — including themselves — to owner.**
//
// `member.manage` is held by officers, so without this an officer could set their own role to
// `owner` and acquire `circle.security.manage`, `circle.delete`, `token.mint` and `token.revoke`.
// The rest of the design goes to real lengths to make becoming an owner deliberate: `invite`
// carries `CHECK (role <> 'owner')` precisely so a leaked bot token "can add a visible, revocable
// member — not seize the circle", and the only other door is a code the CLI prints once on the
// operator's own terminal. Self-promotion walks past both.
func TestUpdate_AnOfficer_CannotGrantARoleAboveTheirOwn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	officer := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")
	member := f.joinAs(view, owner, authz.RoleMember, "Tankgal")

	toOwner := string(authz.RoleOwner)
	tests := []struct {
		name    string
		subject core.MembershipID
	}{
		{"themselves, which is the escalation", officer.Membership.ID},
		{"somebody else, which is the same escalation one step removed", member.Membership.ID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.members.Update(t.Context(), view.ID, tt.subject,
				officer.Membership.ID, membership.UpdateRequest{Role: &toOwner})
			require.True(t, apierr.HasCode(err, apierr.CodeForbidden), "got %v", err)
		})
	}

	// The circle still has exactly one owner, so the refusal was not merely a returned error over
	// a write that happened anyway.
	members, err := f.members.List(t.Context(), view.ID)
	require.NoError(t, err)
	owners := 0
	for _, m := range members {
		if m.Role == string(authz.RoleOwner) {
			owners++
		}
	}
	require.Equal(t, 1, owners)
}

// The rule is "you may grant what you hold", not "officers may not manage roles". Everything at or
// below the actor's own role still works, or the guard would have taken away the capability
// `member.manage` exists to give.
func TestUpdate_GrantingAtOrBelowYourOwnRole_StillWorks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	officer := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")
	member := f.joinAs(view, owner, authz.RoleMember, "Tankgal")

	for _, role := range []authz.Role{
		authz.RoleObserver, authz.RoleMember, authz.RoleOfficer,
	} {
		granted := string(role)
		updated, updateErr := f.members.Update(t.Context(), view.ID, member.Membership.ID,
			officer.Membership.ID, membership.UpdateRequest{Role: &granted})
		require.NoError(t, updateErr, "an officer could not grant %s", role)
		require.Equal(t, granted, updated.Role)
	}

	// And an OWNER may still make another owner, which is how ownership is handed over — the guard
	// must not have closed the one door that is supposed to be open.
	toOwner := string(authz.RoleOwner)
	updated, err := f.members.Update(t.Context(), view.ID, member.Membership.ID,
		owner.Membership.ID, membership.UpdateRequest{Role: &toOwner})
	require.NoError(t, err)
	require.Equal(t, toOwner, updated.Role)
}

// A display-name change carries no role, so it must not be caught by a guard about roles — an
// officer renaming a member is the ordinary case `member.manage` exists for.
func TestUpdate_ADisplayNameChangeByAnOfficer_IsNotRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	officer := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")
	member := f.joinAs(view, owner, authz.RoleMember, "Tankgal")

	name := "Tank"
	updated, err := f.members.Update(t.Context(), view.ID, member.Membership.ID,
		officer.Membership.ID, membership.UpdateRequest{DisplayName: &name})
	require.NoError(t, err)
	require.Equal(t, "Tank", updated.DisplayName)
	require.Equal(t, string(authz.RoleMember), updated.Role)

	// Including on a member who OUTRANKS them: renaming an owner grants the officer nothing, and
	// refusing it would be a guard about roles quietly becoming a guard about names.
	ownerName := "Tankguy the First"
	renamed, err := f.members.Update(t.Context(), view.ID, owner.Membership.ID,
		officer.Membership.ID, membership.UpdateRequest{DisplayName: &ownerName})
	require.NoError(t, err)
	require.Equal(t, ownerName, renamed.DisplayName)
	require.Equal(t, string(authz.RoleOwner), renamed.Role)
}

// **A member's own role is not theirs to change.**
//
// Reported from real use: an owner could set their own role to something else from the Members
// page, and did. `last_owner` covered the sole owner and had nothing to say about a circle with
// two, which is exactly the state this was reported from.
//
// Handing over ownership is promoting somebody else, not demoting yourself: the promotion leaves
// the circle administered at every instant, and the person who ends up responsible for it agreed to
// be. It is not narrowed to `owner` — an officer holds `member.manage` and reaches this path for
// themselves too, and the rule reads the same at every role.
func TestUpdate_YourOwnRole_IsNeverYoursToChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	officer := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")
	toOfficer := string(authz.RoleOfficer)
	toMember := string(authz.RoleMember)
	toOwner := string(authz.RoleOwner)

	// The SOLE owner. This used to be `409 last_owner`; it is the standing refusal now, because
	// that code's own advice — promote somebody else, then repeat the operation — stopped being
	// true the moment a self-demotion was refused however many owners a circle has.
	_, err = f.members.Update(t.Context(), view.ID, owner.Membership.ID, owner.Membership.ID,
		membership.UpdateRequest{Role: &toOfficer})
	require.True(t, apierr.HasCode(err, apierr.CodeForbidden), "got %v", err)

	// A SECOND owner does not unlock it. This is the reported state.
	second := f.joinAs(view, owner, authz.RoleMember, "Tankgal")
	_, err = f.members.Update(t.Context(), view.ID, second.Membership.ID, owner.Membership.ID,
		membership.UpdateRequest{Role: &toOwner})
	require.NoError(t, err)
	_, err = f.members.Update(t.Context(), view.ID, owner.Membership.ID, owner.Membership.ID,
		membership.UpdateRequest{Role: &toOfficer})
	require.True(t, apierr.HasCode(err, apierr.CodeForbidden), "got %v", err)

	// And an OFFICER standing themselves down is the same rule, not an owner-shaped exception.
	_, err = f.members.Update(t.Context(), view.ID, officer.Membership.ID, officer.Membership.ID,
		membership.UpdateRequest{Role: &toMember})
	require.True(t, apierr.HasCode(err, apierr.CodeForbidden), "got %v", err)

	// Refused rather than an error returned over a write that happened anyway.
	require.Equal(t, string(authz.RoleOwner), f.roleOf(view.ID, owner.Membership.ID))
	require.Equal(t, string(authz.RoleOfficer), f.roleOf(view.ID, officer.Membership.ID))

	// Your own DISPLAY NAME is still yours: the guard fires only when a role moves, and a guard
	// about roles quietly becoming a guard about names would take away the one edit every member
	// makes to their own row.
	name := "Tankguy the First"
	renamed, err := f.members.Update(t.Context(), view.ID, owner.Membership.ID, owner.Membership.ID,
		membership.UpdateRequest{DisplayName: &name})
	require.NoError(t, err)
	require.Equal(t, name, renamed.DisplayName)
	require.Equal(t, string(authz.RoleOwner), renamed.Role)
}

// **An officer cannot change the role of somebody who outranks them.**
//
// The officer gains nothing by it — `refuseGrantAboveOwnRole` stops them taking the role — which is
// why this was left open beside that guard rather than shipped with it. It is still an officer
// removing their own supervisor, and the officer keeps `member.manage` afterwards while the person
// who could have taken it away no longer holds `circle.security.manage`.
func TestUpdate_AnOfficer_CannotChangeTheRoleOfAnOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	officer := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")

	// A second owner, so the refusal cannot be `last_owner` wearing this rule's clothes.
	second := f.joinAs(view, owner, authz.RoleMember, "Tankgal")
	toOwner := string(authz.RoleOwner)
	_, err = f.members.Update(t.Context(), view.ID, second.Membership.ID, owner.Membership.ID,
		membership.UpdateRequest{Role: &toOwner})
	require.NoError(t, err)

	for _, role := range []authz.Role{authz.RoleObserver, authz.RoleMember, authz.RoleOfficer} {
		t.Run(string(role), func(t *testing.T) {
			granted := string(role)
			_, updateErr := f.members.Update(t.Context(), view.ID, owner.Membership.ID,
				officer.Membership.ID, membership.UpdateRequest{Role: &granted})
			require.True(t, apierr.HasCode(updateErr, apierr.CodeForbidden), "got %v", updateErr)
		})
	}
	require.Equal(t, string(authz.RoleOwner), f.roleOf(view.ID, owner.Membership.ID))

	// The rule is about the role the SUBJECT holds, not about who they are: the same officer still
	// manages everybody at or below their own role.
	toMember := string(authz.RoleMember)
	demoted, err := f.members.Update(t.Context(), view.ID, second.Membership.ID,
		owner.Membership.ID, membership.UpdateRequest{Role: &toMember})
	require.NoError(t, err)
	require.Equal(t, toMember, demoted.Role)
	toObserver := string(authz.RoleObserver)
	byOfficer, err := f.members.Update(t.Context(), view.ID, second.Membership.ID,
		officer.Membership.ID, membership.UpdateRequest{Role: &toObserver})
	require.NoError(t, err)
	require.Equal(t, toObserver, byOfficer.Role)
}

// **Handing over ownership still works, which is the whole point of refusing the other half.**
//
// An owner promotes somebody else, and that person — an owner now, so not outranked — changes the
// first one's role. Every step is one somebody may take, and the circle has an owner at every
// instant in between.
func TestUpdate_HandingOverOwnership_StillWorks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.localCircle("Riot Blue")
	owner, err := f.joinLocal(f.ownerGrant(view), "Tankguy")
	require.NoError(t, err)
	successor := f.joinAs(view, owner, authz.RoleOfficer, "Sneakco")

	toOwner := string(authz.RoleOwner)
	promoted, err := f.members.Update(t.Context(), view.ID, successor.Membership.ID,
		owner.Membership.ID, membership.UpdateRequest{Role: &toOwner})
	require.NoError(t, err)
	require.Equal(t, toOwner, promoted.Role)

	toOfficer := string(authz.RoleOfficer)
	steppedDown, err := f.members.Update(t.Context(), view.ID, owner.Membership.ID,
		successor.Membership.ID, membership.UpdateRequest{Role: &toOfficer})
	require.NoError(t, err)
	require.Equal(t, toOfficer, steppedDown.Role)
	require.Equal(t, toOwner, f.roleOf(view.ID, successor.Membership.ID))
}

// roleOf re-reads a membership's role, so a refusal is asserted against the row rather than against
// the error the call returned.
func (f *fixture) roleOf(circleID core.CircleID, id core.MembershipID) string {
	f.t.Helper()
	got, err := f.members.Get(f.t.Context(), circleID, id)
	require.NoError(f.t, err)
	return got.Role
}
