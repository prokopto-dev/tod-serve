package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// The whole point of grading the windows, over the wire: one session, two operations, two answers.
//
// The session proved its identity ten minutes ago. That is past the five minutes a revocation asks
// for and well inside the hour an invite revocation asks for, so the same cookie is refused by one
// and accepted by the other. Before this, ten minutes meant re-authenticating for everything —
// which is the complaint ADR-0024 is about.
func TestStepUpTiers_ARoutineOperation_OutlivesASensitiveOne(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	owner := h.seedMember(circleID, authz.RoleOwner)
	subject := h.seedMember(circleID, authz.RoleMember)
	windows := auth.DefaultStepUpWindows()

	session := h.sessionProvedAt(owner, 10*time.Minute)

	// Routine: `invite.revoke`. A 404 means the request passed every gate in the middleware and
	// reached a handler that found no such invite, which is the signal that step-up was satisfied.
	routine := h.do(request{
		Method:  http.MethodDelete,
		Path:    circlePath(circleID) + "/invites/" + newID[core.Invite](h).String(),
		Session: session,
	})
	h.requireProblem(routine, apierr.CodeNotFound)

	// Sensitive: `member.revoke`, the same cookie, refused.
	sensitive := h.do(request{
		Method:  http.MethodPost,
		Path:    circlePath(circleID) + "/members/" + subject.String() + "/revoke",
		Session: session,
	})
	h.requireProblem(sensitive, apierr.CodeStepUpRequired)
	require.NotNil(t, sensitive.Problem.Meta)
	require.Equal(t, string(authz.StepUpSensitive), sensitive.Problem.Meta.StepUpTier,
		"the problem names WHICH bar was failed; a window alone is a number to reverse-engineer")
	require.Equal(t, int(windows.Sensitive.Seconds()), sensitive.Problem.Meta.StepUpWindowSeconds)

	// Past the routine window, the routine operation is refused too — and says so with the other
	// tier. Without this the test would pass on a build where `routine` never refused anything,
	// which is the failure mode a window equal to the session TTL would have.
	h.advance(windows.Routine)
	lapsed := h.do(request{
		Method:  http.MethodDelete,
		Path:    circlePath(circleID) + "/invites/" + newID[core.Invite](h).String(),
		Session: session,
	})
	h.requireProblem(lapsed, apierr.CodeStepUpRequired)
	require.Equal(t, string(authz.StepUpRoutine), lapsed.Problem.Meta.StepUpTier)
	require.Equal(t, int(windows.Routine.Seconds()), lapsed.Problem.Meta.StepUpWindowSeconds)
}

// `/me` reports every graded tier, so a console can say what it can still do and for how long
// rather than finding out one 403 at a time.
//
// The expiries are instants rather than durations because every countdown in the console is a
// signed offset from `as_of` — WEB002 — and a client that computed one from its own clock would
// render a window that is wrong on screen and right in the database.
func TestGetCurrentPrincipal_ReportsEveryStepUpTier_AndWhenEachLapses(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	owner := h.seedMember(circleID, authz.RoleOwner)
	windows := auth.DefaultStepUpWindows()

	got := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/me",
		Session: h.sessionProvedAt(owner, 10*time.Minute),
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var view api.PrincipalView
	require.NoError(t, json.Unmarshal([]byte(got.Body), &view))

	byTier := map[string]api.StepUpTierView{}
	for _, tier := range view.StepUp {
		byTier[tier.Tier] = tier
	}
	require.Len(t, byTier, 2, "`none` is the absence of a bar, not a row somebody can check")

	routine := byTier[string(authz.StepUpRoutine)]
	require.True(t, routine.Satisfied)
	require.Equal(t, int(windows.Routine.Seconds()), routine.WindowSeconds)
	require.NotNil(t, routine.ExpiresAt)
	require.True(t, view.AsOf.Before(*routine.ExpiresAt))

	sensitive := byTier[string(authz.StepUpSensitive)]
	require.False(t, sensitive.Satisfied)
	require.Equal(t, int(windows.Sensitive.Seconds()), sensitive.WindowSeconds)
	require.NotNil(t, sensitive.ExpiresAt)
	require.True(t, sensitive.ExpiresAt.Before(view.AsOf), "it lapsed five minutes ago")

	// The two fields that predate the tiers keep their old meaning exactly, so a client that never
	// learned about tiers is still right.
	require.False(t, view.SteppedUp)
	require.Equal(t, int(windows.Sensitive.Seconds()), view.StepUpWindowSeconds)

	// A token satisfies no tier at any scope, and reports no expiry rather than a zero one.
	byToken := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/me",
		Token: h.seedToken(owner, allScopes()...),
	})
	require.Equal(t, http.StatusOK, byToken.Status, byToken.Body)
	var tokenView api.PrincipalView
	require.NoError(t, json.Unmarshal([]byte(byToken.Body), &tokenView))
	for _, tier := range tokenView.StepUp {
		require.False(t, tier.Satisfied, "a token has never proved an identity: %s", tier.Tier)
		require.Nil(t, tier.ExpiresAt)
	}
}

// The route is session-only, like every other credential-altering operation: a token has no
// session to step up, and offering it the route could only ever let one credential raise
// another's privileges.
func TestStepUpSession_AToken_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	owner := h.seedMember(circleID, authz.RoleOwner)

	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/sessions/step-up",
		Token: h.seedToken(owner, allScopes()...),
		Body:  `{"provider":"` + localProviderKey + `","credential":{"kind":"none"}}`,
	})
	h.requireProblem(got, apierr.CodeSessionRequired)
}

// A `local` provider cannot re-prove an identity it already issued — it mints a new subject every
// time — and the route says that rather than answering `credential_invalid`, which would send
// somebody to try again forever.
//
// This is the console's cue to not offer a button at all. An instance whose only provider is
// `local` has no way to satisfy a step-up, and hiding that behind a refusal that reads like a typo
// is exactly the half-authenticated state with no way out that ADR-0024 is about.
func TestStepUpSession_AProviderThatCannotVerifyASubject_SaysSo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	owner := h.seedMember(circleID, authz.RoleOwner)

	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/sessions/step-up",
		Session: h.session(owner, false),
		Body: `{"provider":"` + localProviderKey + `","credential":{"kind":"none"},` +
			`"display_name":"Operator"}`,
	})
	h.requireProblem(got, apierr.CodeProviderUnverifiable)
	require.Equal(t, http.StatusUnprocessableEntity, got.Status)
}

// It needs a credential. A POST with no credential at all must not be a way to refresh a proof by
// asking nicely — which is what a route that mints nothing and takes nothing would be.
func TestStepUpSession_WithNoCredential_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("Riot")
	owner := h.seedMember(circleID, authz.RoleOwner)

	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/sessions/step-up",
		Session: h.session(owner, false),
		Body:    `{"provider":"` + localProviderKey + `","credential":{"kind":"provider_ticket"}}`,
	})
	require.NotEqual(t, http.StatusOK, got.Status, got.Body)
	require.NotEmpty(t, got.Problem.Code)
}

// The operation requires no step-up of its own, which would be a deadlock: the way out of a lapsed
// proof cannot itself need an unlapsed one.
func TestStepUpSession_IsNotItselfGatedOnStepUp(t *testing.T) {
	t.Parallel()
	route, ok := api.Lookup(api.OpStepUpSession)
	require.True(t, ok)
	require.Equal(t, authz.StepUpNone, route.StepUp(),
		"the route out of a lapsed proof cannot require an unlapsed one")
	require.True(t, route.SessionOnly(), "a token has no session to step up")
	require.False(t, route.CreatesState, "re-proving an identity writes no domain row")
	require.False(t, route.RequiresIdempotencyKey())
	require.False(t, route.CircleScoped,
		"the circle is the session's own; a caller cannot name one")
}
