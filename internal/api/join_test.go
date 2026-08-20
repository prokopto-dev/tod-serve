package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

const (
	joinPath     = api.BasePath + "/join"
	sessionsPath = api.BasePath + "/sessions"
)

// `Idempotency-Key` is required on every POST that creates domain state, and these two create the
// most consequential state there is — a membership and a token. The header is required even though
// no membership principal exists yet to key `(principal, key)` on, so a client has ONE rule rather
// than two.
func TestJoinAndSessions_RequireAnIdempotencyKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, path, body string }{
		{"join", joinPath, `{"invite_code":"TODI-4KQ7M-9XPB2","provider":"local","credential":{"kind":"none"},"display_name":"Tankguy"}`},
		{"sessions", sessionsPath, `{"circle_id":"01K3TGT8N9M4X0Q7R2VB6C5D1E","provider":"local","credential":{"kind":"none"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			got := h.do(request{Method: http.MethodPost, Path: tc.path, Body: tc.body})
			h.requireProblem(got, apierr.CodeIdempotencyKeyRequired)
			require.NotEmpty(t, got.Problem.Errors)
			require.Equal(t, "header.Idempotency-Key", got.Problem.Errors[0].Location)
		})
	}
}

// ADR-0007's stated cost: the credential union is validated in the SERVICE rather than purely in
// the schema, so the errors it produces have to stay specific — `errors[].location` pointing into
// the union rather than "body invalid".
func TestJoin_TheCredentialUnion_FailsWithASpecificLocation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		location string
	}{
		{
			name:     "a kind this server does not accept",
			body:     `{"invite_code":"TODI-4KQ7M-9XPB2","provider":"local","credential":{"kind":"magic"}}`,
			location: "body.credential.kind",
		},
		{
			name:     "a local join with no display name, which is the one place it is required",
			body:     `{"invite_code":"CODE","provider":"local","credential":{"kind":"none"}}`,
			location: "body.display_name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			code := h.seedJoinableCircle()
			body := replaceCode(tt.body, code)

			got := h.do(request{
				Method: http.MethodPost, Path: joinPath, Body: body,
				Headers: map[string]string{api.IdempotencyKeyHeader: "join"},
			})
			require.Equal(t, http.StatusUnprocessableEntity, got.Status, got.Body)
			require.NotEmpty(t, got.Problem.Errors)
			require.Equal(t, tt.location, got.Problem.Errors[0].Location)
		})
	}
}

// A `circle_id` on `/sessions` that is not a ULID answers exactly what an unknown circle does. It
// is the ONE public route that takes a circle identifier, and it takes it WITH a credential — so
// every failure has to look the same or the route becomes a circle-existence oracle.
func TestSessions_EveryFailure_IsTheSame404(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedJoinableCircle()

	// Every shape answers the same 404: a malformed id, a well-formed id nobody uses, and an
	// absent one. Anything narrower would tell a prober their guess was at least well-formed.
	for _, circleID := range []string{
		"not-a-ulid",
		"01K3TGT8N9M4X0Q7R2VB6C5D1E",
		"",
	} {
		got := h.do(request{
			Method: http.MethodPost, Path: sessionsPath,
			Headers: map[string]string{api.IdempotencyKeyHeader: "sessions-" + circleID},
			Body: `{"circle_id":"` + circleID + `","provider":"local",` +
				`"credential":{"kind":"none"},"display_name":"Tankguy"}`,
		})
		require.Equal(t, http.StatusNotFound, got.Status,
			"circle_id %q answered: %s", circleID, got.Body)
		require.Equal(t, apierr.CodeNotFound, got.Problem.Code)
	}
}

// The end-to-end join, through the wire, with the `Accept` header a real client sends.
func TestJoin_ThroughTheWire_MintsAMembershipAndAToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	code := h.seedJoinableCircle()

	got := h.do(request{
		Method: http.MethodPost, Path: joinPath,
		Headers: map[string]string{
			api.IdempotencyKeyHeader: "join",
			"Accept":                 "*/*",
		},
		Body: `{"invite_code":"` + code + `","provider":"local",` +
			`"credential":{"kind":"none"},"display_name":"Tankguy",` +
			`"client":{"name":"nparse-plus-tod","version":"1.2.0"}}`,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var joined struct {
		Created    bool `json:"created"`
		Membership struct {
			Role        string `json:"display_name"`
			DisplayName string `json:"role"`
		} `json:"membership"`
		Token struct {
			Secret string   `json:"token"`
			Name   string   `json:"name"`
			Scopes []string `json:"scopes"`
		} `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(got.Body), &joined))
	require.True(t, joined.Created)
	require.NotEmpty(t, joined.Token.Secret)
	require.Equal(t, "nparse-plus-tod 1.2.0", joined.Token.Name,
		"a device name is what somebody scanning their own token list can act on")
	require.NotEmpty(t, joined.Token.Scopes)

	// The token works on a circle-scoped route straight away.
	me := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/me",
		Token: coreSecret(joined.Token.Secret),
	})
	require.Equal(t, http.StatusOK, me.Status, me.Body)
	require.Contains(t, me.Body, string(authz.RoleOwner))
}

// **The middle of the range, on the routes this milestone added.** `Accept: */*` is curl's default
// and what almost every HTTP client sends; the whole API once answered 406 to all of them, and the
// suite missed it because it only ever exercised the two ENDS — absent, and exactly what we serve.
func TestNewRoutes_TheAcceptHeadersRealClientsSend_AreSatisfied(t *testing.T) {
	t.Parallel()
	accepts := []struct {
		name   string
		header string
		want   int
	}{
		{"curl's default", "*/*", http.StatusOK},
		{"a browser's", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", http.StatusOK},
		{"a JSON client's", "application/json", http.StatusOK},
		{"a JSON client that also takes anything", "application/json, */*;q=0.1", http.StatusOK},
		{"a client with a charset parameter", "application/json; charset=utf-8", http.StatusOK},
		{"JSON excluded explicitly", "application/json;q=0, */*;q=0", http.StatusNotAcceptable},
		{"only XML", "application/xml", http.StatusNotAcceptable},
	}
	for _, tc := range accepts {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			mine := h.seedCircle("Mine")
			token := h.seedToken(h.seedMember(mine, authz.RoleOwner), allScopes()...)
			code := h.seedJoinableCircle()

			// A circle-scoped read and a public POST, because the negotiation runs at the edge and
			// a fix that only reached one of them would be half a fix.
			for _, probe := range []request{
				{Method: http.MethodGet, Path: circlePath(mine), Token: token},
				{
					Method: http.MethodPost, Path: api.BasePath + "/invites/preview",
					Body: `{"code":"` + code + `"}`,
				},
			} {
				probe.Headers = map[string]string{"Accept": tc.header}
				got := h.do(probe)
				require.Equal(t, tc.want, got.Status,
					"%s %s with Accept %q gave: %s", probe.Method, probe.Path, tc.header, got.Body)
			}
		})
	}
}

// seedJoinableCircle writes a circle that accepts `local` and returns a live owner code for it —
// the shape `tod-serve init` produces, so the tests above exercise the bootstrap path a real
// operator takes rather than one only a fixture can reach.
func (h *harness) seedJoinableCircle() string {
	h.t.Helper()
	view, err := h.circles.Create(h.t.Context(), circle.CreateRequest{
		Name: "Joinable", Server: core.Server(schemaenum.ServerBlue),
	})
	require.NoError(h.t, err)

	// The provider row has to exist before the circle can accept it, and `local` is never
	// auto-accepted — an owner reaches for it, which here is this line.
	providerKey := h.seedProviderKey()
	updated, err := h.circles.SetProviders(h.t.Context(), view.ID, circle.SetProvidersRequest{
		Providers:                 []circle.AcceptedProvider{{Key: providerKey}},
		AcknowledgeWeakRevocation: true,
	})
	require.NoError(h.t, err)
	require.NotEmpty(h.t, updated.AcceptedProviders)

	code, _, err := h.invites.MintOwnerGrant(h.t.Context(), view.ID)
	require.NoError(h.t, err)
	return string(code)
}

// seedProviderKey returns the wire key of the harness's `local` provider row, writing it first if
// this harness has not needed one yet.
func (h *harness) seedProviderKey() string {
	h.t.Helper()
	row, err := h.store.Queries().GetIdentityProvider(h.t.Context(), h.seedProvider().String())
	require.NoError(h.t, err)
	return row.Key
}

func replaceCode(body, code string) string {
	return strings.ReplaceAll(strings.ReplaceAll(body, "TODI-4KQ7M-9XPB2", code), `"CODE"`, `"`+code+`"`)
}

func coreSecret(s string) core.Secret { return core.Secret(s) }
