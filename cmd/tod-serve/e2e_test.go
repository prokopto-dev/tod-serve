package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
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

// ulidPattern finds an id in a console listing. Anchored to the full 26 characters so a truncated
// column would fail the test rather than produce an id the API refuses.
var ulidPattern = regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{26}\b`)

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
	joined, _ := srv.join(t, ownerCode, "Tankguy")
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
	rival, _ := srv.join(t, rivalCode, "Rivalguy")
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
type e2eServer struct {
	handler http.Handler
	// lastHeader is the response header of the most recent call, so a helper can read a
	// `Set-Cookie` off it without every request signature growing a second return value.
	lastHeader http.Header
}

func newE2EServer(t *testing.T, ctx context.Context, path string) *e2eServer {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(ctx, path, log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	svc, err := wire(ctx, db, log, core.Secret(e2ePepper), core.Secret(e2eSessionKey))
	require.NoError(t, err)
	server, err := api.New(api.Config{
		Version: "0.0.0-e2e", Store: db, Auth: svc.authn, Sessions: svc.codec,
		Circles: svc.circles, Members: svc.members, Invites: svc.invites,
		Identities: svc.identity, Catalogue: svc.catalogue,
		Tods: svc.tods, States: svc.states, Invalidator: svc.states,
		Clock: svc.clock, Log: log, IDs: svc.ids,
		OnResponseViolation: func(v api.Violation) { t.Errorf("response contract: %s", v) },
	})
	require.NoError(t, err)
	return &e2eServer{handler: server.Handler()}
}

// sessionFrom reads the browser session out of a response's `Set-Cookie`.
//
// It replaces a helper that encoded a cookie with the server's own codec, which was a scheduled
// deletion: until `/join` and `/sessions` set one, the capability floor was reachable in this test
// only by a credential no browser could ever have obtained. Everything past it was real — the real
// middleware, the real ledger read, the real step-up check — but the credential itself was not, and
// a test that mints its own credential cannot fail the way a broken login does.
func sessionFrom(t *testing.T, header http.Header) string {
	t.Helper()
	for _, c := range (&http.Response{Header: header}).Cookies() {
		if c.Name == auth.SessionCookie {
			require.True(t, c.Secure, "the session cookie must be Secure: __Host- requires it")
			require.True(t, c.HttpOnly, "no script has any reason to read a session")
			require.Equal(t, http.SameSiteLaxMode, c.SameSite,
				"a cross-site POST must not carry the session")
			return c.Value
		}
	}
	require.FailNow(t, "the response set no "+auth.SessionCookie+" cookie")
	return ""
}

func (s *e2eServer) request(
	t *testing.T, method, path, body, token string, wantStatus int,
) []byte {
	t.Helper()
	return s.call(t, e2eRequest{
		Method: method, Path: path, Body: body, Token: token, Want: wantStatus,
	})
}

// e2eRequest is one call. It is a struct rather than eight positional parameters because the
// admin path needs a cookie, an `If-Match` and a body at once, and a call site reading
// `"", "", cookie, "*"` is a call site nobody can check.
type e2eRequest struct {
	Method  string
	Path    string
	Body    string
	Token   string
	Session string
	IfMatch string
	Want    int
}

func (s *e2eServer) call(t *testing.T, r e2eRequest) []byte {
	t.Helper()
	var reader io.Reader
	if r.Body != "" {
		reader = strings.NewReader(r.Body)
	}
	req := httptest.NewRequestWithContext(t.Context(), r.Method, r.Path, reader)
	if r.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// The middle of the range, on every request this test makes: `*/*` is curl's default and what
	// almost every HTTP client sends, and the API once answered 406 to all of them.
	req.Header.Set("Accept", "*/*")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	if r.Session != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: r.Session})
	}
	if r.IfMatch != "" {
		req.Header.Set(api.IfMatchHeader, r.IfMatch)
	}
	if r.Method == http.MethodPost {
		// Hashed rather than spelled: `Idempotency-Key` is capped at 255 characters and a provider
		// body is longer than that. What matters is that a retry of the same call reuses the key,
		// which a hash of the call gives for free.
		sum := sha256.Sum256([]byte(r.Method + " " + r.Path + " " + r.Body))
		req.Header.Set(api.IdempotencyKeyHeader, hex.EncodeToString(sum[:16]))
	}

	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	require.Equal(t, r.Want, rec.Code, "%s %s answered: %s", r.Method, r.Path, rec.Body.String())
	s.lastHeader = rec.Header()
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

// join redeems a code and returns BOTH credentials the one operation mints: the PAT in the body
// and the browser session in the `Set-Cookie`. Both come out of the same request because both
// clients come through the same door — there is no browser-only route to redeem an invite.
func (s *e2eServer) join(t *testing.T, code, displayName string) (joinedResponse, string) {
	t.Helper()
	body := s.post(t, "/api/v1/join", joinBody(code, displayName), "", http.StatusOK)
	var joined joinedResponse
	require.NoError(t, json.Unmarshal(body, &joined))
	return joined, sessionFrom(t, s.lastHeader)
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

// TestEndToEnd_FreshDatabaseToConfiguringAnIdentityProvider is the acceptance path ADR-0012 exists
// for: a brand new instance, and an operator ending up able to add the Discord provider over HTTP.
//
// Before this, `instance.security.manage` was instance-realm and nothing granted it, so
// `/admin/identity-providers` answered 403 to every principal including a stepped-up owner — and
// configuring an identity provider was a command-line operation with no API equivalent at all.
//
// It is one test rather than seven because the thing under test is that the steps COMPOSE. The
// console writes a grant against an identity; the join created that identity; the middleware reads
// the ledger on the next request. Any one of those passing in isolation proves nothing about the
// operator's actual morning.
func TestEndToEnd_FreshDatabaseToConfiguringAnIdentityProvider(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "tod.db")

	require.NoError(t, runCLI(t, "migrate", "--db", path))
	initOut, err := captureCLI(t,
		"init", "--db", path, "--name", "Fresh Instance",
		"--public-url", "https://tod.example.com",
		"--circle", "Ops", "--server", "blue",
		"--local", "--accept-local", "--acknowledge-weak-revocation")
	require.NoError(t, err)
	// The bootstrap tells the operator what the next step is. A binary that leaves somebody with a
	// working circle and an unadministrable instance has not finished the job it started.
	require.Contains(t, initOut, "tod-serve instance identities")
	require.Contains(t, initOut, "instance.owner")

	ownerCode := codePattern.FindString(initOut)
	require.NotEmpty(t, ownerCode)

	// --- no identity yet, so there is nothing to grant to ---------------------------------------
	before, err := captureCLI(t, "instance", "identities", "--db", path)
	require.NoError(t, err)
	require.Contains(t, before, "nobody has joined a circle")

	empty, err := captureCLI(t, "instance", "grants", "--db", path)
	require.NoError(t, err)
	require.Contains(t, empty, "no instance permission has been granted")

	// --- the operator joins, which is what creates the identity ---------------------------------
	srv := newE2EServer(t, ctx, path)
	joined, session := srv.join(t, ownerCode, "Operator")
	require.Equal(t, "owner", joined.Membership.Role)

	identities, err := captureCLI(t, "instance", "identities", "--db", path)
	require.NoError(t, err)
	identityID := ulidPattern.FindString(identities)
	require.NotEmpty(t, identityID, "instance identities printed no id:\n%s", identities)
	require.Contains(t, identities, "local")

	// --- an owner, stepped up, with no grant: refused, and the code says which half failed ------
	// The session is the one `/join` set on the response above: a real credential a browser could
	// have, rather than one this test encoded for itself.
	refused := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Session: session, Want: http.StatusForbidden,
	})
	require.Contains(t, string(refused), `"code":"forbidden"`)

	// A token belonging to the same person is refused differently, because the fix is different:
	// no PAT reaches an instance-realm permission at any scope.
	byToken := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Token: joined.Token.Secret, Want: http.StatusForbidden,
	})
	require.Contains(t, string(byToken), `"code":"session_required"`)

	// --- the console grants it -------------------------------------------------------------------
	granted, err := captureCLI(t, "instance", "grant", "--db", path,
		"--identity", identityID, "--permission", string(authz.PermissionInstanceSecurityManage),
		"--reason", "first operator")
	require.NoError(t, err)
	require.Contains(t, granted, "granted")

	// Granting it twice is refused rather than appending a row where nothing happened.
	_, err = captureCLI(t, "instance", "grant", "--db", path,
		"--identity", identityID, "--permission", string(authz.PermissionInstanceSecurityManage))
	require.Error(t, err)
	require.Contains(t, err.Error(), "already records")

	// --- and the same session, unchanged, now reaches the admin surface -------------------------
	listed := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Session: session, Want: http.StatusOK,
	})
	var page struct {
		Items []api.AdminIdentityProvider `json:"items"`
	}
	require.NoError(t, json.Unmarshal(listed, &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, "local", page.Items[0].Key)

	// --- configuring the Discord provider, which is the whole point ------------------------------
	const clientSecret = "e2e-discord-client-secret"
	createdBody := srv.call(t, e2eRequest{
		Method: http.MethodPost, Path: "/api/v1/admin/identity-providers", Session: session,
		Body: `{"key":"discord","kind":"discord","display_name":"Discord",
		        "client_id":"1234567890","client_secret":` + quote(clientSecret) + `,
		        "redirect_uri":"https://tod.example.com/api/v1/auth/callback/discord",
		        "token_endpoint":"https://discord.com/api/oauth2/token","enabled":true}`,
		Want: http.StatusOK,
	})
	require.NotContains(t, string(createdBody), clientSecret,
		"the client secret came back out of the API it went into")
	var created api.AdminIdentityProviderResponse
	require.NoError(t, json.Unmarshal(createdBody, &created))
	require.Equal(t, "discord", created.Key)
	require.True(t, created.Enabled)
	require.True(t, created.ClientSecretSet)
	// `verifiable_subject` is a CHECK against `kind` and was never in the request. A caller cannot
	// assert it, which is why revocation strength can be trusted to be derived.
	require.True(t, created.VerifiableSubject)

	// --- and changing it, under If-Match ---------------------------------------------------------
	current := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Session: session, Want: http.StatusOK,
	})
	require.NoError(t, json.Unmarshal(current, &page))
	require.Len(t, page.Items, 2)

	etag, err := api.ETagOf(providerByKey(t, page.Items, "discord"))
	require.NoError(t, err)
	renamedBody := srv.call(t, e2eRequest{
		Method: http.MethodPatch, Session: session, IfMatch: etag,
		Path: "/api/v1/admin/identity-providers/" + created.ID,
		Body: `{"display_name":"Discord (guild gate)"}`,
		Want: http.StatusOK,
	})
	var renamed api.AdminIdentityProviderResponse
	require.NoError(t, json.Unmarshal(renamedBody, &renamed))
	require.Equal(t, "Discord (guild gate)", renamed.DisplayName)
	require.NotContains(t, string(renamedBody), clientSecret)
	// The secret survived an update that did not mention it. Omitting a write-only field must not
	// clear it, or every rename would silently break the OAuth application.
	require.True(t, renamed.ClientSecretSet)

	// The kind is immutable, and saying so is why the field is accepted at all: ignoring it would
	// let a client believe a provider had been retyped, and kind decides verifiable_subject.
	immutable := srv.call(t, e2eRequest{
		Method: http.MethodPatch, Session: session, IfMatch: "*",
		Path: "/api/v1/admin/identity-providers/" + created.ID,
		Body: `{"kind":"oidc"}`, Want: http.StatusUnprocessableEntity,
	})
	require.Contains(t, string(immutable), `"code":"field_immutable"`)

	// --- and the grant is revocable, which is what makes granting it worth doing ------------------
	_, err = captureCLI(t, "instance", "revoke", "--db", path,
		"--identity", identityID, "--permission", string(authz.PermissionInstanceSecurityManage),
		"--reason", "handed over")
	require.NoError(t, err)

	after := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Session: session, Want: http.StatusForbidden,
	})
	require.Contains(t, string(after), `"code":"forbidden"`)

	// Both decisions survive. The ledger IS the audit record for an instance permission, because
	// audit_log.circle_id is NOT NULL and an instance grant belongs to no circle.
	history, err := captureCLI(t, "instance", "grants", "--db", path, "--history")
	require.NoError(t, err)
	require.Contains(t, history, "granted")
	require.Contains(t, history, "revoked")
	require.Contains(t, history, "first operator")
	require.Contains(t, history, "handed over")
	require.Contains(t, history, "console")
}

// providerByKey finds one provider in a listing, failing rather than returning a zero value: a
// missing row would otherwise be asserted against as an empty one.
func providerByKey(
	t *testing.T, items []api.AdminIdentityProvider, key string,
) api.AdminIdentityProvider {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("no provider %q in %v", key, items)
	return api.AdminIdentityProvider{}
}
