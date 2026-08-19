package oidc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity/outbound"
)

// DefaultSubjectClaim is the claim the subject is read from unless the provider row names another.
// `sub` is the only claim OIDC guarantees is stable and unique within an issuer; `email` is not,
// which is why `subject_claim` exists as an escape hatch rather than as a menu.
const DefaultSubjectClaim = "sub"

// clockSkewLeeway is how far the issuer's clock may be from ours before a valid token is refused.
//
// Zero leeway is not stricter in any useful sense — it is a verifier that fails at 03:00 when
// somebody's NTP drifts, and the fix people reach for under that pressure is to stop checking
// `exp` at all. Sixty seconds is the OIDC ecosystem's usual figure.
const clockSkewLeeway = 60 * time.Second

// Config is one `oidc` row of `identity_provider`.
type Config struct {
	// Issuer must equal the token's `iss` exactly. Not a prefix, not a normalised form: the
	// issuer identifier IS the trust boundary and a fuzzy comparison widens it.
	Issuer string

	// ClientID is what `aud` must contain.
	ClientID string

	// ClientSecret is used only on the browser path's token exchange. A non-browser client
	// presenting an `id_token` needs none of it.
	ClientSecret core.Secret

	// JWKSURI is the ONE URL this package fetches. Discovery is deliberately not implemented:
	// every operator-supplied URL is SSRF surface, and this one is enough.
	JWKSURI string

	// AuthorizationEndpoint and TokenEndpoint drive the browser flow.
	AuthorizationEndpoint string
	TokenEndpoint         string
	RedirectURI           string

	// SubjectClaim defaults to [DefaultSubjectClaim].
	SubjectClaim string

	KeyTTL             time.Duration
	MinRefreshInterval time.Duration
	MaxKeys            int
}

// Identity is what a verified ID token established.
type Identity struct {
	Subject     string
	DisplayName string
}

// Verifier verifies ID tokens for one provider row. It is safe for concurrent use.
type Verifier struct {
	cfg   Config
	keys  *keySet
	clock clock.Clock
	http  outbound.Doer
}

// NewVerifier returns a verifier. Everything it refuses here is something that would otherwise be
// discovered as a confusing verification failure at somebody's first join.
func NewVerifier(doer outbound.Doer, clk clock.Clock, cfg Config) (*Verifier, error) {
	switch {
	case doer == nil:
		return nil, errors.New("oidc verifier: no outbound client")
	case clk == nil:
		return nil, errors.New("oidc verifier: no clock")
	case cfg.Issuer == "":
		return nil, errors.New("oidc verifier: no issuer")
	case cfg.ClientID == "":
		return nil, errors.New("oidc verifier: no client id, so there is no audience to check")
	case cfg.JWKSURI == "":
		return nil, errors.New("oidc verifier: no jwks uri")
	}
	if cfg.SubjectClaim == "" {
		cfg.SubjectClaim = DefaultSubjectClaim
	}
	if cfg.KeyTTL == 0 {
		cfg.KeyTTL = DefaultKeyTTL
	}
	if cfg.MinRefreshInterval == 0 {
		cfg.MinRefreshInterval = DefaultMinRefreshInterval
	}
	if cfg.MaxKeys == 0 {
		cfg.MaxKeys = DefaultMaxKeys
	}

	return &Verifier{
		cfg:   cfg,
		clock: clk,
		http:  doer,
		keys: &keySet{
			http: doer, uri: cfg.JWKSURI, clock: clk,
			ttl: cfg.KeyTTL, minRefresh: cfg.MinRefreshInterval, maxKeys: cfg.MaxKeys,
		},
	}, nil
}

// Verify checks an ID token offline and returns the identity it names.
//
// The order is signature first, then claims, then nonce. Nothing is read out of the token before
// its signature verifies, because everything in it is attacker-controlled until then.
//
// expectedNonce is required. A nonce is what binds an ID token to the authorization request that
// asked for it; without one, a token replayed from anywhere the subject has signed in is accepted
// here. Making it a required parameter rather than an optional one means "we forgot to pass it"
// is a compile-time shape rather than a silent downgrade.
func (v *Verifier) Verify(ctx context.Context, idToken, expectedNonce string) (Identity, error) {
	if expectedNonce == "" {
		return Identity{}, fmt.Errorf("no nonce to check the id token against: %w", ErrCredentialInvalid)
	}

	tok, err := verifySignature(idToken, func(kid string) (publicKey, error) {
		return v.keys.keyFor(ctx, kid)
	})
	if err != nil {
		return Identity{}, err
	}

	if tok.claims.Issuer != v.cfg.Issuer {
		// The token's claimed issuer is not echoed: it is attacker-controlled text heading for a
		// log line somebody will read.
		return Identity{}, fmt.Errorf("id token issuer is not this provider's: %w", ErrCredentialInvalid)
	}

	auds, err := tok.claims.audiences()
	if err != nil {
		return Identity{}, err
	}
	if !contains(auds, v.cfg.ClientID) {
		return Identity{}, fmt.Errorf("id token aud does not name this client: %w", ErrAudienceMismatch)
	}
	// With more than one audience the spec requires `azp`, and it must be us. Without this check
	// a token minted for a different relying party at the same issuer — one that happens to list
	// us as a second audience — verifies here.
	if len(auds) > 1 && tok.claims.AZP != v.cfg.ClientID {
		return Identity{}, fmt.Errorf("id token names several audiences and azp is not this client: %w", ErrAudienceMismatch)
	}

	now := v.clock.Now()
	if tok.claims.Expiry == 0 {
		return Identity{}, fmt.Errorf("id token has no exp: %w", ErrCredentialInvalid)
	}
	if now.After(core.Micros(tok.claims.Expiry * core.MicrosPerSecond).Add(clockSkewLeeway)) {
		return Identity{}, fmt.Errorf("id token expired: %w", ErrCredentialExpired)
	}
	if tok.claims.NotBefore != 0 &&
		now.Before(core.Micros(tok.claims.NotBefore*core.MicrosPerSecond).Add(-clockSkewLeeway)) {
		return Identity{}, fmt.Errorf("id token is not yet valid: %w", ErrCredentialInvalid)
	}
	if tok.claims.IssuedAt != 0 &&
		now.Before(core.Micros(tok.claims.IssuedAt*core.MicrosPerSecond).Add(-clockSkewLeeway)) {
		return Identity{}, fmt.Errorf("id token was issued in the future: %w", ErrCredentialInvalid)
	}

	// Constant time, because a nonce compared with == is a timing oracle over a value the caller
	// is trying to guess.
	if subtle.ConstantTimeCompare([]byte(tok.claims.Nonce), []byte(expectedNonce)) != 1 {
		return Identity{}, fmt.Errorf("id token nonce does not match this authorization: %w", ErrCredentialInvalid)
	}

	subject, err := stringClaim(tok.raw, v.cfg.SubjectClaim)
	if err != nil {
		return Identity{}, fmt.Errorf("read subject claim %q: %w", v.cfg.SubjectClaim, err)
	}
	if subject == "" {
		return Identity{}, fmt.Errorf("id token carries no %q claim: %w", v.cfg.SubjectClaim, ErrCredentialInvalid)
	}

	return Identity{Subject: subject, DisplayName: displayName(tok.raw)}, nil
}

// AuthorizationURL builds the browser redirect for the OIDC half of the shared browser flow.
//
// The nonce is DERIVED from the PKCE verifier rather than stored beside it. `auth_flow` holds
// `state` and `pkce_verifier` and no nonce column, and adding one would be a schema change to
// store a value that is already determined: the verifier is single-use, server-side and
// high-entropy, so a hash of it binds the ID token to this authorization exactly as a separately
// generated nonce would. The derivation is domain-separated so the nonce can never be mistaken
// for the verifier itself.
func (v *Verifier) AuthorizationURL(state, verifier string, scopes []string) string {
	q := url.Values{
		"client_id":             {v.cfg.ClientID},
		"redirect_uri":          {v.cfg.RedirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {state},
		"nonce":                 {NonceFor(verifier)},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	return v.cfg.AuthorizationEndpoint + "?" + q.Encode()
}

// Scopes is what the OIDC half of the browser flow requests. `openid` is mandatory; `profile`
// carries the display name. Nothing else: this product needs a stable subject and something to
// render, and an email address it does not use is an email address it can leak.
func Scopes() []string { return []string{"openid", "profile"} }

// NonceFor derives the nonce that goes with a PKCE verifier. Exported because the callback has to
// recompute it from the stored verifier to check the token it gets back.
func NonceFor(verifier string) string {
	sum := sha256.Sum256([]byte("tod-serve/oidc-nonce\x00" + verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Exchange trades an authorization code for an ID token, server-side.
//
// Only the `id_token` is kept. An OIDC access token would let this instance call the issuer's
// userinfo endpoint, which is a round trip the design deliberately does not make and a credential
// there is no reason to hold.
func (v *Verifier) Exchange(ctx context.Context, code, verifier string) (string, error) {
	if v.cfg.TokenEndpoint == "" {
		return "", errors.New("oidc exchange: the provider row names no token endpoint")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {v.cfg.RedirectURI},
		"client_id":     {v.cfg.ClientID},
		"client_secret": {v.cfg.ClientSecret.Reveal()},
		"code_verifier": {verifier},
	}
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}

	resp, err := v.http.Do(ctx, http.MethodPost, v.cfg.TokenEndpoint, header, []byte(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("exchange oidc authorization code: %w", errors.Join(ErrUnreachable, err))
	}
	if resp.Status != http.StatusOK {
		// The body is dropped for the reason the Discord exchange drops it: a token endpoint's
		// error body has echoed the request back before now, and the request carries the secret.
		return "", fmt.Errorf("exchange oidc authorization code: status %d: %w", resp.Status, ErrCredentialInvalid)
	}
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return "", fmt.Errorf("decode oidc token response: %w", errors.Join(ErrUnreachable, err))
	}
	if body.IDToken == "" {
		return "", fmt.Errorf("oidc token response carries no id_token: %w", ErrCredentialInvalid)
	}
	return body.IDToken, nil
}

// Hosts is the outbound allowlist this provider needs: the endpoints it actually FETCHES, and no
// others. The authorization endpoint is missing on purpose — the browser goes there, this
// instance never does.
func (c Config) Hosts() ([]string, error) {
	hosts := make([]string, 0, 2)
	for _, raw := range []string{c.JWKSURI, c.TokenEndpoint} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("parse provider url: %w", err)
		}
		if u.Hostname() == "" {
			return nil, fmt.Errorf("provider url %q names no host", raw)
		}
		hosts = append(hosts, u.Hostname())
	}
	if len(hosts) == 0 {
		return nil, errors.New("oidc provider names no fetchable endpoint")
	}
	return hosts, nil
}

// displayName reads something human out of the token, in the order OIDC makes them most likely to
// be present. An absent display name is not an error: `display_name` is optional everywhere but
// `local`, and the join request may carry one.
func displayName(raw map[string]json.RawMessage) string {
	for _, claim := range []string{"name", "preferred_username", "nickname"} {
		if v, err := stringClaim(raw, claim); err == nil && v != "" {
			return v
		}
	}
	return ""
}

// stringClaim reads one claim as a string. A claim that is present but not a string is an error
// rather than a coerced value: an issuer whose `sub` is a number is one whose subjects would
// silently change shape.
func stringClaim(raw map[string]json.RawMessage, name string) (string, error) {
	v, ok := raw[name]
	if !ok {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("claim %q is not a string: %w", name, errors.Join(ErrCredentialInvalid, err))
	}
	return s, nil
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
