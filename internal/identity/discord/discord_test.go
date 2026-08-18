package discord_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity/outbound"
)

const (
	ourApp     = "111111111111111111"
	foreignApp = "999999999999999999"
	guildID    = "222222222222222222"
	subject    = "333333333333333333"
)

// call records one request the client made, so a test can assert BOTH the answer and the order —
// the audience check being first is the invariant, and an implementation that made it second
// would still produce the right answer on the happy path.
type call struct {
	method string
	url    string
	body   string
}

// stubDoer answers from a table keyed on a URL suffix. It stands in for the guarded client; the
// guard itself is tested against a real socket in internal/identity/outbound.
type stubDoer struct {
	answers map[string]*outbound.Response
	err     error
	calls   []call
}

func (s *stubDoer) Do(_ context.Context, method, rawURL string, _ http.Header, body []byte) (*outbound.Response, error) {
	s.calls = append(s.calls, call{method: method, url: rawURL, body: string(body)})
	if s.err != nil {
		return nil, s.err
	}
	for suffix, resp := range s.answers {
		if strings.HasSuffix(rawURL, suffix) {
			return resp, nil
		}
	}
	return &outbound.Response{Status: http.StatusNotFound, Header: http.Header{}, Body: []byte("{}")}, nil
}

func (s *stubDoer) paths() []string {
	out := make([]string, 0, len(s.calls))
	for _, c := range s.calls {
		out = append(out, c.url)
	}
	return out
}

func ok(t *testing.T, v map[string]any) *outbound.Response {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return &outbound.Response{Status: http.StatusOK, Header: http.Header{}, Body: b}
}

func newClient(t *testing.T, doer outbound.Doer) *discord.Client {
	t.Helper()
	c, err := discord.New(doer, discord.Config{
		ClientID:     ourApp,
		ClientSecret: core.Secret("s3cret"),
		RedirectURI:  "https://tod.example.com/api/v1/auth/callback/discord",
	})
	require.NoError(t, err)
	return c
}

func authorizedFor(t *testing.T, appID string, scopes ...string) map[string]any {
	t.Helper()
	return map[string]any{"application": map[string]any{"id": appID}, "scopes": scopes}
}

// The invariant named in ADR-0011 and docs/concepts/invariants.md. A Discord access token is a
// bearer token that `GET /users/@me` honours whichever application minted it, so per-instance
// registration closes cross-instance replay ONLY if something checks the audience.
func TestDiscord_ForeignApplicationToken_Refused(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{answers: map[string]*outbound.Response{
		"/oauth2/@me": ok(t, authorizedFor(t, foreignApp, discord.ScopeIdentify)),
		"/users/@me":  ok(t, map[string]any{"id": subject, "username": "someone"}),
	}}

	_, err := newClient(t, doer).Verify(t.Context(), core.Secret("token"), nil)

	require.ErrorIs(t, err, discord.ErrAudienceMismatch)
	require.Equal(t, 1, len(doer.calls), "nothing is read about a subject whose token is not ours")
	require.Contains(t, doer.calls[0].url, "/oauth2/@me")
}

// The audience check is first, always. An implementation that reads the user object and then
// checks the audience has already told an attacker's server that the token is valid.
func TestVerify_AudienceCheck_RunsBeforeAnyOtherCall(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{answers: map[string]*outbound.Response{
		"/oauth2/@me": ok(t, authorizedFor(t, ourApp, discord.ScopeIdentify)),
		"/users/@me":  ok(t, map[string]any{"id": subject, "username": "tankguy"}),
	}}

	facts, err := newClient(t, doer).Verify(t.Context(), core.Secret("token"), nil)

	require.NoError(t, err)
	require.Equal(t, subject, facts.Subject)
	require.Equal(t, "tankguy", facts.DisplayName)
	require.Contains(t, doer.paths()[0], "/oauth2/@me")
}

func TestVerify_GlobalName_IsPreferredOverUsername(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{answers: map[string]*outbound.Response{
		"/oauth2/@me": ok(t, authorizedFor(t, ourApp, discord.ScopeIdentify)),
		"/users/@me":  ok(t, map[string]any{"id": subject, "username": "legacy_name", "global_name": "Tankguy"}),
	}}

	facts, err := newClient(t, doer).Verify(t.Context(), core.Secret("token"), nil)

	require.NoError(t, err)
	require.Equal(t, "Tankguy", facts.DisplayName)
}

// A scope the user declined is reported as a declined scope, never as a role failure. The two
// point at completely different fixes and this flow knows which one happened.
func TestVerify_DeclinedGuildScope_ReportsScopeDeclinedNotRoleFailure(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{answers: map[string]*outbound.Response{
		"/oauth2/@me": ok(t, authorizedFor(t, ourApp, discord.ScopeIdentify)),
		"/users/@me":  ok(t, map[string]any{"id": subject, "username": "tankguy"}),
	}}

	_, err := newClient(t, doer).Verify(t.Context(), core.Secret("token"), []string{guildID})

	require.ErrorIs(t, err, discord.ErrScopeDeclined)
	require.NotErrorIs(t, err, discord.ErrGuildRoleRequired)
	require.NotContains(t, strings.Join(doer.paths(), " "), "/member",
		"the member call is not attempted without the scope that grants it")
}

// The flow calls only GET /users/@me/guilds/{id}/member, and therefore requests only
// guilds.members.read. It never calls GET /users/@me/guilds, which needs the broader `guilds`
// scope and would harvest the subject's whole guild list to answer a narrower question.
func TestVerify_GuildFacts_ComeFromTheMemberEndpointOnly(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{answers: map[string]*outbound.Response{
		"/oauth2/@me": ok(t, authorizedFor(t, ourApp, discord.ScopeIdentify, discord.ScopeGuildsMembersRead)),
		"/users/@me":  ok(t, map[string]any{"id": subject, "username": "tankguy"}),
		"/member":     ok(t, map[string]any{"roles": []string{"role-a", "role-b"}}),
	}}

	facts, err := newClient(t, doer).Verify(t.Context(), core.Secret("token"), []string{guildID})

	require.NoError(t, err)
	require.Equal(t, discord.GuildFact{Member: true, RoleIDs: []string{"role-a", "role-b"}}, facts.Guilds[guildID])
	for _, p := range doer.paths() {
		require.NotEqual(t, discord.DefaultAPIBase+"/users/@me/guilds", p,
			"the guild list is never fetched; it needs a scope this flow does not request")
	}
}

// Discord's 404 is a RECORDED negative — "we asked, and no" — not a missing fact. The gate needs
// to tell those apart to report guild_membership_required rather than guild_role_required.
func TestVerify_GuildMemberNotFound_RecordsANegativeRatherThanNothing(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{answers: map[string]*outbound.Response{
		"/oauth2/@me": ok(t, authorizedFor(t, ourApp, discord.ScopeIdentify, discord.ScopeGuildsMembersRead)),
		"/users/@me":  ok(t, map[string]any{"id": subject, "username": "tankguy"}),
		"/member":     {Status: http.StatusNotFound, Header: http.Header{}, Body: []byte(`{"message":"Unknown Guild"}`)},
	}}

	facts, err := newClient(t, doer).Verify(t.Context(), core.Secret("token"), []string{guildID})

	require.NoError(t, err)
	fact, known := facts.Guilds[guildID]
	require.True(t, known, "a 404 is an answer and is recorded as one")
	require.False(t, fact.Member)
}

// A rate limit or a 5xx settles nothing, so nothing is written down. The gate then rejects for
// "no facts", which is the correct answer: we do not know.
func TestVerify_GuildMemberUnavailable_LeavesTheFactAbsent(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{answers: map[string]*outbound.Response{
		"/oauth2/@me": ok(t, authorizedFor(t, ourApp, discord.ScopeIdentify, discord.ScopeGuildsMembersRead)),
		"/users/@me":  ok(t, map[string]any{"id": subject, "username": "tankguy"}),
		"/member":     {Status: http.StatusTooManyRequests, Header: http.Header{}, Body: []byte(`{}`)},
	}}

	_, err := newClient(t, doer).Verify(t.Context(), core.Secret("token"), []string{guildID})

	require.ErrorIs(t, err, discord.ErrUnreachable)
}

func TestExchange_TokenResponse_YieldsTheAccessToken(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{answers: map[string]*outbound.Response{
		"/oauth2/token": ok(t, map[string]any{"access_token": "at-1", "scope": "identify"}),
	}}

	token, err := newClient(t, doer).Exchange(t.Context(), "code-1", "verifier-1")

	require.NoError(t, err)
	require.Equal(t, "at-1", token.Reveal())
	require.Contains(t, doer.calls[0].body, "code_verifier=verifier-1", "the PKCE verifier is sent from the server")
	require.Contains(t, doer.calls[0].body, "client_secret=s3cret", "the instance is a confidential client")
}

// A token endpoint's error body has echoed the request back before now, and that request carries
// the client secret. Nothing from it reaches the error.
func TestExchange_RefusedCode_LeaksNothingFromTheResponseBody(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{answers: map[string]*outbound.Response{
		"/oauth2/token": {
			Status: http.StatusBadRequest, Header: http.Header{},
			Body: []byte(`{"error":"invalid_grant","client_secret":"s3cret"}`),
		},
	}}

	_, err := newClient(t, doer).Exchange(t.Context(), "code-1", "verifier-1")

	require.ErrorIs(t, err, discord.ErrCredentialInvalid)
	require.NotContains(t, err.Error(), "s3cret")
}

func TestNew_ConfigurationWithoutAnApplication_IsRefused(t *testing.T) {
	t.Parallel()

	full := discord.Config{ClientID: ourApp, ClientSecret: core.Secret("s"), RedirectURI: "https://x/y"}

	for name, mutate := range map[string]func(discord.Config) discord.Config{
		"no client id":     func(c discord.Config) discord.Config { c.ClientID = ""; return c },
		"no client secret": func(c discord.Config) discord.Config { c.ClientSecret = ""; return c },
		"no redirect uri":  func(c discord.Config) discord.Config { c.RedirectURI = ""; return c },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := discord.New(&stubDoer{}, mutate(full))
			require.Error(t, err)
		})
	}

	_, err := discord.New(nil, full)
	require.Error(t, err, "a client with no outbound client would have no guard")
}

func TestVerify_UnreachableDiscord_IsReportedAsUnreachable(t *testing.T) {
	t.Parallel()

	doer := &stubDoer{err: errors.New("dial tcp: connection refused")}

	_, err := newClient(t, doer).Verify(t.Context(), core.Secret("token"), nil)

	require.ErrorIs(t, err, discord.ErrUnreachable)
}
