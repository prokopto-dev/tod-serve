package discord

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
)

// AuthorizationURL builds the URL the browser is redirected to.
//
// PKCE is S256 and the VERIFIER never leaves the server — only its hash does. The instance is a
// confidential client, so PKCE is not what proves the client's identity here (the secret is); it
// is what stops a code intercepted from the redirect being exchanged by whoever intercepted it.
//
// `prompt=consent` is deliberately NOT set. Re-prompting every time trains people to click
// through a consent screen without reading it, which is the opposite of what the scope minimalism
// in this flow is for.
func (c *Client) AuthorizationURL(state, verifier string, scopes []string) string {
	q := url.Values{
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {c.cfg.RedirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {state},
		"code_challenge":        {PKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	return c.cfg.AuthorizeURL + "?" + q.Encode()
}

// PKCEChallenge is the S256 challenge for a verifier: base64url(sha256(verifier)), unpadded.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Scopes returns the scope set to request.
//
// The rule is that the authorization asks for exactly what the callback will use and no more:
// `identify` always, `guilds.members.read` only where a guild gate actually exists. Asking for
// less than the callback uses fails closed against members who are genuinely in the guild; asking
// for more puts a permission on every consent screen that most circles never need.
func Scopes(guildGated bool) []string {
	if guildGated {
		return []string{ScopeIdentify, ScopeGuildsMembersRead}
	}
	return []string{ScopeIdentify}
}
