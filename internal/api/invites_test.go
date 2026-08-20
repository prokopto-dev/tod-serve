package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

func invitesPath(id core.CircleID) string { return circlePath(id) + "/invites" }

// Canonical §6: `invite.create` sits OUTSIDE the capability floor while `token.mint` sits inside
// it, so the bot that posts an invite link on request can exist. That trade is only defensible
// because an invite minted by a token is hard-narrowed on all three axes whatever the request
// asked for — and because the response says so.
func TestCreateInvite_ByAPAT_IsClampedAndTheResponseSaysCappedByPat(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	officer := h.seedMember(mine, authz.RoleOfficer)
	token := h.seedToken(officer, authz.ScopeInviteCreate, authz.ScopeInviteRead)

	got := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{"role":"officer","max_uses":25,"expires_in_seconds":2592000}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var minted api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &minted))
	require.Equal(t, 1, minted.MaxUses, "a token may not mint a multi-use invite")
	require.Equal(t, string(authz.RoleMember), minted.Role, "a token may not mint above member")
	require.Equal(t, fixtureNow.Add(24*time.Hour), minted.ExpiresAt,
		"a token may not mint an invite that outlives a day")
	require.Equal(t, invite.CappedByPAT, minted.CappedBy)
	require.Equal(t, "pat", minted.MintedByKind)
	require.NotEmpty(t, minted.Code, "the code is returned exactly once, here")
}

// A session is not clamped, and the response says nothing was narrowed — because nothing was. A
// `capped_by` that appeared on every response would be a field clients learn to ignore.
func TestCreateInvite_BySession_IsNotClampedAndSaysNothingWasCapped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	officer := h.seedMember(mine, authz.RoleOfficer)

	got := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine), Session: h.session(officer, true),
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{"role":"officer","max_uses":25,"expires_in_seconds":172800}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var minted api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &minted))
	require.Equal(t, 25, minted.MaxUses)
	require.Equal(t, string(authz.RoleOfficer), minted.Role)
	require.Empty(t, minted.CappedBy)
	require.Equal(t, "session", minted.MintedByKind)
}

// The clamp comes from the PRINCIPAL and never from the body. A token that could say "I am a
// session" would be a token with no clamp at all, so there are two defences and both are asserted:
// the field is not in the schema, and even if it were, the answer would still say `pat`.
func TestCreateInvite_ThePrincipalDecidesTheClamp_NotTheBody(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	officer := h.seedMember(mine, authz.RoleOfficer)
	token := h.seedToken(officer, authz.ScopeInviteCreate)

	// There is no `minted_by_kind` on the request at all, and an unknown property is refused
	// rather than ignored — an ignored one is a field a client believes it set.
	claimed := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "claimed"},
		Body:    `{"max_uses":10,"minted_by_kind":"session"}`,
	})
	h.requireProblem(claimed, apierr.CodeValidationFailed)
	require.NotEmpty(t, claimed.Problem.Errors)
	require.Equal(t, "body.minted_by_kind", claimed.Problem.Errors[0].Location)

	// And the same request without it is still clamped, because the principal decided.
	got := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{"max_uses":10}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var minted api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &minted))
	require.Equal(t, 1, minted.MaxUses)
	require.Equal(t, "pat", minted.MintedByKind)
	require.Equal(t, invite.CappedByPAT, minted.CappedBy)
}

// An owner invite is unrepresentable — `CHECK (role <> 'owner')` — so there is no value to clamp
// to that would be what the caller asked for. It is refused for a session and for a token alike.
func TestCreateInvite_AnOwnerInvite_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	officer := h.seedMember(mine, authz.RoleOfficer)

	got := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine), Session: h.session(officer, true),
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{"role":"owner"}`,
	})
	require.Equal(t, http.StatusUnprocessableEntity, got.Status, got.Body)
}

// The list carries the display prefix and never the code: the database holds only the hash, and
// the plaintext existed once, in the response that minted it.
func TestListInvites_CarriesThePrefixAndNeverTheCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	officer := h.seedMember(mine, authz.RoleOfficer)
	token := h.seedToken(officer, authz.ScopeInviteCreate, authz.ScopeInviteRead)

	created := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{}`,
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)
	var minted api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(created.Body), &minted))

	listed := h.do(request{Method: http.MethodGet, Path: invitesPath(mine), Token: token})
	require.Equal(t, http.StatusOK, listed.Status, listed.Body)
	require.NotContains(t, listed.Body, string(minted.Code),
		"the invite list carries a live code; the database holds only its hash for a reason")
	require.Contains(t, listed.Body, minted.CodePrefix)
}

// `previewInvite` takes the code in a POST BODY, never a path segment: a code is a bearer
// credential and a path lands in access logs, browser history and `Referer` headers.
func TestPreviewInvite_TellsACodeHolderWhatTheyWouldJoin_BeforeJoining(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	officer := h.seedMember(mine, authz.RoleOfficer)
	token := h.seedToken(officer, authz.ScopeInviteCreate)

	created := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{"role":"observer"}`,
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)
	var minted api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(created.Body), &minted))

	// No credential at all: the code IS the capability.
	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/invites/preview",
		Body: `{"code":"` + string(minted.Code) + `"}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var preview api.InvitePreview
	require.NoError(t, json.Unmarshal([]byte(got.Body), &preview))
	require.Equal(t, "Mine", preview.Circle.Name)
	require.Equal(t, "blue", preview.Circle.Server)
	require.Equal(t, string(authz.RoleObserver), preview.GrantedRole)
	require.Equal(t, "invite", preview.Kind)
	require.NotEmpty(t, preview.RevocationStrength,
		"revocation strength is shown BEFORE anybody joins; that is the whole point of the field")

	// The circle's id is deliberately absent: a code holder has no membership yet, and teaching
	// them an identifier they can probe other routes with buys nothing.
	require.NotContains(t, got.Body, mine.String())
}

// The middle of the range, at the edge: a code arrives lower-cased, without the scheme, with
// spaces. Every one of those is the same code, and a server that refused them would send the
// person back to an officer for a fresh one.
func TestPreviewInvite_ACodeTypedAnyWay_Resolves(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	officer := h.seedMember(mine, authz.RoleOfficer)
	token := h.seedToken(officer, authz.ScopeInviteCreate)

	created := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{}`,
	})
	var minted api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(created.Body), &minted))

	canonical := string(minted.Code)
	for _, typed := range []string{
		canonical,
		strings.ToLower(canonical),
		strings.TrimPrefix(canonical, invite.Scheme+"-"),
		strings.ReplaceAll(canonical, "-", ""),
		" " + canonical + " ",
		strings.ReplaceAll(canonical, "-", " "),
	} {
		got := h.do(request{
			Method: http.MethodPost, Path: api.BasePath + "/invites/preview",
			Body: `{"code":"` + typed + `"}`,
		})
		require.Equal(t, http.StatusOK, got.Status, "typed as %q: %s", typed, got.Body)
	}
}

// Unknown, unparseable and never-issued all answer identically; a dead code answers with the
// reason it is dead, which is exactly `previewInvite`'s disclosure and the ceiling every other
// public route that takes a code is held to.
func TestPreviewInvite_WhatIsNotALiveCode_AnswersTheRightCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want apierr.Code
	}{
		{"never issued", `{"code":"TODI-4KQ7M-9XPB2"}`, apierr.CodeInviteInvalid},
		{"not a code at all", `{"code":"hello"}`, apierr.CodeInviteInvalid},
		{"empty", `{"code":""}`, apierr.CodeInviteInvalid},
		{"a personal access token", `{"code":"tods_pat_ABCDEFGH_x"}`, apierr.CodeInviteInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			got := h.do(request{
				Method: http.MethodPost, Path: api.BasePath + "/invites/preview", Body: tt.body,
			})
			h.requireProblem(got, tt.want)
		})
	}
}

// Revoking an invite is in the capability floor, so a token does not reach it at any scope.
func TestRevokeInvite_NeedsASteppedUpSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	officer := h.seedMember(mine, authz.RoleOfficer)
	token := h.seedToken(officer, allScopes()...)

	created := h.do(request{
		Method: http.MethodPost, Path: invitesPath(mine), Token: token,
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{}`,
	})
	var minted api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(created.Body), &minted))
	path := invitesPath(mine) + "/" + minted.ID.String()

	byToken := h.do(request{Method: http.MethodDelete, Path: path, Token: token})
	h.requireProblem(byToken, apierr.CodeSessionRequired)

	bySession := h.do(request{
		Method: http.MethodDelete, Path: path, Session: h.session(officer, true),
	})
	require.Equal(t, http.StatusOK, bySession.Status, bySession.Body)

	// And the code stops resolving, which is what revoking one is for.
	preview := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/invites/preview",
		Body: `{"code":"` + string(minted.Code) + `"}`,
	})
	h.requireProblem(preview, apierr.CodeInviteRevoked)
}

// The `local` one-use ceiling, exercised through the EDGE rather than through the service.
//
// `internal/invite` is told whether the circle accepts an unverifiable provider; it does not look.
// So the service-level test passes that flag itself and cannot catch a handler that stops setting
// it — the ceiling would silently stop applying while every invite test stayed green. This is the
// test that fails when the one line computing it goes away.
//
// The ceiling exists because a `local` identity has no credential to re-present: `POST /sessions`
// cannot work for one, so every lost token becomes a new invite, and invite hygiene degrades until
// somebody leaves a 30-day 50-use invite lying around — the same hole weak revocation opens, from
// the other side.
func TestCreateInvite_IntoACircleAcceptingLocal_IsClampedToOneUseByTheHandler(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// A circle that accepts `local`, built the way `tod-serve circle create --accept-local` builds
	// one — an owner reaching for the weak provider deliberately.
	view, err := h.circles.Create(h.t.Context(), circle.CreateRequest{
		Name: "Weak", Server: core.Server(schemaenum.ServerBlue),
	})
	require.NoError(t, err)
	_, err = h.circles.SetProviders(h.t.Context(), view.ID, circle.SetProvidersRequest{
		Providers:                 []circle.AcceptedProvider{{Key: h.seedProviderKey()}},
		AcknowledgeWeakRevocation: true,
	})
	require.NoError(t, err)

	officer := h.seedMemberIn(view.ID, authz.RoleOfficer)
	got := h.do(request{
		Method: http.MethodPost, Path: invitesPath(view.ID), Session: h.session(officer, true),
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{"max_uses":25}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var minted api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &minted))
	require.Equal(t, 1, minted.MaxUses,
		"the local ceiling did not reach the mint; the handler is not computing it")
	require.Equal(t, invite.CappedByWeakProvider, minted.CappedBy,
		"a clamped request must say which rule narrowed it")

	// And a circle that accepts nothing weak is NOT clamped, so the test above is failing for the
	// right reason rather than because everything is clamped.
	durable := h.seedCircle("Durable")
	durableOfficer := h.seedMemberIn(durable, authz.RoleOfficer)
	unclamped := h.do(request{
		Method: http.MethodPost, Path: invitesPath(durable),
		Session: h.session(durableOfficer, true),
		Headers: map[string]string{api.IdempotencyKeyHeader: "mint"},
		Body:    `{"max_uses":25}`,
	})
	require.Equal(t, http.StatusOK, unclamped.Status, unclamped.Body)
	var other api.MintedInviteResponse
	require.NoError(t, json.Unmarshal([]byte(unclamped.Body), &other))
	require.Equal(t, 25, other.MaxUses)
	require.Empty(t, other.CappedBy)
}
