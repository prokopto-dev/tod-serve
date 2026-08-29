package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// setupBody is a whole first run: an instance, the `local` provider with its acknowledgement, and
// a first circle. It is what every test here submits, so a refusal cannot be a malformed body.
const setupBody = `{
  "name": "Wizard Instance",
  "public_url": "https://tod.example.com",
  "provider": {
    "key": "local", "kind": "local", "display_name": "This server",
    "acknowledge_weak_revocation": true
  },
  "circle": {"name": "Riot Blue", "server": "blue"}
}`

// setupRequest builds a well-formed call to one setup route, so the three refusal tests below vary
// ONLY in what they are testing. A refusal that happened to be a 404 because the body was wrong
// would pass every one of them.
func setupRequest(route api.Route, token core.Secret) request {
	req := request{
		Method: route.Method,
		Path:   api.BasePath + route.Path,
		Token:  token,
	}
	if route.Method == http.MethodPost {
		req.Body = setupBody
		req.Headers = map[string]string{api.IdempotencyKeyHeader: "setup-refusal-probe"}
	}
	return req
}

// TestSetupRoutes_TokenUnset_EveryOperationRefuses is the first of the three refusals.
//
// **This route is a takeover surface.** An instance running with no `TOD_SETUP_TOKEN` — an upgrade
// of an existing deployment, or one whose operator deleted the line after setting up — must refuse
// every caller, including one presenting no token at all, which is what an empty configured value
// would otherwise compare equal to.
//
// It is derived from [api.SetupRoutes] rather than written per route, so a second setup route added
// later is covered by this the day it is added.
func TestSetupRoutes_TokenUnset_EveryOperationRefuses(t *testing.T) {
	t.Parallel()
	routes := api.SetupRoutes()
	require.NotEmpty(t, routes, "no setup routes; this gate is vacant")

	for _, route := range routes {
		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			h := newHarnessWithoutSetupToken(t)
			// The empty string is the case that matters: with no token configured, a caller who
			// sends nothing is comparing "" against "" and must still be refused.
			for _, presented := range []core.Secret{"", testSetupTok, testWrongTok} {
				got := h.do(setupRequest(route, presented))
				h.requireProblem(got, apierr.CodeNotFound)
			}
			requireNothingWritten(t, h)
		})
	}
}

// TestSetupRoutes_WrongToken_IsTheSameRefusalAsUnset is the second refusal, and it asserts more
// than that a wrong token fails.
//
// The two responses have to be INDISTINGUISHABLE. An instance with setup armed and one with the
// variable never set must look identical to somebody guessing, or the refusal itself tells a
// stranger which hosts are worth spending guesses on. They are compared whole rather than by
// status, because a differing `detail` leaks exactly as well as a differing code.
//
// The wrong token is the right one with one character changed and the SAME LENGTH:
// `subtle.ConstantTimeCompare` returns early on a length mismatch, so a shorter wrong token would
// be a weaker probe than it looks.
func TestSetupRoutes_WrongToken_IsTheSameRefusalAsUnset(t *testing.T) {
	t.Parallel()
	require.Len(t, testWrongTok, len(testSetupTok),
		"the wrong token must be the same length as the right one, or this proves less than it says")
	require.NotEqual(t, testSetupTok, testWrongTok)

	for _, route := range api.SetupRoutes() {
		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			armed := newHarness(t)
			wrong := armed.do(setupRequest(route, testWrongTok))
			armed.requireProblem(wrong, apierr.CodeNotFound)

			unset := newHarnessWithoutSetupToken(t)
			absent := unset.do(setupRequest(route, testWrongTok))
			unset.requireProblem(absent, apierr.CodeNotFound)

			require.Equal(t, absent.Status, wrong.Status)
			require.Equal(t, absent.Problem.Code, wrong.Problem.Code)
			require.Equal(t, absent.Problem.Title, wrong.Problem.Title)
			require.Equal(t, absent.Problem.Detail, wrong.Problem.Detail,
				"a wrong token and an unset one must not be tellable apart")
			requireNothingWritten(t, armed)
		})
	}
}

// TestSetupRoutes_AnAdministratorExists_EveryOperationRefuses is the third refusal, and the one
// that makes the window CLOSE.
//
// It is asserted against the real ledger rather than a flag, which is the whole of ADR-0016: there
// is no `setup_complete` row to set, so what shuts the wizard is an identity holding an
// administrator permission and nothing else.
func TestSetupRoutes_AnAdministratorExists_EveryOperationRefuses(t *testing.T) {
	t.Parallel()
	// Both keys that mean "administers this instance", each on its own. Either alone must close
	// the window: an instance whose only administrator holds `instance.owner` is administered.
	for _, permission := range []authz.Permission{
		authz.PermissionInstanceSecurityManage,
		authz.PermissionInstanceOwner,
	} {
		t.Run(string(permission), func(t *testing.T) {
			t.Parallel()
			for _, route := range api.SetupRoutes() {
				t.Run(string(route.ID), func(t *testing.T) {
					t.Parallel()
					h := newHarness(t)
					circle := h.seedCircle("Existing")
					member := h.seedMember(circle, authz.RoleOwner)

					// Open until the grant lands, which is what makes the refusal below a
					// statement about the grant rather than about the harness.
					require.Equal(t, http.StatusOK,
						h.do(setupRequest(routeGET(t), testSetupTok)).Status)

					h.grantInstance(member, permission)
					h.requireProblem(
						h.do(setupRequest(route, testSetupTok)), apierr.CodeConflict)
				})
			}
		})
	}
}

// TestSetupRoutes_AnAdministratorRevoked_ReopensTheWindow is the other half of deriving the
// window: an instance whose last administrator was revoked is unadministrable, and the wizard is
// one of the two ways back into it.
//
// A stored `setup_complete` flag could not answer this at all, which is the argument ADR-0016
// makes for not having one.
func TestSetupRoutes_AnAdministratorRevoked_ReopensTheWindow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Existing")
	member := h.seedMember(circle, authz.RoleOwner)

	h.grantInstance(member, authz.PermissionInstanceOwner)
	h.requireProblem(
		h.do(setupRequest(routeGET(t), testSetupTok)), apierr.CodeConflict)

	h.revokeInstance(member, authz.PermissionInstanceOwner)
	got := h.do(setupRequest(routeGET(t), testSetupTok))
	require.Equal(t, http.StatusOK, got.Status, "body was: %s", got.Body)

	var state api.SetupState
	require.NoError(t, json.Unmarshal([]byte(got.Body), &state))
	require.True(t, state.Available)
	require.False(t, state.AdministratorExists)
}

// TestSetupRoutes_AnAdministratorWhoCannotLogIn_ReopensTheWindow is the second unadministrable
// state, and the one a grant-only derivation calls healthy.
//
// An instance grant is on an IDENTITY; an identity reaches a request only through a membership, and
// `Authenticator.membership` refuses a revoked one on every call. The ledger outlives the
// membership — a revocation is a membership row, not a grant row — so `instance.owner` can be held
// by somebody who cannot sign in. Closing setup on that would lock the operator out of the instance
// AND out of the browser door back into it, which is the failure ADR-0016 exists to prevent.
//
// It is the same predicate `tod-serve doctor` reports on, through
// `instancegrant.CanAuthenticate`: an operator told nobody can administer this instance must not
// then be told setup is over.
func TestSetupRoutes_AnAdministratorWhoCannotLogIn_ReopensTheWindow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circle := h.seedCircle("Existing")
	member := h.seedMember(circle, authz.RoleOwner)
	h.grantInstance(member, authz.PermissionInstanceOwner)

	// Held and live: shut.
	h.requireProblem(h.do(setupRequest(routeGET(t), testSetupTok)), apierr.CodeConflict)

	// The GRANT is untouched — this revokes the membership, which is the whole point.
	h.revokeMembership(circle, member)

	got := h.do(setupRequest(routeGET(t), testSetupTok))
	require.Equal(t, http.StatusOK, got.Status,
		"a grant nobody can carry closed the one door back in; body was: %s", got.Body)
	var state api.SetupState
	require.NoError(t, json.Unmarshal([]byte(got.Body), &state))
	require.True(t, state.Available)
	require.False(t, state.AdministratorExists)

	// And `/meta` agrees, because that is what the console routes on.
	meta := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/meta"})
	var served api.ServerMeta
	require.NoError(t, json.Unmarshal([]byte(meta.Body), &served))
	require.True(t, served.SetupAvailable)
}

// TestSetupState_AHalfFinishedSetup_IsReportedAndResumable is the state ADR-0016 exists to make
// survivable: an `instance` row, a provider and a circle, and no administrator behind any of them.
//
// `/meta` says `configured: true` there. If the window closed on that, the operator would be
// locked out of the instance AND out of the wizard that fixes it — so this drives the whole thing
// twice and requires the second run to converge rather than duplicate.
func TestSetupState_AHalfFinishedSetup_IsReportedAndResumable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	first := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/setup", Token: testSetupTok,
		Body:    setupBody,
		Headers: map[string]string{api.IdempotencyKeyHeader: "setup-run-1"},
	})
	require.Equal(t, http.StatusOK, first.Status, "body was: %s", first.Body)
	var one api.SetupResult
	require.NoError(t, json.Unmarshal([]byte(first.Body), &one))
	require.NotEmpty(t, one.OwnerCode)
	require.Equal(t, "/join#"+one.OwnerCode, one.JoinPath)
	require.Equal(t, "weak", one.RevocationStrength,
		"a circle accepting `local` is weakly revocable, and the wizard has to say so")

	// Nothing has been redeemed, so nobody administers this instance and the window is still open
	// — even though `configured` is now true.
	meta := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/meta"})
	var served api.ServerMeta
	require.NoError(t, json.Unmarshal([]byte(meta.Body), &served))
	require.True(t, served.Configured)
	require.True(t, served.SetupAvailable,
		"an instance row is not an administrator; closing the window here is the lockout")

	state := h.do(setupRequest(routeGET(t), testSetupTok))
	var described api.SetupState
	require.NoError(t, json.Unmarshal([]byte(state.Body), &described))
	require.Len(t, described.Circles, 1)
	require.Len(t, described.Providers, 1)

	// A second run with the SAME body and a DIFFERENT key: it must not create a second circle, and
	// it must say why rather than silently picking one.
	second := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/setup", Token: testSetupTok,
		Body:    setupBody,
		Headers: map[string]string{api.IdempotencyKeyHeader: "setup-run-2"},
	})
	h.requireProblem(second, apierr.CodeConflict)

	// Naming the circle it already made is how the run is resumed, and it issues a FRESH code:
	// the first one was never stored in plaintext and cannot be recovered.
	resumed := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/setup", Token: testSetupTok,
		Body: `{"name":"Wizard Instance","public_url":"https://tod.example.com",
		        "provider":{"key":"local","kind":"local","acknowledge_weak_revocation":true},
		        "circle":{"id":"` + described.Circles[0].ID.String() + `"}}`,
		Headers: map[string]string{api.IdempotencyKeyHeader: "setup-run-3"},
	})
	require.Equal(t, http.StatusOK, resumed.Status, "body was: %s", resumed.Body)
	var three api.SetupResult
	require.NoError(t, json.Unmarshal([]byte(resumed.Body), &three))
	require.Equal(t, described.Circles[0].ID, three.CircleID)
	require.NotEqual(t, one.OwnerCode, three.OwnerCode)

	// Still one circle and one provider. A resumed run adds nothing it already added.
	after := h.do(setupRequest(routeGET(t), testSetupTok))
	require.NoError(t, json.Unmarshal([]byte(after.Body), &described))
	require.Len(t, described.Circles, 1)
	require.Len(t, described.Providers, 1)
}

// TestMeta_SetupAvailable_IsFalseWithNoToken keeps the console's routing signal honest.
//
// `setup_available` is what the SPA routes on, and an instance with no token set can never
// complete setup over HTTP — so advertising the wizard there would send an operator to a form that
// cannot work, on the one screen where they have no other diagnosis available.
func TestMeta_SetupAvailable_IsFalseWithNoToken(t *testing.T) {
	t.Parallel()
	h := newHarnessWithoutSetupToken(t)
	got := h.do(request{Method: http.MethodGet, Path: api.BasePath + "/meta"})
	require.Equal(t, http.StatusOK, got.Status)

	var meta api.ServerMeta
	require.NoError(t, json.Unmarshal([]byte(got.Body), &meta))
	require.False(t, meta.Configured)
	require.False(t, meta.SetupAvailable,
		"no TOD_SETUP_TOKEN means setup cannot complete; saying it is available is a lie")
}

// TestRunSetup_NeitherSecret_LeavesThroughAnotherRoute closes "never echoed" for both of the
// secrets this flow handles.
//
// The setup token must appear in no response at all. The owner code must appear in EXACTLY ONE —
// the run that minted it — because `tod_meta` holds only its hash
// (`TestGrant_TheCode_IsNeverStored` in internal/invite is the other half of that), so a second
// route that could hand it back would be the only place a database read yields a live credential.
func TestRunSetup_NeitherSecret_LeavesThroughAnotherRoute(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/setup", Token: testSetupTok,
		Body:    setupBody,
		Headers: map[string]string{api.IdempotencyKeyHeader: "setup-secrecy"},
	})
	require.Equal(t, http.StatusOK, got.Status, "body was: %s", got.Body)
	var result api.SetupResult
	require.NoError(t, json.Unmarshal([]byte(got.Body), &result))
	require.NotEmpty(t, result.OwnerCode)
	require.NotContains(t, got.Body, testSetupTok.Reveal(),
		"the run echoed the token that authorised it")

	for _, probe := range []request{
		{Method: http.MethodGet, Path: api.BasePath + "/meta"},
		{Method: http.MethodGet, Path: api.BasePath + "/setup", Token: testSetupTok},
	} {
		after := h.do(probe)
		require.NotContains(t, after.Body, result.OwnerCode,
			"%s hands back a live owner code", probe.Path)
		require.NotContains(t, after.Body, testSetupTok.Reveal(),
			"%s echoed the setup token", probe.Path)
	}
}

// TestRouteRegistry_EveryRoute_RefusesATokenInTheURL is the mechanism canonical §7's "no
// exception" was missing.
//
// The rule was enforced inside `authorize`, and two auth kinds return before reaching it —
// `AuthMetricsToken` and `AuthSetupToken` — so a request with a valid credential in the header and
// a token in the query was served, leaving it in the access log of every proxy in between. The
// setup token is the worst one to leak that way: it takes the instance over.
//
// It is derived from the ROUTE REGISTRY rather than a list, so the next auth kind somebody adds is
// covered on the day it is added rather than the day somebody notices. It drives every registered
// operation, including the ones on the metrics listener.
func TestRouteRegistry_EveryRoute_RefusesATokenInTheURL(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	circleID := h.seedCircle("QueryToken")

	// Two shapes, because [auth.RejectTokenInURL] refuses on two grounds: a parameter NAMED like a
	// credential, and any parameter whose VALUE is one of our tokens whatever it is called.
	probes := []struct {
		name  string
		query string
	}{
		{"a credential-shaped parameter name", "access_token=anything"},
		{"our own token under any name at all", "x=tods_pat_smuggled"},
	}

	driven := 0
	for _, route := range api.Routes() {
		served := false
		for _, id := range h.server.Registered() {
			if id == route.ID {
				served = true
			}
		}
		if !served {
			continue
		}
		for _, probe := range probes {
			driven++
			got := h.do(request{
				Method:  route.Method,
				Path:    pathFor(route, circleID) + "?" + probe.query,
				Body:    bodyFor(route),
				Metrics: route.Auth == api.AuthMetricsToken,
				Headers: map[string]string{
					api.IdempotencyKeyHeader: "url-token-" + string(route.ID),
					api.IfMatchHeader:        "*",
				},
			})
			require.Equalf(t, http.StatusUnauthorized, got.Status,
				"%s answered %d to %s; a token in a URL is refused with no exception. Body: %s",
				route.ID, got.Status, probe.name, got.Body)
			require.Equalf(t, apierr.CodeUnauthenticated, got.Problem.Code,
				"%s refused %s with %q rather than unauthenticated",
				route.ID, probe.name, got.Problem.Code)
		}
	}
	// The vacuity guard: this walks a registry a refactor could empty, and a gate that passes over
	// nothing passes over anything.
	require.Positive(t, driven, "no route was driven; the registry walk is wrong")
}

// routeGET returns the read half of the setup surface, failing rather than defaulting: a test that
// silently probed nothing would be a green test over an empty set.
func routeGET(t *testing.T) api.Route {
	t.Helper()
	for _, route := range api.SetupRoutes() {
		if route.Method == http.MethodGet {
			return route
		}
	}
	require.FailNow(t, "no GET setup route")
	return api.Route{}
}

// requireNothingWritten asserts a refused run wrote nothing. A route that refused the caller and
// created the instance anyway would pass a status assertion and fail the only thing that matters.
func requireNothingWritten(t *testing.T, h *harness) {
	t.Helper()
	_, err := h.store.Queries().GetInstance(h.t.Context())
	require.Error(t, err, "a refused setup run created the instance row")
}

// TestSetupRoutes_NoIdempotencyKey_AreRefusedBeforeTheyWrite is the gate on the registry telling
// the truth about the edge.
//
// `POST /setup` declares `CreatesState: true` and `Idempotency: IdempotencyHandler`, and the setup
// branch of the middleware called the handler directly — so the one operation on this instance that
// mints an instance-owner credential was the one operation served with no retry key at all. The
// declaration was right and the edge did not read it, which is precisely the failure a route
// registry exists to make impossible.
//
// It is derived from [api.SetupRoutes] and from the route's own declaration rather than from a
// list of paths, so a second state-creating setup route is covered the day it is added.
func TestSetupRoutes_NoIdempotencyKey_AreRefusedBeforeTheyWrite(t *testing.T) {
	t.Parallel()
	driven := 0
	for _, route := range api.SetupRoutes() {
		if !route.RequiresIdempotencyKey() {
			continue
		}
		driven++
		t.Run(string(route.ID), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			// The body is the one every other test here submits, so the refusal below is about
			// the missing header and cannot be about anything else.
			got := h.do(request{
				Method: route.Method, Path: api.BasePath + route.Path,
				Token: testSetupTok, Body: setupBody,
			})
			h.requireProblem(got, apierr.CodeIdempotencyKeyRequired)
			requireNothingWritten(t, h)

			// And the key is VALIDATED, not merely present: the same rules every other
			// state-creating operation is held to, reached through the same code path.
			bad := h.do(request{
				Method: route.Method, Path: api.BasePath + route.Path,
				Token: testSetupTok, Body: setupBody,
				Headers: map[string]string{api.IdempotencyKeyHeader: "setup\x00run"},
			})
			h.requireProblem(bad, apierr.CodeValidationFailed)
			requireNothingWritten(t, h)
		})
	}
	require.Positive(t, driven,
		"no setup route declares that it creates state; this gate is vacant")
}

// TestRunSetup_ARepeatedRequest_MintsNoSecondOwnerCode answers what `IdempotencyHandler` means
// here, which is not what it means on `/join`.
//
// There is no `idempotency_record` to replay from — that table's `principal_membership_id` is NOT
// NULL and setup has no principal — and the response cannot be reproduced from the database in any
// case, because the owner code is stored only as a hash. What the handler owns instead is
// CONVERGENCE: every step is create-if-absent, and the one step that mints a credential is refused
// outright once the instance has a circle nobody named.
//
// So a retry of a lost response does not quietly mint a second owner code. It is refused, and the
// refusal names the field that resumes the run. The owner code is minted as the LAST thing a run
// does and returned in the response that reports it, so a run that answered a refusal minted
// nothing — which is what makes the assertions below a statement about the ledger and not only
// about the body.
func TestRunSetup_ARepeatedRequest_MintsNoSecondOwnerCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// One key, sent twice, exactly as a client retrying a request whose response it never saw.
	const key = "setup-retry-after-a-lost-response"
	send := func() response {
		return h.do(request{
			Method: http.MethodPost, Path: api.BasePath + "/setup", Token: testSetupTok,
			Body:    setupBody,
			Headers: map[string]string{api.IdempotencyKeyHeader: key},
		})
	}

	first := send()
	require.Equal(t, http.StatusOK, first.Status, "body was: %s", first.Body)
	var one api.SetupResult
	require.NoError(t, json.Unmarshal([]byte(first.Body), &one))
	require.NotEmpty(t, one.OwnerCode)

	second := send()
	h.requireProblem(second, apierr.CodeConflict)
	require.NotContains(t, second.Body, "owner_code",
		"the retry handed back a second credential nobody asked for")
	require.NotContains(t, second.Body, one.OwnerCode)

	// And it left the instance exactly as the first run did.
	state := h.do(setupRequest(routeGET(t), testSetupTok))
	var described api.SetupState
	require.NoError(t, json.Unmarshal([]byte(state.Body), &described))
	require.Len(t, described.Circles, 1, "the retry created a second circle")
	require.Equal(t, one.CircleID, described.Circles[0].ID)
	require.Len(t, described.Providers, 1)
}

// TestRunSetup_ConcurrentRuns_LeaveOneCircleAndTheWinnersInstance is the gate on the race the
// snapshot in [setup.Service.Run] used to lose.
//
// `Describe` reads, and every write after it is a decision made from that read. Two operators — or
// one browser that retried a slow request — could both describe an instance with no circle, and
// the second would go on to overwrite the instance row with its own name and create a second
// circle from a state that had stopped being true.
//
// Each run submits a DIFFERENT instance name and circle name, which is what makes the last
// assertion possible: the instance row must say what the run that succeeded submitted. A run that
// was refused must not have written on its way to being refused.
func TestRunSetup_ConcurrentRuns_LeaveOneCircleAndTheWinnersInstance(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	const runs = 4
	body := func(i int) string {
		return fmt.Sprintf(`{
		  "name": "Instance %[1]d",
		  "public_url": "https://tod-%[1]d.example.com",
		  "provider": {
		    "key": "local", "kind": "local", "display_name": "This server",
		    "acknowledge_weak_revocation": true
		  },
		  "circle": {"name": "Circle %[1]d", "server": "blue"}
		}`, i)
	}

	got := make([]response, runs)
	var released sync.WaitGroup
	released.Add(1)
	var done sync.WaitGroup
	for i := range runs {
		done.Add(1)
		go func() {
			defer done.Done()
			released.Wait()
			got[i] = h.do(request{
				Method: http.MethodPost, Path: api.BasePath + "/setup", Token: testSetupTok,
				Body: body(i),
				Headers: map[string]string{
					api.IdempotencyKeyHeader: fmt.Sprintf("setup-race-%d", i),
				},
			})
		}()
	}
	released.Done()
	done.Wait()

	winners := 0
	winner := -1
	for i, res := range got {
		if res.Status == http.StatusOK {
			winners++
			winner = i
			continue
		}
		require.Equal(t, http.StatusConflict, res.Status,
			"run %d failed for a reason that is not losing the race: %s", i, res.Body)
	}
	require.Equal(t, 1, winners, "more than one concurrent run completed first-run setup")

	state := h.do(setupRequest(routeGET(t), testSetupTok))
	var described api.SetupState
	require.NoError(t, json.Unmarshal([]byte(state.Body), &described))
	require.Len(t, described.Circles, 1, "a losing run created a circle nobody asked for")
	require.Equal(t, fmt.Sprintf("Circle %d", winner), described.Circles[0].Name)
	require.Equal(t, fmt.Sprintf("Instance %d", winner), described.InstanceName,
		"a losing run overwrote the instance row on its way to being refused")
	require.Equal(t, fmt.Sprintf("https://tod-%d.example.com", winner), described.PublicURL)
}
