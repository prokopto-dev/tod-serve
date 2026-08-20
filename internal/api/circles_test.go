package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

func circlePath(id core.CircleID) string { return api.BasePath + "/circles/" + id.String() }

// `listCircles` returns only circles the caller is a member of, and a principal is bound to one
// membership — so it returns one. There is no list-all operation at any permission level: a
// circle's existence is part of what it is hiding.
func TestListCircles_ReturnsOnlyTheCallersOwnCircle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	member := h.seedMember(mine, authz.RoleOwner)
	token := h.seedToken(member, authz.ScopeCircleRead)
	h.seedCircle("Theirs")

	got := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/circles", Token: token})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var page api.Page[map[string]any]
	require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, mine.String(), page.Items[0]["id"])
	require.False(t, page.HasMore)
	require.Empty(t, page.NextCursor)
}

// The ETag is computed over the representation WITHOUT `as_of`, or every read would produce a new
// tag and every `If-Match` would be a 412.
func TestGetCircle_TheETag_IsStableAcrossReads(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	token := h.seedToken(h.seedMember(mine, authz.RoleOwner), authz.ScopeCircleRead)

	first := h.do(request{Method: http.MethodGet, Path: circlePath(mine), Token: token})
	require.Equal(t, http.StatusOK, first.Status, first.Body)
	require.NotEmpty(t, first.Header.Get(api.ETagHeader))

	h.advance(time.Minute)
	second := h.do(request{Method: http.MethodGet, Path: circlePath(mine), Token: token})
	require.Equal(t, first.Header.Get(api.ETagHeader), second.Header.Get(api.ETagHeader),
		"the tag moved without the resource changing; as_of has leaked into it")

	// And `as_of` really did move, so the test above is not passing because the clock stood still.
	var a, b api.CircleResponse
	require.NoError(t, json.Unmarshal([]byte(first.Body), &a))
	require.NoError(t, json.Unmarshal([]byte(second.Body), &b))
	require.NotEqual(t, a.AsOf, b.AsOf)
}

// ADR-0009: a circle is pinned to one server permanently. Sending `server` is refused with the code
// that says why rather than ignored, because ignoring it would let a client believe it had moved.
func TestUpdateCircle_Server_Is422FieldImmutable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	session := h.session(owner, true)
	etag := h.circleETag(mine, owner)

	got := h.do(request{
		Method: http.MethodPatch, Path: circlePath(mine), Session: session,
		Headers: map[string]string{api.IfMatchHeader: etag},
		Body:    `{"server":"green"}`,
	})
	h.requireProblem(got, apierr.CodeFieldImmutable)
	require.NotEmpty(t, got.Problem.Errors)
	require.Equal(t, "body.server", got.Problem.Errors[0].Location)
}

// `If-Match` is REQUIRED on a state transition, and a stale one is refused with the CURRENT
// representation attached so the read-merge-retry round trip costs no extra request.
func TestUpdateCircle_Concurrency_Requires428AndAnswers412WithTheCurrentBody(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)
	session := h.session(owner, true)

	missing := h.do(request{
		Method: http.MethodPatch, Path: circlePath(mine), Session: session,
		Body: `{"name":"Renamed"}`,
	})
	h.requireProblem(missing, apierr.CodePreconditionRequired)

	stale := h.do(request{
		Method: http.MethodPatch, Path: circlePath(mine), Session: session,
		Headers: map[string]string{api.IfMatchHeader: `"not-the-current-tag"`},
		Body:    `{"name":"Renamed"}`,
	})
	h.requireProblem(stale, apierr.CodePreconditionFailed)
	require.NotNil(t, stale.Problem.Meta)
	require.NotEmpty(t, stale.Problem.Meta.Current,
		"a 412 owes its caller the current representation, or the client has to guess")

	ok := h.do(request{
		Method: http.MethodPatch, Path: circlePath(mine), Session: session,
		Headers: map[string]string{api.IfMatchHeader: h.circleETag(mine, owner)},
		Body:    `{"name":"Renamed"}`,
	})
	require.Equal(t, http.StatusOK, ok.Status, ok.Body)
	var view api.CircleResponse
	require.NoError(t, json.Unmarshal([]byte(ok.Body), &view))
	require.Equal(t, "Renamed", view.Name)
	require.NotEqual(t, h.circleETag(mine, owner), stale.Header.Get(api.ETagHeader))
}

// The capability floor, at the edge: `circle.manage` is session-and-step-up only, so a token with
// every scope in the catalogue still does not reach it — and the two refusals point at two
// different fixes.
func TestUpdateCircle_TheCapabilityFloor_RefusesATokenAndAStaleSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)

	byToken := h.do(request{
		Method: http.MethodPatch, Path: circlePath(mine),
		Token:   h.seedToken(owner, allScopes()...),
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"name":"Renamed"}`,
	})
	h.requireProblem(byToken, apierr.CodeSessionRequired)

	byStaleSession := h.do(request{
		Method: http.MethodPatch, Path: circlePath(mine), Session: h.session(owner, false),
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body:    `{"name":"Renamed"}`,
	})
	h.requireProblem(byStaleSession, apierr.CodeStepUpRequired)
	require.NotNil(t, byStaleSession.Problem.Meta)
	require.Positive(t, byStaleSession.Problem.Meta.StepUpWindowSeconds)
}

// Within a circle, the wrong ROLE is 403 — not 404. The distinction is exactly: wrong tenant is
// 404, right tenant with insufficient permission is 403.
func TestGetCircle_TheWrongRole_IsForbiddenRatherThanNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	observer := h.seedMember(mine, authz.RoleObserver)

	// An observer holds `circle.read`, so it reads. A token with no scopes does not.
	scoped := h.do(request{
		Method: http.MethodGet, Path: circlePath(mine),
		Token: h.seedToken(observer, authz.ScopeCircleRead),
	})
	require.Equal(t, http.StatusOK, scoped.Status, scoped.Body)

	unscoped := h.do(request{
		Method: http.MethodGet, Path: circlePath(mine),
		Token: h.seedToken(observer),
	})
	h.requireProblem(unscoped, apierr.CodeInsufficientScope)
}

// A circle id that is not a ULID answers exactly what an unknown one does. Anything narrower would
// tell a prober their guess was at least well-formed.
func TestGetCircle_AMalformedID_AnswersTheSameAsAnUnknownOne(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	token := h.seedToken(h.seedMember(mine, authz.RoleOwner), authz.ScopeCircleRead)

	for _, id := range []string{"not-a-ulid", "01K3TGT8N9M4X0Q7R2VB6C5D1E", "", "%20"} {
		got := h.do(request{
			Method: http.MethodGet, Path: api.BasePath + "/circles/" + id, Token: token,
		})
		require.Equal(t, http.StatusNotFound, got.Status, "id %q answered: %s", id, got.Body)
	}
}

// `instance.circle.create` is an INSTANCE-realm permission and no circle role grants it, so this
// route is unreachable today. That is a documented hole rather than a bug in the handler, and it is
// pinned here so that whoever lands instance-realm grants finds a test waiting for them.
func TestCreateCircle_IsUnreachableUntilInstanceRealmGrantsExist(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.seedCircle("Mine")
	owner := h.seedMember(mine, authz.RoleOwner)

	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/circles", Session: h.session(owner, true),
		Headers: map[string]string{api.IdempotencyKeyHeader: "create"},
		Body:    `{"name":"Second","server":"green"}`,
	})
	h.requireProblem(got, apierr.CodeForbidden)
	require.False(t, authz.RolePermissions(authz.RoleOwner).Has(authz.PermissionInstanceCircleCreate),
		"an owner now holds instance.circle.create; this test is the wrong shape")
}

// circleETag reads the circle and returns the tag a writer has to quote back.
func (h *harness) circleETag(circleID core.CircleID, member core.MembershipID) string {
	h.t.Helper()
	got := h.do(request{
		Method: http.MethodGet, Path: circlePath(circleID),
		Token: h.seedToken(member, authz.ScopeCircleRead),
	})
	require.Equal(h.t, http.StatusOK, got.Status, got.Body)
	etag := got.Header.Get(api.ETagHeader)
	require.NotEmpty(h.t, etag)
	return etag
}
