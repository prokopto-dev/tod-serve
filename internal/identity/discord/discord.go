// Package discord verifies a Discord identity and reads the facts the per-circle guild gate needs.
//
// Per [ADR-0011](docs/adr/0011-operator-registered-discord-application.md) the instance is a
// CONFIDENTIAL OAuth client holding its operator's own `client_secret`, so the authorization-code
// exchange happens here, server-side, and the browser never touches a Discord token.
//
// Two things about this package are load-bearing rather than incidental:
//
//   - **The access token is never returned to a caller that could store it.** [Client.Verify]
//     takes the token, makes every call that needs it, and hands back only derived facts. The
//     token exists inside one function call. `TestDiscord_AccessToken_NeverPersisted` is the
//     mechanism at the flow level; this shape is what makes it easy to keep true.
//   - **`GET /oauth2/@me` runs first, always.** A Discord access token is a bearer token and
//     `GET /users/@me` honours any valid one whichever application minted it, so per-instance
//     registration closes cross-instance replay only if something checks the audience. That check
//     is [ErrAudienceMismatch], and it runs on the token this instance just minted (where it is
//     redundant) as well as on one a client supplied (where it is the whole defence). A rule with
//     a carve-out is a rule somebody implements on the wrong side.
package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity/outbound"
)

// Host is the only host this provider reaches. Discord's URL is fixed, which is why `discord` is
// not on the SSRF surface the way an operator-supplied OIDC issuer is.
const Host = "discord.com"

// The endpoints. Version-pinned: an unversioned Discord API URL silently becomes a different API.
const (
	DefaultAPIBase        = "https://discord.com/api/v10"
	DefaultAuthorizeURL   = "https://discord.com/oauth2/authorize"
	defaultTokenEndpoint  = DefaultAPIBase + "/oauth2/token"
	authorizationInfoPath = "/oauth2/@me"
	currentUserPath       = "/users/@me"
)

// The scopes this flow uses, and only these.
//
// `guilds.members.read` grants `GET /users/@me/guilds/{guild.id}/member`. It does NOT grant
// `GET /users/@me/guilds`, which needs the separate and broader `guilds` scope — so this flow
// never calls the guild list and never learns which other Discord servers a subject is in. One
// call answers membership and roles for the one guild that gates the circle; see
// docs/design/04-identity-and-revocation.md §5.
const (
	ScopeIdentify          = "identify"
	ScopeGuildsMembersRead = "guilds.members.read"
)

// The failure modes, as sentinels. They are sentinels rather than coded API errors because this
// package must not import the one that owns the error codes — internal/identity dispatches to it,
// so the dependency only runs one way. internal/identity maps each to its wire code.
var (
	// ErrAudienceMismatch is a token minted for somebody else's application. See the package
	// comment: this is what actually closes cross-instance replay.
	ErrAudienceMismatch = errors.New("discord token was minted for another application")

	// ErrCredentialInvalid is a token Discord refuses, or a malformed response.
	ErrCredentialInvalid = errors.New("discord refused the credential")

	// ErrScopeDeclined is a scope the flow needs that the user did not grant. It is emphatically
	// not a role failure: telling somebody they lack a role when the truth is that we were never
	// permitted to look points them at the wrong fix.
	ErrScopeDeclined = errors.New("a scope this flow needs was not granted")

	// ErrUnreachable is Discord being down, slow or unparseable — the operator's problem or
	// nobody's, never the caller's.
	ErrUnreachable = errors.New("discord is unreachable")
)

// Config is the operator's own registered application. `client_secret` is a [core.Secret]: a
// database read is now a Discord-application compromise, which ADR-0011 names as a cost, so the
// value renders as `***` on every path out except an explicit Reveal.
type Config struct {
	ClientID     string
	ClientSecret core.Secret
	RedirectURI  string

	// TokenEndpoint and APIBase come from the provider row when the operator set them, and
	// default otherwise. They are overridable mainly so a test can point at a stub without the
	// package growing a global.
	TokenEndpoint string
	APIBase       string
	AuthorizeURL  string
}

// GuildFact is what one `GET /users/@me/guilds/{guild.id}/member` call established.
//
// The three states are deliberate, and the third is why this is a struct rather than a
// `map[string][]string`. An entry that is ABSENT means the call was never made or did not
// complete — no evaluation is possible. An entry with `Member: false` means Discord answered 404:
// the subject is genuinely not in that guild. Reading those two as the same thing is how a gate
// reports "you lack the role" to somebody who is not in the guild at all, or worse, reports
// nothing and lets them through.
type GuildFact struct {
	Member  bool     `json:"member"`
	RoleIDs []string `json:"roles"`
}

// GuildFacts is the gated-guild id to fact map that rides on the credential_ticket.
type GuildFacts map[string]GuildFact

// Facts is everything kept after the access token is discarded.
type Facts struct {
	// Subject is the Discord snowflake. It is the identity's subject and never changes.
	Subject string
	// DisplayName is `global_name ?? username`.
	DisplayName string
	// Scopes is what the user actually GRANTED, read from GET /oauth2/@me rather than from what
	// the authorization request asked for. Those differ exactly when somebody unticked a box.
	Scopes []string
	// Guilds holds one entry per guild the caller asked about that Discord answered for.
	Guilds GuildFacts
}

// Client talks to Discord through the guarded outbound client.
type Client struct {
	http outbound.Doer
	cfg  Config
}

// New returns a client. It refuses a configuration with no application, because a `discord`
// provider row without a `client_id` is unrepresentable in the schema and one here would mean
// something bypassed it.
func New(doer outbound.Doer, cfg Config) (*Client, error) {
	if doer == nil {
		return nil, errors.New("discord client: no outbound client")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("discord client: no client id; the operator has not registered an application")
	}
	if cfg.ClientSecret.IsZero() {
		return nil, errors.New("discord client: no client secret")
	}
	if cfg.RedirectURI == "" {
		return nil, errors.New("discord client: no redirect uri")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	if cfg.TokenEndpoint == "" {
		cfg.TokenEndpoint = defaultTokenEndpoint
	}
	// `token_endpoint` is a column, so an operator can set one — and for `discord` there is only
	// ever one right answer. A foreign host would be refused by the outbound allowlist anyway,
	// but as a dial that failed rather than as a configuration mistake somebody can read, so it
	// is caught here instead.
	if err := requireDiscordHost(cfg.TokenEndpoint, "token endpoint"); err != nil {
		return nil, err
	}
	if err := requireDiscordHost(cfg.APIBase, "api base"); err != nil {
		return nil, err
	}
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = DefaultAuthorizeURL
	}
	return &Client{http: doer, cfg: cfg}, nil
}

// requireDiscordHost refuses an endpoint pointed somewhere other than Discord.
func requireDiscordHost(raw, what string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("discord client: parse %s %q: %w", what, raw, err)
	}
	if u.Hostname() != Host {
		return fmt.Errorf("discord client: %s is %q, which is not %s", what, u.Hostname(), Host)
	}
	return nil
}

// Exchange trades an authorization code for an access token.
//
// The PKCE verifier is a parameter because it is held server-side on `auth_flow` — the browser
// never has it. That is what a confidential client buys: even a stolen `code` is useless without
// both the verifier and the secret.
func (c *Client) Exchange(ctx context.Context, code, verifier string) (core.Secret, error) {
	form := url.Values{
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret.Reveal()},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.cfg.RedirectURI},
		"code_verifier": {verifier},
	}
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}

	resp, err := c.http.Do(ctx, http.MethodPost, c.cfg.TokenEndpoint, header, []byte(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("exchange discord authorization code: %w", errors.Join(ErrUnreachable, err))
	}
	if resp.Status != http.StatusOK {
		// The body is not logged or wrapped in: a token endpoint's error body has echoed the
		// request back before now, and the request carries the client secret.
		return "", fmt.Errorf("exchange discord authorization code: status %d: %w", resp.Status, ErrCredentialInvalid)
	}

	var body struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return "", fmt.Errorf("decode discord token response: %w", errors.Join(ErrUnreachable, err))
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("discord token response carries no access token: %w", ErrCredentialInvalid)
	}
	return core.Secret(body.AccessToken), nil
}

// Verify establishes who a token belongs to and what the gate needs to know about them, and
// returns only that. The token is not part of the result.
//
// guildIDs is the set of guilds to ask about, and it comes from a secret (the invite's circle) or
// from the verified identity's own memberships — never from caller input, so there is nothing here
// to enumerate.
//
// The order is fixed and the first step is the audience check. Everything after it is reading
// facts about a subject we have already established is ours to read.
func (c *Client) Verify(ctx context.Context, token core.Secret, guildIDs []string) (Facts, error) {
	facts, err := c.Identify(ctx, token)
	if err != nil {
		return Facts{}, err
	}
	if err := c.AddGuildFacts(ctx, token, &facts, guildIDs); err != nil {
		return Facts{}, err
	}
	return facts, nil
}

// Identify performs the audience check and reads the subject, and stops there.
//
// It is separate from [Client.AddGuildFacts] because the browser callback cannot know which
// guilds to ask about until it knows WHO is asking: with no invite, the guild set comes from the
// circles this identity already belongs to, which is a lookup keyed on something the caller
// proved rather than something they supplied.
func (c *Client) Identify(ctx context.Context, token core.Secret) (Facts, error) {
	scopes, err := c.authorizationInfo(ctx, token)
	if err != nil {
		return Facts{}, err
	}
	subject, displayName, err := c.currentUser(ctx, token)
	if err != nil {
		return Facts{}, err
	}
	return Facts{Subject: subject, DisplayName: displayName, Scopes: scopes, Guilds: GuildFacts{}}, nil
}

// AddGuildFacts fills in one member object per guild, using the scopes [Client.Identify] read.
func (c *Client) AddGuildFacts(ctx context.Context, token core.Secret, facts *Facts, guildIDs []string) error {
	if len(guildIDs) == 0 {
		return nil
	}
	if facts.Guilds == nil {
		facts.Guilds = GuildFacts{}
	}

	// A guild question with no scope to answer it is a declined scope, reported as one. Falling
	// through to the member call would produce a 401 from Discord and — if that were read as
	// "not a member" — a role failure reported to somebody who holds the role.
	if !hasScope(facts.Scopes, ScopeGuildsMembersRead) {
		return fmt.Errorf("%s was not granted: %w", ScopeGuildsMembersRead, ErrScopeDeclined)
	}

	for _, id := range guildIDs {
		fact, known, err := c.guildMember(ctx, token, id)
		if err != nil {
			return err
		}
		// `known` false means the call did not settle the question. The entry is left ABSENT
		// rather than written as a non-member, so the gate rejects for "no facts" instead of
		// claiming Discord said no.
		if known {
			facts.Guilds[id] = fact
		}
	}
	return nil
}

// authorizationInfo is `GET /oauth2/@me`. It answers two questions in one call: whose application
// minted this token, and which scopes the user actually granted.
func (c *Client) authorizationInfo(ctx context.Context, token core.Secret) ([]string, error) {
	type authorizationInfo struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
		Scopes []string `json:"scopes"`
	}
	body, err := getJSON[authorizationInfo](ctx, c, token, authorizationInfoPath)
	if err != nil {
		return nil, err
	}
	if body.Application.ID != c.cfg.ClientID {
		// The foreign application id is deliberately not in the message: it is another operator's
		// instance identifier, and this error reaches a log an attacker may be able to read.
		return nil, fmt.Errorf("discord token audience is not this instance: %w", ErrAudienceMismatch)
	}
	return body.Scopes, nil
}

// currentUser is `GET /users/@me`. `global_name` is Discord's display name and may be null for an
// account that never set one, which is what the fallback to `username` is for.
func (c *Client) currentUser(ctx context.Context, token core.Secret) (subject, displayName string, err error) {
	type user struct {
		ID         string  `json:"id"`
		Username   string  `json:"username"`
		GlobalName *string `json:"global_name"`
	}
	body, err := getJSON[user](ctx, c, token, currentUserPath)
	if err != nil {
		return "", "", err
	}
	if body.ID == "" {
		return "", "", fmt.Errorf("discord user object carries no id: %w", ErrCredentialInvalid)
	}
	name := body.Username
	if body.GlobalName != nil && *body.GlobalName != "" {
		name = *body.GlobalName
	}
	return body.ID, name, nil
}

// guildMember is `GET /users/@me/guilds/{guild.id}/member`, the one call that answers both halves
// of the gate. It reports `known` false when the answer is neither a 200 nor a 404 — a rate limit,
// a 5xx — so the caller can leave the fact absent rather than invent one.
func (c *Client) guildMember(ctx context.Context, token core.Secret, guildID string) (GuildFact, bool, error) {
	endpoint := c.cfg.APIBase + "/users/@me/guilds/" + url.PathEscape(guildID) + "/member"
	header := http.Header{"Authorization": {"Bearer " + token.Reveal()}}

	resp, err := c.http.Do(ctx, http.MethodGet, endpoint, header, nil)
	if err != nil {
		return GuildFact{}, false, fmt.Errorf("read discord guild member: %w", errors.Join(ErrUnreachable, err))
	}
	switch resp.Status {
	case http.StatusOK:
		var body struct {
			Roles []string `json:"roles"`
		}
		if err := json.Unmarshal(resp.Body, &body); err != nil {
			return GuildFact{}, false, fmt.Errorf("decode discord guild member: %w", errors.Join(ErrUnreachable, err))
		}
		if body.Roles == nil {
			body.Roles = []string{}
		}
		return GuildFact{Member: true, RoleIDs: body.Roles}, true, nil
	case http.StatusNotFound:
		// Discord's answer for "this subject is not in that guild". A recorded negative, not a
		// missing fact — the gate tells those apart.
		return GuildFact{Member: false, RoleIDs: []string{}}, true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return GuildFact{}, false, fmt.Errorf("discord refused the guild member read: status %d: %w", resp.Status, ErrCredentialInvalid)
	default:
		return GuildFact{}, false, fmt.Errorf("discord guild member read: status %d: %w", resp.Status, ErrUnreachable)
	}
}

// getJSON performs one authenticated GET against the API base and decodes it.
//
// It is a generic function rather than a method taking an `any` out-parameter because `any` in a
// signature is banned by AGENTS.md, and for the reason the ban exists: the decoded shape is part
// of what the call means, and a caller reading `c.get(ctx, token, path, &thing)` cannot see
// whether the callee agreed.
func getJSON[T any](ctx context.Context, c *Client, token core.Secret, path string) (T, error) {
	var out T
	header := http.Header{"Authorization": {"Bearer " + token.Reveal()}}

	resp, err := c.http.Do(ctx, http.MethodGet, c.cfg.APIBase+path, header, nil)
	if err != nil {
		return out, fmt.Errorf("read discord %s: %w", path, errors.Join(ErrUnreachable, err))
	}
	switch {
	case resp.Status == http.StatusUnauthorized, resp.Status == http.StatusForbidden:
		return out, fmt.Errorf("discord refused %s: status %d: %w", path, resp.Status, ErrCredentialInvalid)
	case resp.Status != http.StatusOK:
		return out, fmt.Errorf("discord %s: status %d: %w", path, resp.Status, ErrUnreachable)
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return out, fmt.Errorf("decode discord %s: %w", path, errors.Join(ErrUnreachable, err))
	}
	return out, nil
}

// hasScope reports whether the granted set contains want. Discord returns scopes as a list here
// and as a space-separated string elsewhere, so both shapes are accepted.
func hasScope(granted []string, want string) bool {
	for _, g := range granted {
		if g == want {
			return true
		}
		for _, part := range strings.Fields(g) {
			if part == want {
				return true
			}
		}
	}
	return false
}
