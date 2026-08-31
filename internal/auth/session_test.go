package auth_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

const sessionKey = core.Secret("session-signing-key-for-tests")

// now is a fixed instant. The clock is injected everywhere, so a test that needs an exact moment
// writes one down rather than reading one.
const now = core.Micros(1_755_483_247_000_000)

// sessionID is a fixed session id, so a test that needs one writes it down for the same reason the
// instant above is written down.
const sessionID = "01K3TGT8N9M4X0Q7R2VB6C5D1F"

func testCodec(t *testing.T) *auth.SessionCodec {
	t.Helper()
	c, err := auth.NewSessionCodec(sessionKey)
	require.NoError(t, err)
	return c
}

func TestNewSessionCodec_NoKey_IsRefused(t *testing.T) {
	t.Parallel()
	_, err := auth.NewSessionCodec("")
	require.ErrorIs(t, err, auth.ErrNoSessionKey)
}

func TestSession_RoundTrip_PreservesEveryField(t *testing.T) {
	t.Parallel()
	c := testCodec(t)
	want := auth.Session{
		ID:           sessionID,
		MembershipID: "01K3TGT8N9M4X0Q7R2VB6C5D1E",
		IssuedAt:     now,
		ExpiresAt:    now.Add(auth.DefaultSessionTTL),
		SteppedUpAt:  now,
	}
	value, err := c.Encode(want)
	require.NoError(t, err)

	got, err := c.Decode(value, now)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// A session is signed, not stored, so the signature is the only thing standing behind it.
func TestSession_TamperedPayload_IsRefused(t *testing.T) {
	t.Parallel()
	c := testCodec(t)
	value, err := c.Encode(auth.Session{
		ID:           sessionID,
		MembershipID: "01K3TGT8N9M4X0Q7R2VB6C5D1E",
		ExpiresAt:    now.Add(time.Hour),
	})
	require.NoError(t, err)

	body, sig, ok := strings.Cut(value, ".")
	require.True(t, ok)

	_, err = c.Decode(body[:len(body)-1]+"A"+"."+sig, now)
	require.ErrorIs(t, err, auth.ErrSessionSignature)
}

// A cookie signed by another instance, or by this one before its key was rotated, is not a session.
func TestSession_SignedWithAnotherKey_IsRefused(t *testing.T) {
	t.Parallel()
	other, err := auth.NewSessionCodec("a different key")
	require.NoError(t, err)
	value, err := other.Encode(auth.Session{
		ID:           sessionID,
		MembershipID: "01K3TGT8N9M4X0Q7R2VB6C5D1E",
		ExpiresAt:    now.Add(time.Hour),
	})
	require.NoError(t, err)

	_, err = testCodec(t).Decode(value, now)
	require.ErrorIs(t, err, auth.ErrSessionSignature)
}

func TestSession_Expired_IsRefused(t *testing.T) {
	t.Parallel()
	c := testCodec(t)
	value, err := c.Encode(auth.Session{
		ID:           sessionID,
		MembershipID: "01K3TGT8N9M4X0Q7R2VB6C5D1E",
		ExpiresAt:    now,
	})
	require.NoError(t, err)

	_, err = c.Decode(value, now)
	require.ErrorIs(t, err, auth.ErrSessionExpired, "a session expires AT its expiry, not after it")
}

// A session with no id cannot be signed out: there is nothing to write into `session_revocation`
// and nothing for the authenticator to match. Refusing it on BOTH sides is what makes "every
// accepted session can be ended" a property of the codec rather than a convention at its callers —
// encoding covers the sessions this build mints, decoding covers the cookies an older one did.
func TestSession_WithNoID_IsRefusedOnEncodeAndOnDecode(t *testing.T) {
	t.Parallel()
	c := testCodec(t)

	_, err := c.Encode(auth.Session{
		MembershipID: "01K3TGT8N9M4X0Q7R2VB6C5D1E",
		ExpiresAt:    now.Add(time.Hour),
	})
	require.ErrorIs(t, err, auth.ErrSessionHasNoID)

	// The decode half needs a cookie this codec would accept in every other respect, which the
	// encoder now refuses to produce. So the payload is assembled and signed the way the codec
	// does it: a correctly signed, unexpired, id-less session is exactly the shape a build from
	// before the sign-out route left in somebody's browser.
	payload, err := json.Marshal(struct {
		MembershipID string      `json:"m"`
		ExpiresAt    core.Micros `json:"e"`
	}{MembershipID: "01K3TGT8N9M4X0Q7R2VB6C5D1E", ExpiresAt: now.Add(time.Hour)})
	require.NoError(t, err)
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(sessionKey.Reveal()))
	_, err = mac.Write([]byte(body))
	require.NoError(t, err)
	legacy := body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	// The fixture is only worth anything if it is otherwise valid, so prove the signature verifies
	// by checking the refusal is about the id rather than about the MAC.
	_, err = c.Decode(legacy, now)
	require.ErrorIs(t, err, auth.ErrSessionHasNoID)
	require.NotErrorIs(t, err, auth.ErrSessionSignature)
}

func TestSession_MalformedValues_AreRefused(t *testing.T) {
	t.Parallel()
	c := testCodec(t)
	for _, value := range []string{"", "no-dot", ".", "notbase64.notbase64", "e30.AAAA"} {
		_, err := c.Decode(value, now)
		require.Error(t, err, "decoded %q as a session", value)
	}
}

// The `__Host-` prefix is not decoration: a browser refuses to set such a cookie unless it is
// Secure, has Path=/ and carries no Domain, which is a guarantee the server gets for free and can
// obtain no other way.
func TestSessionCookie_Attributes_SatisfyTheHostPrefix(t *testing.T) {
	t.Parallel()
	c := testCodec(t)
	cookie := c.Cookie("value", now.Add(time.Hour))

	require.Equal(t, auth.SessionCookie, cookie.Name)
	require.True(t, strings.HasPrefix(cookie.Name, "__Host-"))
	require.True(t, cookie.Secure)
	require.True(t, cookie.HttpOnly)
	require.Equal(t, "/", cookie.Path)
	require.Empty(t, cookie.Domain)
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)

	cleared := c.ClearCookie()
	require.Equal(t, auth.SessionCookie, cleared.Name)
	require.Negative(t, cleared.MaxAge)
	require.True(t, cleared.Secure)
	require.Equal(t, "/", cleared.Path)
}
