package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

func membersPath(id core.CircleID) string { return circlePath(id) + "/members" }

func memberPath(circleID core.CircleID, member core.MembershipID) string {
	return membersPath(circleID) + "/" + member.String()
}

// The member list carries `revocation_strength` per membership, which answers a different question
// from the circle's: "will revoking THIS person stick?" A circle accepting both `discord` and
// `local` is weak overall and its Discord members are individually durable.
func TestListMembers_CarriesPerMembershipRevocationStrength(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	token := h.seedToken(owner, authz.ScopeMemberRead)

	got := h.do(request{Method: http.MethodGet, Path: membersPath(mine), Token: token})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var page api.Page[map[string]any]
	require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
	require.Len(t, page.Items, 1)
	// The fixture's members join through `local`, which has no verifiable subject.
	require.Equal(t, "weak", page.Items[0]["revocation_strength"])
	require.Contains(t, page.Items[0]["provider_key"], "local",
		"the membership names the provider behind its identity, which is what makes the strength answerable")
	require.Equal(t, false, page.Items[0]["possible_duplicate"])
}

// `revokeMember` answers with the membership representation — carrying `revocation_strength` —
// plus `active_invite_count`, so a UI can say "you also have 2 live invites" without a separate
// warnings channel being invented beside it.
func TestRevokeMember_TheResponse_CarriesStrengthAndTheInviteCount(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	member := h.seedMember(mine, authz.RoleMember)
	session := h.session(owner, true)

	// Two live invites, minted by the owner's own token.
	token := h.seedToken(owner, authz.ScopeInviteCreate)
	for _, key := range []string{"a", "b"} {
		created := h.do(request{
			Method: http.MethodPost, Path: invitesPath(mine), Token: token,
			Headers: map[string]string{api.IdempotencyKeyHeader: key},
			Body:    `{}`,
		})
		require.Equal(t, http.StatusOK, created.Status, created.Body)
	}

	got := h.do(request{
		Method: http.MethodPost, Path: memberPath(mine, member) + "/revoke", Session: session,
		Headers: map[string]string{api.IfMatchHeader: h.memberETag(mine, member, owner)},
		Body:    `{"reason":"leaked the board"}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var revoked api.RevokedResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &revoked))
	require.NotNil(t, revoked.RevokedAt)
	require.Equal(t, "weak", revoked.RevocationStrength)
	// The circle is weakly revocable and `revoke_invalidates_invites` defaults on, so the invites
	// went in the same transaction and the response says how many.
	require.Equal(t, 2, revoked.InvitesRevoked)
	require.Equal(t, 0, revoked.ActiveInviteCount)
}

// Revocation is checked on EVERY request, so it takes effect on the revoked member's next call
// with no token sweep and nothing to forget.
func TestRevokeMember_TakesEffectOnTheNextRequest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	member := h.seedMember(mine, authz.RoleMember)
	memberToken := h.seedToken(member, authz.ScopeCircleRead)

	before := h.do(request{Method: http.MethodGet, Path: circlePath(mine), Token: memberToken})
	require.Equal(t, http.StatusOK, before.Status, before.Body)

	revoke := h.do(request{
		Method: http.MethodPost, Path: memberPath(mine, member) + "/revoke",
		Session: h.session(owner, true),
		Headers: map[string]string{api.IfMatchHeader: h.memberETag(mine, member, owner)},
		Body:    `{}`,
	})
	require.Equal(t, http.StatusOK, revoke.Status, revoke.Body)

	after := h.do(request{Method: http.MethodGet, Path: circlePath(mine), Token: memberToken})
	h.requireProblem(after, apierr.CodeMembershipRevoked)
}

// A circle cannot lose its last owner: there is no operation anywhere that creates one out of
// nothing.
func TestRevokeMember_TheLastOwner_Is409LastOwner(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)

	got := h.do(request{
		Method: http.MethodPost, Path: memberPath(mine, owner) + "/revoke",
		Session: h.session(owner, true),
		Headers: map[string]string{api.IfMatchHeader: h.memberETag(mine, owner, owner)},
		Body:    `{}`,
	})
	h.requireProblem(got, apierr.CodeLastOwner)
}

// Reinstatement is the only way back in, and it is explicit and gated on `member.revoke`.
func TestReinstateMember_IsTheOnlyWayBackIn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	member := h.seedMember(mine, authz.RoleMember)
	session := h.session(owner, true)

	revoke := h.do(request{
		Method: http.MethodPost, Path: memberPath(mine, member) + "/revoke", Session: session,
		Headers: map[string]string{api.IfMatchHeader: h.memberETag(mine, member, owner)},
		Body:    `{}`,
	})
	require.Equal(t, http.StatusOK, revoke.Status, revoke.Body)

	got := h.do(request{
		Method: http.MethodPost, Path: memberPath(mine, member) + "/reinstate", Session: session,
		Headers: map[string]string{api.IfMatchHeader: h.memberETag(mine, member, owner)},
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var view api.MemberResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &view))
	require.Nil(t, view.RevokedAt)
	require.Equal(t, string(authz.RoleMember), view.Role)
}

// **There is no delete-membership operation at all, anywhere.** The partial unique index is the
// entire revocation mechanism, and a DELETE that worked would be the delete-then-insert path that
// hands a revoked person a clean row.
func TestMembers_ThereIsNoDeleteOperation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	member := h.seedMember(mine, authz.RoleMember)

	got := h.do(request{
		Method: http.MethodDelete, Path: memberPath(mine, member),
		Session: h.session(owner, true),
	})
	require.Equal(t, http.StatusMethodNotAllowed, got.Status, got.Body)

	// And the registry holds no such operation either, so this is not a route somebody forgot to
	// wire — it is one that does not exist.
	for _, route := range api.Routes() {
		if route.Method == http.MethodDelete {
			require.NotContains(t, route.Path, "/members/",
				"%s deletes a membership; revocation is the mechanism, not deletion", route.ID)
		}
	}
}

// A bot gets a `kind='service'` membership with a human `owner_membership_id`, so the audit always
// names a responsible person.
func TestCreateServiceMember_NamesAHumanAndReturnsOneToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)

	got := h.do(request{
		Method: http.MethodPost, Path: circlePath(mine) + "/service-members",
		Session: h.session(owner, true),
		Headers: map[string]string{api.IdempotencyKeyHeader: "bot"},
		Body:    `{"display_name":"Invite Bot","role":"member","scopes":["invite:create"]}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var created api.ServiceMemberResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &created))
	require.Equal(t, schemaenum.MembershipKindService, created.Membership.Kind)
	require.Equal(t, owner.String(), created.Membership.OwnerMembershipID)
	require.Equal(t, []string{"invite:create"}, created.Token.Scopes)
	require.NotEmpty(t, created.Token.Secret)

	// The token works, and is bounded by the role — `role permissions ∩ token scopes`.
	minted := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine),
		Token:   core.Secret(created.Token.Secret),
		Headers: map[string]string{api.IdempotencyKeyHeader: "by-bot"},
		Body:    `{}`,
	})
	h.requireProblem(minted, apierr.CodeForbidden)
	require.Equal(t, string(authz.RoleMember), created.Membership.Role,
		"a member does not hold invite.create; that is the role narrowing, not the scope")
}

// `createServiceMember` is a state-creating POST, so `Idempotency-Key` is required and a retry
// replays rather than minting a second bot.
func TestCreateServiceMember_Idempotency_IsRequiredAndReplays(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	session := h.session(owner, true)
	body := `{"display_name":"Invite Bot"}`

	missing := h.do(request{
		Method: http.MethodPost, Path: circlePath(mine) + "/service-members",
		Session: session, Body: body,
	})
	h.requireProblem(missing, apierr.CodeIdempotencyKeyRequired)

	first := h.do(request{
		Method: http.MethodPost, Path: circlePath(mine) + "/service-members",
		Session: session, Headers: map[string]string{api.IdempotencyKeyHeader: "bot"}, Body: body,
	})
	require.Equal(t, http.StatusOK, first.Status, first.Body)

	retry := h.do(request{
		Method: http.MethodPost, Path: circlePath(mine) + "/service-members",
		Session: session, Headers: map[string]string{api.IdempotencyKeyHeader: "bot"}, Body: body,
	})
	require.Equal(t, http.StatusOK, retry.Status, retry.Body)
	require.Equal(t, "true", retry.Header.Get(api.IdempotencyReplayedHeader))
	require.JSONEq(t, first.Body, retry.Body, "a retry must not mint a second bot")

	// The same key with a DIFFERENT request is a client bug, and it says so rather than replaying
	// somebody else's answer.
	reused := h.do(request{
		Method: http.MethodPost, Path: circlePath(mine) + "/service-members",
		Session: session, Headers: map[string]string{api.IdempotencyKeyHeader: "bot"},
		Body: `{"display_name":"A Different Bot"}`,
	})
	h.requireProblem(reused, apierr.CodeIdempotencyKeyReused)
}

// memberETag reads a member and returns the tag a writer has to quote back.
func (h *harness) memberETag(
	circleID core.CircleID, member, reader core.MembershipID,
) string {
	h.t.Helper()
	got := h.do(request{
		Method: http.MethodGet, Path: memberPath(circleID, member),
		Token: h.seedToken(reader, authz.ScopeMemberRead),
	})
	require.Equal(h.t, http.StatusOK, got.Status, got.Body)
	etag := got.Header.Get(api.ETagHeader)
	require.NotEmpty(h.t, etag)
	return etag
}
