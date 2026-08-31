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

const instancePath = api.BasePath + "/admin/instance"

// instanceAdmin seeds the instance row, an owner, a stepped-up session and the instance grant that
// reaches these routes.
func instanceAdmin(t *testing.T, h *harness, selfService bool) (string, core.MembershipID) {
	t.Helper()
	h.seedInstance(selfService)
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)
	return session, owner
}

func readInstanceSettings(t *testing.T, got response) api.InstanceSettingsResponse {
	t.Helper()
	var body api.InstanceSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &body), got.Body)
	return body
}

// The instance settings are instance-realm: `instance.security.manage` reaches them, no circle role
// grants that, and no PAT reaches it at any scope. Driven from BOTH sides — the same principal
// refused without a grant and served with one — because a test that only asserted the success half
// would pass with the permission check deleted.
func TestInstanceSettings_AGrant_IsWhatMakesThemReachable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(false)
	session, owner := h.adminSession(t)

	refused := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	h.requireProblem(refused, apierr.CodeForbidden)

	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)

	got := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	body := readInstanceSettings(t, got)
	require.Equal(t, "Test Instance", body.Name)
	require.False(t, body.SelfServiceCircleCreation)
	require.Empty(t, body.Changes, "a fresh instance has changed nothing")
}

// `instance.owner` reaches them too, and by EXPANSION rather than by being listed on the route.
// The route names the narrower key deliberately (ADR-0015): choosing `instance.owner` would have
// made delegating this one switch impossible without handing over the whole instance realm.
func TestInstanceSettings_InstanceOwner_ReachesThemThroughTheExpansion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedInstance(false)
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceOwner)

	got := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
}

// The whole point of the issue behind this route: an operator who answered the wizard one way is
// no longer stuck with it, and `/meta` — which is what a client actually reads to decide whether
// to offer "create a circle" — says so immediately.
func TestUpdateInstanceSettings_SelfService_ChangesWhatMetaReports(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, _ := instanceAdmin(t, h, false)

	before := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/meta"})
	require.Equal(t, http.StatusOK, before.Status)
	var meta api.ServerMeta
	require.NoError(t, json.Unmarshal([]byte(before.Body), &meta))
	require.False(t, meta.SelfServiceCircleCreation)

	read := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	require.Equal(t, http.StatusOK, read.Status, read.Body)

	updated := h.do(request{
		Method: http.MethodPatch, Path: instancePath, Session: session,
		Headers: map[string]string{api.IfMatchHeader: read.Header.Get(api.ETagHeader)},
		Body:    `{"self_service_circle_creation":true,"reason":"the guild grew"}`,
	})
	require.Equal(t, http.StatusOK, updated.Status, updated.Body)
	body := readInstanceSettings(t, updated)
	require.True(t, body.SelfServiceCircleCreation)

	// The response says exactly what was written, so an operator finds out from the write rather
	// than by going and looking.
	require.Len(t, body.Changes, 1)
	require.Equal(t, api.InstanceSettingKey("self_service_circle_creation"), body.Changes[0].Setting)
	require.Equal(t, "0", body.Changes[0].OldValue)
	require.Equal(t, "1", body.Changes[0].NewValue)
	require.Equal(t, "the guild grew", body.Changes[0].Reason)
	require.False(t, body.Changes[0].ByConsole)
	require.NotEmpty(t, body.Changes[0].ChangedByIdentityID)

	after := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/meta"})
	require.NoError(t, json.Unmarshal([]byte(after.Body), &meta))
	require.True(t, meta.SelfServiceCircleCreation)
}

// The change is attributed to the IDENTITY that made it, and the read returns the whole ledger.
// An audit record nobody can read is one nobody checks.
func TestGetInstanceSettings_TheLedger_NamesWhoChangedWhat(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, owner := instanceAdmin(t, h, false)

	read := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	written := h.do(request{
		Method: http.MethodPatch, Path: instancePath, Session: session,
		Headers: map[string]string{api.IfMatchHeader: read.Header.Get(api.ETagHeader)},
		Body:    `{"self_service_circle_creation":true,"name":"Riot","reason":"renamed"}`,
	})
	require.Equal(t, http.StatusOK, written.Status, written.Body)

	got := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	body := readInstanceSettings(t, got)
	require.Equal(t, "Riot", body.Name)
	require.Len(t, body.Changes, 2, "two settings moved and the ledger holds one row each")
	for _, change := range body.Changes {
		require.Equal(t, h.identityOf(owner).String(), change.ChangedByIdentityID)
		require.False(t, change.ByConsole)
	}
	// Newest first, which is what an administrator asking "who turned this on" wants at the top.
	require.GreaterOrEqual(t, body.Changes[0].ChangedAt, body.Changes[1].ChangedAt)
}

// The public URL must keep matching the redirect URI registered with every identity provider. A
// mismatch is a sign-in that completes at the provider and lands somewhere else, leaving no
// evidence here at all — so sending it is REFUSED rather than ignored, and the refusal names what
// to change instead.
func TestUpdateInstanceSettings_ThePublicURL_IsRefusedRatherThanIgnored(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, _ := instanceAdmin(t, h, false)

	read := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	got := h.do(request{
		Method: http.MethodPatch, Path: instancePath, Session: session,
		Headers: map[string]string{api.IfMatchHeader: read.Header.Get(api.ETagHeader)},
		Body:    `{"public_url":"https://moved.example.com"}`,
	})
	h.requireProblem(got, apierr.CodeFieldImmutable)
	require.Contains(t, got.Problem.Detail, "TOD_PUBLIC_URL")

	// And nothing moved: a refusal that had already written the other half of the request would
	// be worse than one that applied it.
	after := readInstanceSettings(t, h.do(request{
		Method: http.MethodGet, Path: instancePath, Session: session,
	}))
	require.Equal(t, "https://tod.example.com", after.PublicURL)
	require.Empty(t, after.Changes)
}

// `If-Match` is what makes a retry safe here, so it is REQUIRED and a stale one is refused. An
// optional precondition is one nobody sends.
func TestUpdateInstanceSettings_IfMatch_IsRequiredAndChecked(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, _ := instanceAdmin(t, h, false)

	missing := h.do(request{
		Method: http.MethodPatch, Path: instancePath, Session: session,
		Body: `{"self_service_circle_creation":true}`,
	})
	h.requireProblem(missing, apierr.CodePreconditionRequired)

	stale := h.do(request{
		Method: http.MethodPatch, Path: instancePath, Session: session,
		Headers: map[string]string{api.IfMatchHeader: `"not-the-current-tag"`},
		Body:    `{"self_service_circle_creation":true}`,
	})
	h.requireProblem(stale, apierr.CodePreconditionFailed)

	// The refusal is real, not cosmetic: nothing was written.
	require.Empty(t, readInstanceSettings(t, h.do(request{
		Method: http.MethodGet, Path: instancePath, Session: session,
	})).Changes)
}

// The entity tag covers `updated_at`, so turning a switch on and off again does NOT return to the
// tag a client already holds. Without that, a conditional read would answer 304 and the two
// changes the ledger recorded would be invisible.
func TestGetInstanceSettings_TheTag_MovesEvenWhenTheValuesReturn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, _ := instanceAdmin(t, h, false)

	first := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	original := first.Header.Get(api.ETagHeader)
	require.NotEmpty(t, original)

	on := h.do(request{
		Method: http.MethodPatch, Path: instancePath, Session: session,
		Headers: map[string]string{api.IfMatchHeader: original},
		Body:    `{"self_service_circle_creation":true}`,
	})
	require.Equal(t, http.StatusOK, on.Status, on.Body)

	h.clock.Advance(time.Minute)
	off := h.do(request{
		Method: http.MethodPatch, Path: instancePath, Session: session,
		Headers: map[string]string{api.IfMatchHeader: on.Header.Get(api.ETagHeader)},
		Body:    `{"self_service_circle_creation":false}`,
	})
	require.Equal(t, http.StatusOK, off.Status, off.Body)
	require.NotEqual(t, original, off.Header.Get(api.ETagHeader),
		"the settings returned to their original values and so did the tag; a cached client "+
			"would be told 304 and never see the two changes the ledger recorded")

	revalidated := h.do(request{
		Method: http.MethodGet, Path: instancePath, Session: session,
		Headers: map[string]string{api.IfNoneMatchHeader: original},
	})
	require.Equal(t, http.StatusOK, revalidated.Status)
	require.Len(t, readInstanceSettings(t, revalidated).Changes, 2)
}

// A request that would change nothing is refused rather than committed: an audit record whose rows
// include ones where nothing happened is one somebody has to filter before reading.
func TestUpdateInstanceSettings_ARequestThatChangesNothing_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, _ := instanceAdmin(t, h, true)

	read := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	got := h.do(request{
		Method: http.MethodPatch, Path: instancePath, Session: session,
		Headers: map[string]string{api.IfMatchHeader: read.Header.Get(api.ETagHeader)},
		Body:    `{"self_service_circle_creation":true}`,
	})
	h.requireProblem(got, apierr.CodeConflict)
	require.Empty(t, readInstanceSettings(t, h.do(request{
		Method: http.MethodGet, Path: instancePath, Session: session,
	})).Changes)
}

// An instance nobody has set up has no settings, and says so with the fix in the message rather
// than answering 500 or inventing a row.
func TestGetInstanceSettings_NoInstanceRow_SaysSo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, owner := h.adminSession(t)
	h.grantInstance(owner, authz.PermissionInstanceSecurityManage)

	got := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	h.requireProblem(got, apierr.CodeConflict)
}

// **The flag is advertised and not yet enforced, and this pins that rather than assuming it.**
//
// `createCircle` carries `instance.circle.create` in the route registry unconditionally: the
// permission is declared as data and checked by the middleware before any handler runs, so nothing
// on that path reads the `instance` row at all. Turning self-service on therefore changes what
// `/meta` publishes — which is what a client reads to decide whether to offer the button — and
// changes nothing about who the API will accept a circle from.
//
// Making it changeable does not close that gap, and closing it is a separate decision: a route
// whose permission depends on a row is something the registry cannot currently express, which is
// the whole of AGENTS.md law 1. This test exists so the gap is a stated fact with a red test
// waiting for whoever wires it, rather than something a reader infers from the field's name.
func TestCreateCircle_SelfServiceOn_StillRequiresTheInstanceGrant(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session, _ := instanceAdmin(t, h, false)

	read := h.do(request{Method: http.MethodGet, Path: instancePath, Session: session})
	on := h.do(request{
		Method: http.MethodPatch, Path: instancePath, Session: session,
		Headers: map[string]string{api.IfMatchHeader: read.Header.Get(api.ETagHeader)},
		Body:    `{"self_service_circle_creation":true}`,
	})
	require.Equal(t, http.StatusOK, on.Status, on.Body)

	// `/meta` moved, which is the half that works.
	meta := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/meta"})
	var published api.ServerMeta
	require.NoError(t, json.Unmarshal([]byte(meta.Body), &published))
	require.True(t, published.SelfServiceCircleCreation)

	// The route did not. An ordinary member of a circle, with no instance grant at all, is still
	// refused — self-service on or off.
	member := h.seedMember(h.seedCircle("Riot"), authz.RoleOwner)
	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/circles",
		Session: h.session(member, true),
		Headers: map[string]string{api.IdempotencyKeyHeader: "self-service-attempt"},
		Body:    `{"name":"Second","server":"blue"}`,
	})
	h.requireProblem(got, apierr.CodeForbidden)
}
