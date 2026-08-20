package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
)

// The credential material the end-to-end test runs with. They are constants rather than random
// values so that a failure reproduces.
const (
	e2ePepper     = "e2e-token-pepper"
	e2eSessionKey = "e2e-session-key"
)

// codePattern finds the owner code the CLI prints. It matches the canonical form only: a test that
// accepted a loose spelling would pass while the CLI printed something no client could redeem.
var codePattern = regexp.MustCompile(`TODI-[0-9A-HJKMNP-TV-Z]{5}-[0-9A-HJKMNP-TV-Z]{5}`)

// TestEndToEnd_InitToJoinToACircleScopedRoute is the acceptance path, over real SQLite in a temp
// directory and the real HTTP server, with nothing stubbed.
//
// It is one test rather than six because the thing being asserted is that the steps COMPOSE:
// every one of them passes in isolation in another file, and the failure this catches is the one
// where the code the CLI prints is not the code the API resolves, or the membership the join
// creates is not the one the token authenticates.
//
// The last step is the point. A second circle's principal must get 404 — never 403 — on the first
// circle's resources, because a 403 confirms the circle exists and a circle's existence is part of
// what it is hiding.
func TestEndToEnd_InitToJoinToACircleScopedRoute(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "tod.db")

	// --- migrate, then init: the instance, the local provider, the first circle -----------------
	require.NoError(t, runCLI(t, "migrate", "--db", path))

	initOut, err := captureCLI(t,
		"init", "--db", path,
		"--name", "Test Instance",
		"--public-url", "https://tod.example.com",
		"--circle", "Riot Blue", "--server", "blue",
		"--local", "--accept-local", "--acknowledge-weak-revocation")
	require.NoError(t, err)
	require.Contains(t, initOut, "Riot Blue")
	// The weak-revocation warning is on the bootstrap path, where the operator is looking.
	require.Contains(t, initOut, "ADVISORY")

	ownerCode := codePattern.FindString(initOut)
	require.NotEmpty(t, ownerCode, "init printed no owner code:\n%s", initOut)
	// The join link carries the code in the FRAGMENT, which no browser transmits — not to us, not
	// to a proxy, and not in a `Referer`. A query string would put it in every access log between
	// the officer and the server.
	require.Contains(t, initOut, "/join#"+ownerCode)
	require.NotContains(t, initOut, "/join?")

	// --- a second circle, so there is a tenant to be refused -----------------------------------
	secondOut, err := captureCLI(t,
		"circle", "create", "--db", path, "--name", "Rival Green", "--server", "green",
		"--accept-local", "--acknowledge-weak-revocation")
	require.NoError(t, err)
	rivalCode := codePattern.FindString(secondOut)
	require.NotEmpty(t, rivalCode)

	// --- the server, wired exactly as `serve` wires it ------------------------------------------
	srv := newE2EServer(t, ctx, path)

	// --- preview: what a code holder is told BEFORE joining --------------------------------------
	var preview api.InvitePreview
	previewBody := srv.post(t, "/api/v1/invites/preview",
		`{"code":`+quote(ownerCode)+`}`, "", http.StatusOK)
	require.NoError(t, json.Unmarshal(previewBody, &preview))
	require.Equal(t, "Riot Blue", preview.Circle.Name)
	require.Equal(t, "blue", preview.Circle.Server)
	require.Equal(t, "owner", preview.GrantedRole)
	// The preview says the circle is weakly revocable before anybody joins it. That field is the
	// whole reason revocation strength is in the API rather than in the documentation.
	require.Equal(t, "weak", preview.RevocationStrength)
	require.Contains(t, preview.WeakProviders, "local")

	// --- join: redeem the code with a local credential and get a PAT ----------------------------
	joined := srv.join(t, ownerCode, "Tankguy")
	require.True(t, joined.Created)
	require.Equal(t, "owner", joined.Membership.Role)
	require.Equal(t, "Riot Blue", joined.Circle.Name)
	require.NotEmpty(t, joined.Token.Secret)
	require.True(t, strings.HasPrefix(joined.Token.Secret, "tods_pat_"))

	// The owner code is single-use. A second redemption is refused rather than making a second
	// owner, and that refusal is a compare-and-swap in SQL rather than a check in Go.
	srv.post(t, "/api/v1/join", joinBody(ownerCode, "Someone Else"), "", http.StatusConflict)

	// --- a PAT calling a circle-scoped route ----------------------------------------------------
	own := srv.get(t, "/api/v1/circles/"+joined.Circle.ID.String(),
		joined.Token.Secret, http.StatusOK)
	var mine api.CircleResponse
	require.NoError(t, json.Unmarshal(own, &mine))
	require.Equal(t, "Riot Blue", mine.Name)
	require.Equal(t, "weak", mine.RevocationStrength)

	// --- and the second circle's principal, refused with 404 ------------------------------------
	rival := srv.join(t, rivalCode, "Rivalguy")
	require.NotEqual(t, joined.Circle.ID, rival.Circle.ID)

	body := srv.get(t, "/api/v1/circles/"+joined.Circle.ID.String(),
		rival.Token.Secret, http.StatusNotFound)
	require.Contains(t, string(body), `"code":"not_found"`,
		"a cross-circle read must be 404 not_found; a 403 would confirm the circle exists")

	// The same rule over every circle-scoped route this milestone serves, not only the one above.
	for _, probe := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/circles/" + joined.Circle.ID.String() + "/members"},
		{http.MethodGet, "/api/v1/circles/" + joined.Circle.ID.String() + "/invites"},
	} {
		srv.request(t, probe.method, probe.path, "",
			rival.Token.Secret, http.StatusNotFound)
	}
}

// e2eServer is the wired API over a real database, with the response contract checked on every
// request the test makes.
type e2eServer struct{ handler http.Handler }

func newE2EServer(t *testing.T, ctx context.Context, path string) *e2eServer {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(ctx, path, log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	svc, err := wire(ctx, db, log, core.Secret(e2ePepper), core.Secret(e2eSessionKey))
	require.NoError(t, err)
	server, err := api.New(api.Config{
		Version: "0.0.0-e2e", Store: db, Auth: svc.authn,
		Circles: svc.circles, Members: svc.members, Invites: svc.invites,
		Identities: svc.identity, Catalogue: svc.catalogue,
		Invalidator: api.UnprojectedTimers{},
		Clock:       svc.clock, Log: log, IDs: svc.ids,
		OnResponseViolation: func(v api.Violation) { t.Errorf("response contract: %s", v) },
	})
	require.NoError(t, err)
	return &e2eServer{handler: server.Handler()}
}

func (s *e2eServer) request(
	t *testing.T, method, path, body, token string, wantStatus int,
) []byte {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// The middle of the range, on every request this test makes: `*/*` is curl's default and what
	// almost every HTTP client sends, and the API once answered 406 to all of them.
	req.Header.Set("Accept", "*/*")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		req.Header.Set(api.IdempotencyKeyHeader, method+" "+path+" "+body)
	}

	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	require.Equal(t, wantStatus, rec.Code, "%s %s answered: %s", method, path, rec.Body.String())
	return rec.Body.Bytes()
}

func (s *e2eServer) post(t *testing.T, path, body, token string, want int) []byte {
	t.Helper()
	return s.request(t, http.MethodPost, path, body, token, want)
}

func (s *e2eServer) get(t *testing.T, path, token string, want int) []byte {
	t.Helper()
	return s.request(t, http.MethodGet, path, "", token, want)
}

func (s *e2eServer) join(t *testing.T, code, displayName string) joinedResponse {
	t.Helper()
	body := s.post(t, "/api/v1/join", joinBody(code, displayName), "", http.StatusOK)
	var joined joinedResponse
	require.NoError(t, json.Unmarshal(body, &joined))
	return joined
}

// joinedResponse mirrors what `/join` answers. It is declared here rather than reusing the service
// type so that the test asserts against the JSON a client actually receives.
type joinedResponse struct {
	Created    bool `json:"created"`
	Membership struct {
		ID                 core.MembershipID `json:"id"`
		Role               string            `json:"role"`
		DisplayName        string            `json:"display_name"`
		RevocationStrength string            `json:"revocation_strength"`
	} `json:"membership"`
	Circle struct {
		ID                 core.CircleID `json:"id"`
		Name               string        `json:"name"`
		Server             string        `json:"server"`
		RevocationStrength string        `json:"revocation_strength"`
	} `json:"circle"`
	Token struct {
		Secret string   `json:"token"`
		Prefix string   `json:"token_prefix"`
		Scopes []string `json:"scopes"`
	} `json:"token"`
}

func joinBody(code, displayName string) string {
	return `{"invite_code":` + quote(code) +
		`,"provider":"local","credential":{"kind":"none"},"display_name":` + quote(displayName) +
		`,"client":{"name":"e2e","version":"1.0.0"}}`
}

func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// runCLI executes one command and discards its output.
func runCLI(t *testing.T, args ...string) error {
	t.Helper()
	_, err := captureCLI(t, args...)
	return err
}

// captureCLI executes one command and returns everything it printed.
func captureCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := newRootCommand()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	return buf.String(), err
}
