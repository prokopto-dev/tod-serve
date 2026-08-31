package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// SessionCookie is the browser session cookie. The `__Host-` prefix is not decoration: a browser
// refuses to set such a cookie unless it is `Secure`, has `Path=/` and carries no `Domain`, which
// means a subdomain this instance does not control cannot write one. That is a guarantee the
// server gets for free and cannot obtain any other way.
const SessionCookie = "__Host-tod_session"

// DefaultSessionTTL is how long a session lasts before it must authenticate again.
const DefaultSessionTTL = 12 * time.Hour

// DefaultStepUpWindow is how recently a session must have proved its identity to perform a
// capability-floor operation. A tab left open all afternoon still authenticates you; it does not
// prove that you are the person now typing into it.
const DefaultStepUpWindow = 5 * time.Minute

var (
	// ErrMalformedSession is returned for a cookie value that is not a session at all.
	ErrMalformedSession = errors.New("malformed session")
	// ErrSessionSignature is returned when the signature does not verify. It is deliberately
	// distinct from ErrMalformedSession in the log and identical to it on the wire.
	ErrSessionSignature = errors.New("session signature does not verify")
	// ErrSessionExpired is returned for a session past its expiry.
	ErrSessionExpired = errors.New("session expired")
	// ErrNoSessionKey is returned when a codec is built without a signing key.
	ErrNoSessionKey = errors.New("session signing key is empty")
	// ErrSessionHasNoID is returned for a session carrying no id, on encode and on decode alike.
	//
	// A session with no id cannot be signed out — there is nothing to write into
	// `session_revocation` and nothing for the authenticator to match — so accepting one would
	// ship the bug the sign-out route exists to fix, hidden behind the fix. Refusing it here is
	// what makes "every accepted session can be ended" a property rather than a convention.
	//
	// The cost is stated: cookies minted by a build older than the sign-out route carry no id, so
	// upgrading to it signs those sessions out once. That is the same blast radius as rotating
	// TOD_SESSION_KEY, paid once, and it is the smaller of the two prices on offer.
	ErrSessionHasNoID = errors.New("session carries no id")
)

// Session is what the cookie carries.
//
// Sessions are stateless — signed, not stored. There is no `session` table in the schema and this
// is why: the thing a server-side session table buys is revocation, and revocation here is already
// checked on EVERY request against the membership itself. A revoked membership's session stops
// working on its next request whether or not anybody remembered to delete a row, which is a
// stronger guarantee than a table would give and one fewer thing to forget.
//
// What it costs, stated: there is no "sign out everywhere" that takes effect before the session
// expires, and the signing key is the only thing standing behind every live session, so rotating it
// signs everyone out. Both are acceptable at a TTL measured in hours; neither would be at a TTL
// measured in months, and that is the constraint to remember before lengthening it.
//
// One session state IS stored, and it is the narrow half: `session_revocation` holds the sessions
// somebody signed out of, so a cookie copied before the sign-out is refused too. Nothing about a
// live session is written anywhere — the table only ever grows when a person ends one, and a row
// is swept once the session it names would have expired regardless.
type Session struct {
	// ID identifies this session, and is what a sign-out records. It is a ULID minted when the
	// session is created — see [SessionCodec.Encode], which refuses a session without one.
	//
	// It is not a secret and is not compared as one: the whole cookie is already authenticated by
	// the MAC below, so by the time anything reads this field the value has been proved to be one
	// this server issued. What it buys is a name for "this session" that is not the signature,
	// which no server may store, and not `(membership, issued_at)`, which two sessions minted in
	// one microsecond would share.
	ID string `json:"sid"`
	// MembershipID is the principal. A session, like a token, is bound to one membership.
	MembershipID string `json:"m"`
	// IssuedAt is when the session was created.
	IssuedAt core.Micros `json:"i"`
	// ExpiresAt is when it stops being accepted.
	ExpiresAt core.Micros `json:"e"`
	// SteppedUpAt is when the identity was last proved. It is separate from IssuedAt because a
	// long-lived session re-proves itself several times.
	SteppedUpAt core.Micros `json:"s"`
}

// SessionCodec signs and verifies session cookies.
type SessionCodec struct {
	key core.Secret
}

// NewSessionCodec returns a codec over the instance's session signing key.
func NewSessionCodec(key core.Secret) (*SessionCodec, error) {
	if key.IsZero() {
		return nil, fmt.Errorf("new session codec: %w", ErrNoSessionKey)
	}
	return &SessionCodec{key: key}, nil
}

// Encode returns the cookie value for a session: `<payload>.<signature>`, both base64url.
//
// A session with no id is refused rather than signed. See [ErrSessionHasNoID]: an unrevocable
// session must not be mintable, and a construction error at the one call site that mints them is
// cheaper to notice than a sign-out that silently does nothing.
func (c *SessionCodec) Encode(s Session) (string, error) {
	if s.ID == "" {
		return "", fmt.Errorf("encode session: %w", ErrSessionHasNoID)
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode session: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(c.sign(body)), nil
}

// Decode verifies and parses a cookie value, refusing one that has expired at now.
//
// The signature is checked BEFORE the payload is unmarshalled, so a forged cookie never reaches
// the JSON decoder — a parser is a much larger attack surface than a MAC comparison.
func (c *SessionCodec) Decode(value string, now core.Micros) (Session, error) {
	body, sig, ok := strings.Cut(value, ".")
	if !ok {
		return Session{}, fmt.Errorf("decode session: %w: no signature", ErrMalformedSession)
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return Session{}, fmt.Errorf("decode session: %w: signature is not base64url",
			ErrMalformedSession)
	}
	if !hmac.Equal(got, c.sign(body)) {
		return Session{}, fmt.Errorf("decode session: %w", ErrSessionSignature)
	}

	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Session{}, fmt.Errorf("decode session: %w: payload is not base64url",
			ErrMalformedSession)
	}
	var s Session
	if err := json.Unmarshal(payload, &s); err != nil {
		return Session{}, fmt.Errorf("decode session: %w: %w", ErrMalformedSession, err)
	}
	if s.ID == "" {
		return Session{}, fmt.Errorf("decode session: %w", ErrSessionHasNoID)
	}
	if s.ExpiresAt.IsZero() || !now.Before(s.ExpiresAt) {
		return Session{}, fmt.Errorf("decode session: %w", ErrSessionExpired)
	}
	return s, nil
}

// Cookie returns the `Set-Cookie` for a session value. Every attribute here is load-bearing:
// `Secure` and `Path=/` because `__Host-` requires them, `HttpOnly` because no script has any
// reason to read a session, and `SameSite=Lax` because a cross-site POST must not carry it.
func (c *SessionCodec) Cookie(value string, expires core.Micros) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires.Time(),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// ClearCookie returns the `Set-Cookie` that removes a session from the browser.
func (c *SessionCodec) ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func (c *SessionCodec) sign(body string) []byte {
	mac := hmac.New(sha256.New, []byte(c.key.Reveal()))
	// Deliberate waiver: hash.Hash.Write never returns an error.
	_, _ = mac.Write([]byte(body))
	return mac.Sum(nil)
}
