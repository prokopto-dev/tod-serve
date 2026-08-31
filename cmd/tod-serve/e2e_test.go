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
	// e2eSetupToken arms first-run setup. It is what an operator writes into `.env` beside the
	// two above, and the only thing between a fresh public instance and whoever loads it first.
	e2eSetupToken = "e2e-setup-token"
	// e2ePublicURL is what `.env` sets before `serve` starts. The wizard prefills its form from it
	// and the OAuth callback returns to it, so an instance meant to be set up in a browser has to
	// know it before there is any row to read it from.
	e2ePublicURL = "https://tod.example.com"
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
	return newE2EServerWith(t, ctx, path, core.Secret(e2eSetupToken))
}

// newE2EServerWithoutSetupToken wires the same binary with `TOD_SETUP_TOKEN` unset, which is the
// state an upgraded instance and a finished one are both in.
func newE2EServerWithoutSetupToken(t *testing.T, ctx context.Context, path string) *e2eServer {
	t.Helper()
	return newE2EServerWith(t, ctx, path, "")
}

func newE2EServerWith(
	t *testing.T, ctx context.Context, path string, setupToken core.Secret,
) *e2eServer {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(ctx, path, log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	// Passed rather than read from the environment: `$TOD_PUBLIC_URL` is part of the `.env` step
	// every deployment does before `serve` starts, and `t.Setenv` cannot be used in a package
	// whose tests are all parallel. This is the value that file would have supplied.
	svc, err := wire(ctx, db, log,
		core.Secret(e2ePepper), core.Secret(e2eSessionKey), e2ePublicURL)
	require.NoError(t, err)
	server, err := api.New(api.Config{
		Version: "0.0.0-e2e", Store: db, Auth: svc.authn, Sessions: svc.codec,
		Circles: svc.circles, Members: svc.members, Invites: svc.invites,
		Identities: svc.identity, Catalogue: svc.catalogue, InstanceSettings: svc.settings,
		Tods: svc.tods, States: svc.states, Invalidator: svc.states,
		Clock: svc.clock, Log: log, IDs: svc.ids,
		Setup: svc.setup, SetupToken: api.SetupConfig{Token: setupToken},
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
//
// Since ADR-0016 the FIRST redemption needs no console step at all: redeeming an owner grant while
// nobody administers the instance grants that identity `instance.owner` in the join's own
// transaction. So the console arm is driven here by a SECOND operator, on a second circle, after
// the window has closed — which is the state that arm actually exists for, and the only one left
// in which a grant has to be written by hand.
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

	// And `doctor` says so, which is where an operator looks. An instance nobody can administer is
	// a broken instance even when every HTTP check passes, and nothing said it before: the console
	// hides the Instance nav entry rather than explaining it and every command here exits 0.
	unadministrable, err := captureCLI(t, "doctor", "--db", path)
	require.Error(t, err, "doctor exited 0 on an instance nobody can administer:\n%s", unadministrable)
	require.Contains(t, unadministrable, "nobody can administer this instance")
	require.Contains(t, unadministrable, string(authz.PermissionInstanceOwner),
		"doctor names the problem and not the fix")

	// --- the operator joins, which is what creates the identity ---------------------------------
	srv := newE2EServer(t, ctx, path)
	joined, session := srv.join(t, ownerCode, "Operator")
	require.Equal(t, "owner", joined.Membership.Role)

	identities, err := captureCLI(t, "instance", "identities", "--db", path)
	require.NoError(t, err)
	identityID := ulidPattern.FindString(identities)
	require.NotEmpty(t, identityID, "instance identities printed no id:\n%s", identities)
	require.Contains(t, identities, "local")

	// --- redeeming the owner grant made them the administrator, with no console step ------------
	// The LEDGER holds one decision, not five. What was decided and what it reaches are two
	// different facts, and storing the expansion would leave four rows behind on a revocation.
	ledger, err := captureCLI(t, "instance", "grants", "--db", path)
	require.NoError(t, err)
	require.Contains(t, ledger, string(authz.PermissionInstanceOwner))
	require.NotContains(t, ledger, string(authz.PermissionInstanceSecurityManage))
	// Recorded as decided by NOBODY: the instance had no administrator and somebody presented the
	// code, which is a fact about the instance rather than a person's judgement.
	require.Contains(t, ledger, "console")
	require.Contains(t, ledger, "first administrator")

	// A token belonging to the same person is still refused, because the fix is different: no PAT
	// reaches an instance-realm permission at any scope, whatever the ledger says.
	byToken := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Token: joined.Token.Secret, Want: http.StatusForbidden,
	})
	require.Contains(t, string(byToken), `"code":"session_required"`)

	// --- and the session `/join` set reaches the admin surface immediately ----------------------
	// The session is the one `/join` set on the response above: a real credential a browser could
	// have, rather than one this test encoded for itself.
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

	// And doctor now agrees, reading the same expansion the request above did. A doctor counting
	// raw ledger rows would report a problem this operator had already fixed, and the two
	// disagreeing about one database is worse than either answer alone.
	administrable, err := captureCLI(t, "doctor", "--db", path)
	require.NoError(t, err, "doctor still reports a problem after the first redemption:\n%s",
		administrable)
	require.Contains(t, administrable, "1 identity can administer this instance")

	// --- a SECOND operator: the window is shut, so the console is the only way in ---------------
	// This is the arm ADR-0012 is about, driven in the state it exists for. The bootstrap branch
	// is derived from "nobody administers this instance" and that is now false, so redeeming a
	// second owner code makes an owner of a CIRCLE and nothing more.
	secondOut, err := captureCLI(t, "circle", "create", "--db", path,
		"--name", "Ops Two", "--server", "green",
		"--accept-local", "--acknowledge-weak-revocation")
	require.NoError(t, err)
	secondCode := codePattern.FindString(secondOut)
	require.NotEmpty(t, secondCode)

	_, secondSession := srv.join(t, secondCode, "Second")
	secondRefused := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Session: secondSession, Want: http.StatusForbidden,
	})
	require.Contains(t, string(secondRefused), `"code":"forbidden"`,
		"the bootstrap branch fired twice; it is derived and must close for good")

	stillOne, err := captureCLI(t, "doctor", "--db", path)
	require.NoError(t, err)
	require.Contains(t, stillOne, "1 identity can administer this instance")

	// --- the console grants it -------------------------------------------------------------------
	// `instance.owner`, which is what `circle create` printed above and what the deployment runbook
	// says, rather than the narrower key this route declares. Those were different commands until
	// `instance.owner` expanded to the instance realm: the grant the operator was told to make
	// wrote a durable, audited decision that nothing consulted, and the next thing they were told
	// to do answered 403. Driving the DOCUMENTED key is the point of this test.
	secondIdentity := identityNamed(t, path, "Second")
	granted, err := captureCLI(t, "instance", "grant", "--db", path,
		"--identity", secondIdentity, "--permission", string(authz.PermissionInstanceOwner),
		"--reason", "second operator")
	require.NoError(t, err)
	require.Contains(t, granted, "granted")

	// Granting it twice is refused rather than appending a row where nothing happened.
	_, err = captureCLI(t, "instance", "grant", "--db", path,
		"--identity", secondIdentity, "--permission", string(authz.PermissionInstanceOwner))
	require.Error(t, err)
	require.Contains(t, err.Error(), "already records")

	secondListed := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Session: secondSession, Want: http.StatusOK,
	})
	require.Contains(t, string(secondListed), `"key":"local"`)

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
	// Both of them, because there are two administrators now and doctor counts identities rather
	// than rows: revoking one and asserting nobody can administer the instance would be a test
	// that passed on a miscount.
	for _, identity := range []string{identityID, secondIdentity} {
		_, err = captureCLI(t, "instance", "revoke", "--db", path,
			"--identity", identity, "--permission", string(authz.PermissionInstanceOwner),
			"--reason", "handed over")
		require.NoError(t, err)
	}

	after := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Session: session, Want: http.StatusForbidden,
	})
	require.Contains(t, string(after), `"code":"forbidden"`)
	afterSecond := srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/admin/identity-providers",
		Session: secondSession, Want: http.StatusForbidden,
	})
	require.Contains(t, string(afterSecond), `"code":"forbidden"`)

	// The expansion goes away with the decision that made it, and doctor says so again. Without
	// this the whole expansion could be one-way — granted once and never taken back — which is the
	// property ADR-0012 chose an append-only ledger over a capability list to avoid.
	revoked, err := captureCLI(t, "doctor", "--db", path)
	require.Error(t, err, "doctor exited 0 after the last administrator was revoked:\n%s", revoked)
	require.Contains(t, revoked, "nobody can administer this instance")

	// Both decisions survive. The ledger IS the audit record for an instance permission, because
	// audit_log.circle_id is NOT NULL and an instance grant belongs to no circle.
	history, err := captureCLI(t, "instance", "grants", "--db", path, "--history")
	require.NoError(t, err)
	require.Contains(t, history, "granted")
	require.Contains(t, history, "revoked")
	require.Contains(t, history, "first administrator")
	require.Contains(t, history, "second operator")
	require.Contains(t, history, "handed over")
	require.Contains(t, history, "console")
}

// identityNamed finds one identity's id in `instance identities` by the display name a join gave
// it. Scanning for a name rather than taking the first ULID on the page is what makes a test with
// two operators in it mean anything: `ulidPattern.FindString` over the whole listing returns
// whichever row sorted first, which is not the row the caller asked about.
func identityNamed(t *testing.T, path, displayName string) string {
	t.Helper()
	out, err := captureCLI(t, "instance", "identities", "--db", path)
	require.NoError(t, err)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, displayName) {
			continue
		}
		id := ulidPattern.FindString(line)
		require.NotEmpty(t, id, "no id on the row for %q: %s", displayName, line)
		return id
	}
	require.FailNow(t, "no identity named "+displayName+" in:\n"+out)
	return ""
}

// TestEndToEnd_SetupWizardToConfiguringAnIdentityProvider is the acceptance path for ADR-0016,
// and it is the whole reason the wizard exists.
//
// It starts where an operator starts — `migrate`, and nothing else — and finishes holding a
// BROWSER SESSION that can register an identity provider. Anything less would prove the routes
// exist rather than that setup works: `/admin/identity-providers` is instance-realm, session-only
// and in the capability floor, so reaching it means the wizard produced a code, the code produced
// an identity, and the redemption produced the `instance.owner` grant that makes the instance
// administrable. No `docker compose run` anywhere in it, which is the point.
//
// It is one test rather than six for the same reason the two above are: the failure it catches is
// the one where the steps do not COMPOSE.
func TestEndToEnd_SetupWizardToConfiguringAnIdentityProvider(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "tod.db")

	// --- migrate, and NOTHING else. No init, no circle create, no instance grant --------------
	require.NoError(t, runCLI(t, "migrate", "--db", path))
	srv := newE2EServer(t, ctx, path)

	// --- what a browser reads before it has any credential --------------------------------------
	var meta api.ServerMeta
	require.NoError(t, json.Unmarshal(srv.get(t, "/api/v1/meta", "", http.StatusOK), &meta))
	require.False(t, meta.Configured)
	require.True(t, meta.SetupAvailable, "a fresh instance with a setup token routes to /setup")

	// --- the wizard refuses a stranger, and says nothing about which half is missing ------------
	srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/setup", Want: http.StatusNotFound,
	})
	srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/setup",
		Token: e2eSetupToken + "x", Want: http.StatusNotFound,
	})

	// --- one form: instance, provider, circle, catalogue seed -----------------------------------
	runBody := `{"name":"Wizard Instance","public_url":"https://tod.example.com",
	             "provider":{"key":"local","kind":"local","display_name":"This server",
	                         "acknowledge_weak_revocation":true},
	             "circle":{"name":"Riot Blue","server":"blue"}}`
	var result api.SetupResult
	require.NoError(t, json.Unmarshal(srv.call(t, e2eRequest{
		Method: http.MethodPost, Path: "/api/v1/setup", Token: e2eSetupToken,
		Body: runBody, Want: http.StatusOK,
	}), &result))
	require.Equal(t, "Riot Blue", result.CircleName)
	// The circle accepts `local`, so revocation in it is advisory and the wizard says so at the
	// moment the operator chose it rather than in a document they have not opened.
	require.Equal(t, "weak", result.RevocationStrength)
	require.Positive(t, result.RaidTargetsAdded, "the catalogue seed did not run")

	ownerCode := codePattern.FindString(result.OwnerCode)
	require.Equal(t, result.OwnerCode, ownerCode, "the wizard returned a code no client can redeem")
	// The browser is sent to the code in the FRAGMENT. A fragment reaches no server, no proxy and
	// no `Referer` — the same rule the CLI's printed link obeys.
	require.Equal(t, "/join#"+ownerCode, result.JoinPath)
	require.NotContains(t, result.JoinPath, "?")

	// --- the operator redeems it, exactly as the browser would ----------------------------------
	joined, session := srv.join(t, ownerCode, "Operator")
	require.True(t, joined.Created)
	require.Equal(t, "owner", joined.Membership.Role)
	require.Equal(t, result.CircleID, joined.Circle.ID)

	// --- and THAT is what made them an administrator, with no console step ----------------------
	grants, err := captureCLI(t, "instance", "grants", "--db", path)
	require.NoError(t, err)
	require.Contains(t, grants, string(authz.PermissionInstanceOwner))
	require.Contains(t, grants, "granted")
	// Written by nobody: the instance had no administrator and somebody presented the code, which
	// is a fact about the instance rather than a person's decision.
	require.Contains(t, grants, "console")

	// The window is shut, and it shut on the GRANT rather than on a flag anybody set.
	require.NoError(t, json.Unmarshal(srv.get(t, "/api/v1/meta", "", http.StatusOK), &meta))
	require.True(t, meta.Configured)
	require.False(t, meta.SetupAvailable)
	srv.call(t, e2eRequest{
		Method: http.MethodGet, Path: "/api/v1/setup",
		Token: e2eSetupToken, Want: http.StatusConflict,
	})

	// --- the acceptance criterion: this session configures an identity provider -----------------
	const clientSecret = "e2e-wizard-discord-secret"
	created := srv.call(t, e2eRequest{
		Method: http.MethodPost, Path: "/api/v1/admin/identity-providers", Session: session,
		Body: `{"key":"discord","kind":"discord","display_name":"Discord",
		        "client_id":"1234567890","client_secret":` + quote(clientSecret) + `,
		        "redirect_uri":"https://tod.example.com/api/v1/auth/callback/discord",
		        "token_endpoint":"https://discord.com/api/oauth2/token","enabled":true}`,
		Want: http.StatusOK,
	})
	require.NotContains(t, string(created), clientSecret)
	var provider api.AdminIdentityProviderResponse
	require.NoError(t, json.Unmarshal(created, &provider))
	require.Equal(t, "discord", provider.Key)
	require.True(t, provider.VerifiableSubject)

	// --- and the CLI path still exists, because it is the way back ------------------------------
	// `init` refuses a second instance rather than overwriting one; `circle create` still works.
	_, err = captureCLI(t, "init", "--db", path, "--name", "Second")
	require.Error(t, err)
	require.Contains(t, err.Error(), "already initialised")
	second, err := captureCLI(t, "circle", "create", "--db", path,
		"--name", "Rival Green", "--server", "green")
	require.NoError(t, err)
	require.NotEmpty(t, codePattern.FindString(second))
}

// TestEndToEnd_SetupWizard_WithoutATokenTheInstanceIsUnreachable is the first refusal at the
// binary's own boundary rather than at the API's.
//
// `serve` reads `TOD_SETUP_TOKEN` from the environment and passes whatever it finds; an operator
// who never set one, or who deleted the line after setting up, gets an instance where the wizard
// refuses everybody — including a caller presenting the empty string, which is what the unset
// value would compare equal to if the comparison were the only check.
func TestEndToEnd_SetupWizard_WithoutATokenTheInstanceIsUnreachable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "tod.db")
	require.NoError(t, runCLI(t, "migrate", "--db", path))

	srv := newE2EServerWithoutSetupToken(t, ctx, path)
	for _, token := range []string{"", e2eSetupToken, "anything at all"} {
		srv.call(t, e2eRequest{
			Method: http.MethodGet, Path: "/api/v1/setup", Token: token,
			Want: http.StatusNotFound,
		})
	}

	var meta api.ServerMeta
	require.NoError(t, json.Unmarshal(srv.get(t, "/api/v1/meta", "", http.StatusOK), &meta))
	require.False(t, meta.SetupAvailable,
		"an instance that cannot complete setup must not advertise the wizard")
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
