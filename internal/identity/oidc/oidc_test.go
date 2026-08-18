package oidc_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity/oidc"
	"github.com/prokopto-dev/tod-serve/internal/identity/outbound"
)

const (
	issuer   = "https://idp.example.com"
	clientID = "tod-serve-instance"
	jwksURI  = "https://idp.example.com/jwks.json"
	nonce    = "nonce-abc"
	kid      = "key-1"
)

// now is a fixed instant, so every `exp` in this file is readable against it.
var now = core.MicrosFromTime(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))

// jwksDoer answers the one URL this package fetches, and counts how often it was asked.
type jwksDoer struct {
	body    []byte
	status  int
	err     error
	fetches int
}

func (d *jwksDoer) Do(_ context.Context, _, _ string, _ http.Header, _ []byte) (*outbound.Response, error) {
	d.fetches++
	if d.err != nil {
		return nil, d.err
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &outbound.Response{Status: status, Header: http.Header{}, Body: d.body}, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rsaJWKS(t *testing.T, keyID string, pub *rsa.PublicKey) []byte {
	t.Helper()
	doc := map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256",
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	return b
}

// sign builds a compact JWS. Written out rather than pulled from a library so the test can mint
// the malformed and hostile shapes a library would refuse to produce.
func sign(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	h, err := json.Marshal(header)
	require.NoError(t, err)
	c, err := json.Marshal(claims)
	require.NoError(t, err)

	input := b64(h) + "." + b64(c)
	if header["alg"] == "none" {
		return input + "."
	}
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return input + "." + b64(sig)
}

func goodClaims() map[string]any {
	return map[string]any{
		"iss":   issuer,
		"sub":   "subject-1",
		"aud":   clientID,
		"exp":   now.Time().Add(5 * time.Minute).Unix(),
		"iat":   now.Time().Unix(),
		"nonce": nonce,
		"name":  "Tankguy",
	}
}

func newVerifier(t *testing.T, doer outbound.Doer, mutate func(*oidc.Config)) *oidc.Verifier {
	t.Helper()
	cfg := oidc.Config{Issuer: issuer, ClientID: clientID, JWKSURI: jwksURI}
	if mutate != nil {
		mutate(&cfg)
	}
	v, err := oidc.NewVerifier(doer, clock.NewTest(now), cfg)
	require.NoError(t, err)
	return v
}

func newRSAFixture(t *testing.T) (*rsa.PrivateKey, *jwksDoer) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key, &jwksDoer{body: rsaJWKS(t, kid, &key.PublicKey)}
}

func TestVerify_ValidToken_YieldsTheSubjectAndDisplayName(t *testing.T) {
	t.Parallel()

	key, doer := newRSAFixture(t)
	token := sign(t, key, map[string]any{"alg": "RS256", "kid": kid}, goodClaims())

	got, err := newVerifier(t, doer, nil).Verify(t.Context(), token, nonce)

	require.NoError(t, err)
	require.Equal(t, oidc.Identity{Subject: "subject-1", DisplayName: "Tankguy"}, got)
}

// The algorithm allowlist, from both ends. `none` is a token that says it needs no signature;
// HS256 is a token asking to be verified with the public key as an HMAC secret, which is a key
// the attacker fetched from the JWKS themselves.
func TestVerify_AlgorithmsOutsideTheAllowlist_AreRefused(t *testing.T) {
	t.Parallel()

	key, doer := newRSAFixture(t)

	t.Run("alg none", func(t *testing.T) {
		t.Parallel()
		token := sign(t, key, map[string]any{"alg": "none", "kid": kid}, goodClaims())
		_, err := newVerifier(t, doer, nil).Verify(t.Context(), token, nonce)
		require.ErrorIs(t, err, oidc.ErrCredentialInvalid)
	})

	t.Run("HS256 signed with the public modulus", func(t *testing.T) {
		t.Parallel()
		h, err := json.Marshal(map[string]any{"alg": "HS256", "kid": kid})
		require.NoError(t, err)
		c, err := json.Marshal(goodClaims())
		require.NoError(t, err)
		input := b64(h) + "." + b64(c)
		mac := hmac.New(sha256.New, key.PublicKey.N.Bytes())
		_, err = mac.Write([]byte(input))
		require.NoError(t, err)
		token := input + "." + b64(mac.Sum(nil))

		_, err = newVerifier(t, doer, nil).Verify(t.Context(), token, nonce)
		require.ErrorIs(t, err, oidc.ErrCredentialInvalid)
	})
}

func TestVerify_ClaimsThatDoNotHold_AreRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{"another issuer", func(c map[string]any) { c["iss"] = "https://evil.example" }, oidc.ErrCredentialInvalid},
		{"issuer as a prefix is not the issuer", func(c map[string]any) { c["iss"] = issuer + "/tenant-2" }, oidc.ErrCredentialInvalid},
		{"another audience", func(c map[string]any) { c["aud"] = "another-instance" }, oidc.ErrAudienceMismatch},
		{"several audiences, azp is somebody else", func(c map[string]any) {
			c["aud"] = []string{clientID, "other"}
			c["azp"] = "other"
		}, oidc.ErrAudienceMismatch},
		{"expired", func(c map[string]any) { c["exp"] = now.Time().Add(-2 * time.Minute).Unix() }, oidc.ErrCredentialExpired},
		{"no exp at all", func(c map[string]any) { delete(c, "exp") }, oidc.ErrCredentialInvalid},
		{"issued in the future", func(c map[string]any) { c["iat"] = now.Time().Add(time.Hour).Unix() }, oidc.ErrCredentialInvalid},
		{"not yet valid", func(c map[string]any) { c["nbf"] = now.Time().Add(time.Hour).Unix() }, oidc.ErrCredentialInvalid},
		{"another nonce", func(c map[string]any) { c["nonce"] = "somebody-else's" }, oidc.ErrCredentialInvalid},
		{"no nonce", func(c map[string]any) { delete(c, "nonce") }, oidc.ErrCredentialInvalid},
		{"no subject", func(c map[string]any) { delete(c, "sub") }, oidc.ErrCredentialInvalid},
		{"subject is a number", func(c map[string]any) { c["sub"] = 7 }, oidc.ErrCredentialInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key, doer := newRSAFixture(t)
			claims := goodClaims()
			tt.mutate(claims)
			token := sign(t, key, map[string]any{"alg": "RS256", "kid": kid}, claims)

			_, err := newVerifier(t, doer, nil).Verify(t.Context(), token, nonce)

			require.ErrorIs(t, err, tt.want)
		})
	}
}

// Several audiences with the right azp is legitimate, and refusing it would break issuers that
// mint one token for a family of clients.
func TestVerify_SeveralAudiencesWithOurAZP_IsAccepted(t *testing.T) {
	t.Parallel()

	key, doer := newRSAFixture(t)
	claims := goodClaims()
	claims["aud"] = []string{"other", clientID}
	claims["azp"] = clientID
	token := sign(t, key, map[string]any{"alg": "RS256", "kid": kid}, claims)

	got, err := newVerifier(t, doer, nil).Verify(t.Context(), token, nonce)

	require.NoError(t, err)
	require.Equal(t, "subject-1", got.Subject)
}

func TestVerify_TamperedPayload_IsRefused(t *testing.T) {
	t.Parallel()

	key, doer := newRSAFixture(t)
	token := sign(t, key, map[string]any{"alg": "RS256", "kid": kid}, goodClaims())

	parts := strings.Split(token, ".")
	forged := goodClaims()
	forged["sub"] = "somebody-else"
	b, err := json.Marshal(forged)
	require.NoError(t, err)
	tampered := parts[0] + "." + b64(b) + "." + parts[2]

	_, err = newVerifier(t, doer, nil).Verify(t.Context(), tampered, nonce)

	require.ErrorIs(t, err, oidc.ErrCredentialInvalid)
}

func TestVerify_MalformedTokens_AreRefused(t *testing.T) {
	t.Parallel()

	_, doer := newRSAFixture(t)
	v := newVerifier(t, doer, nil)

	for _, token := range []string{"", "a.b", "a.b.c.d", "!!!.###.$$$"} {
		_, err := v.Verify(t.Context(), token, nonce)
		require.ErrorIs(t, err, oidc.ErrCredentialInvalid, "token %q", token)
	}
}

// `subject_claim` exists because some issuers mint a stable identifier under another name. It is
// still read as an exact claim, never inferred.
func TestVerify_ConfiguredSubjectClaim_IsHonoured(t *testing.T) {
	t.Parallel()

	key, doer := newRSAFixture(t)
	claims := goodClaims()
	claims["employee_number"] = "e-4471"
	token := sign(t, key, map[string]any{"alg": "RS256", "kid": kid}, claims)

	got, err := newVerifier(t, doer, func(c *oidc.Config) { c.SubjectClaim = "employee_number" }).
		Verify(t.Context(), token, nonce)

	require.NoError(t, err)
	require.Equal(t, "e-4471", got.Subject)
}

func TestVerify_NoNonceToCheckAgainst_IsRefusedRatherThanSkipped(t *testing.T) {
	t.Parallel()

	key, doer := newRSAFixture(t)
	token := sign(t, key, map[string]any{"alg": "RS256", "kid": kid}, goodClaims())

	_, err := newVerifier(t, doer, nil).Verify(t.Context(), token, "")

	require.ErrorIs(t, err, oidc.ErrCredentialInvalid)
	require.Zero(t, doer.fetches, "a token with nothing to bind it to is refused before any network")
}

func TestKeySet_WithinTTL_IsNotRefetched(t *testing.T) {
	t.Parallel()

	key, doer := newRSAFixture(t)
	v := newVerifier(t, doer, nil)
	token := sign(t, key, map[string]any{"alg": "RS256", "kid": kid}, goodClaims())

	for range 5 {
		_, err := v.Verify(t.Context(), token, nonce)
		require.NoError(t, err)
	}
	require.Equal(t, 1, doer.fetches)
}

// An unknown kid is both "the issuer rotated" and "somebody is turning this instance into a
// request amplifier". One refetch, then a floor on how often the next one may happen.
func TestKeySet_UnknownKid_RefetchesAtMostOncePerInterval(t *testing.T) {
	t.Parallel()

	key, doer := newRSAFixture(t)
	testClock := clock.NewTest(now)
	v, err := oidc.NewVerifier(doer, testClock, oidc.Config{Issuer: issuer, ClientID: clientID, JWKSURI: jwksURI})
	require.NoError(t, err)

	token := sign(t, key, map[string]any{"alg": "RS256", "kid": "rotated-in"}, goodClaims())

	for range 10 {
		_, err := v.Verify(t.Context(), token, nonce)
		require.ErrorIs(t, err, oidc.ErrCredentialInvalid)
	}
	require.Equal(t, 1, doer.fetches,
		"ten unknown kids inside the floor are one fetch; the document was just read and re-reading it would answer the same")

	testClock.Advance(2 * oidc.DefaultMinRefreshInterval)
	_, err = v.Verify(t.Context(), token, nonce)
	require.ErrorIs(t, err, oidc.ErrCredentialInvalid)
	require.Equal(t, 2, doer.fetches, "past the floor, a rotation is picked up")

	for range 10 {
		_, err := v.Verify(t.Context(), token, nonce)
		require.ErrorIs(t, err, oidc.ErrCredentialInvalid)
	}
	require.Equal(t, 2, doer.fetches, "and the floor applies again straight away")
}

func TestKeySet_HostileDocuments_AreRefused(t *testing.T) {
	t.Parallel()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	token := sign(t, key, map[string]any{"alg": "RS256", "kid": kid}, goodClaims())

	oversized := make([]map[string]any, 0, oidc.DefaultMaxKeys+1)
	for i := range oidc.DefaultMaxKeys + 1 {
		oversized = append(oversized, map[string]any{
			"kty": "RSA", "kid": string(rune('a' + i)), "use": "sig",
			"n": b64(key.PublicKey.N.Bytes()), "e": b64(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		})
	}
	oversizedDoc, err := json.Marshal(map[string]any{"keys": oversized})
	require.NoError(t, err)

	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	weakDoc, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": kid, "use": "sig",
		"n": b64(weak.PublicKey.N.Bytes()), "e": b64(big.NewInt(int64(weak.PublicKey.E)).Bytes()),
	}}})
	require.NoError(t, err)

	offCurve, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "EC", "kid": kid, "crv": "P-256", "use": "sig",
		"x": b64(big.NewInt(1).Bytes()), "y": b64(big.NewInt(2).Bytes()),
	}}})
	require.NoError(t, err)

	tests := []struct {
		name string
		doer *jwksDoer
	}{
		{"more keys than the cap", &jwksDoer{body: oversizedDoc}},
		{"an RSA key under the 2048-bit floor", &jwksDoer{body: weakDoc}},
		{"an EC point that is not on its curve", &jwksDoer{body: offCurve}},
		{"not JSON at all", &jwksDoer{body: []byte("<html>404</html>")}},
		{"an empty key set", &jwksDoer{body: []byte(`{"keys":[]}`)}},
		{"a 500 from the issuer", &jwksDoer{status: http.StatusInternalServerError, body: []byte("{}")}},
		{"an unreachable issuer", &jwksDoer{err: errors.New("connection refused")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := newVerifier(t, tt.doer, nil).Verify(t.Context(), token, nonce)
			require.Error(t, err)
			require.NotErrorIs(t, err, nil)
		})
	}
}

// `use: enc` is an encryption key. Verifying a signature with one is a category error, and
// issuers do publish both in one document.
func TestKeySet_EncryptionKeys_AreSkipped(t *testing.T) {
	t.Parallel()

	sigKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	encKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	doc, err := json.Marshal(map[string]any{"keys": []map[string]any{
		{
			"kty": "RSA", "kid": "enc-1", "use": "enc",
			"n": b64(encKey.PublicKey.N.Bytes()), "e": b64(big.NewInt(int64(encKey.PublicKey.E)).Bytes()),
		},
		{
			"kty": "RSA", "kid": kid, "use": "sig",
			"n": b64(sigKey.PublicKey.N.Bytes()), "e": b64(big.NewInt(int64(sigKey.PublicKey.E)).Bytes()),
		},
	}})
	require.NoError(t, err)

	token := sign(t, sigKey, map[string]any{"alg": "RS256", "kid": kid}, goodClaims())
	got, err := newVerifier(t, &jwksDoer{body: doc}, nil).Verify(t.Context(), token, nonce)

	require.NoError(t, err)
	require.Equal(t, "subject-1", got.Subject)
}

// The EC path, signed the way a real issuer signs: r||s, fixed width, not ASN.1.
func TestVerify_ES256Token_Verifies(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	doc, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "EC", "kid": kid, "crv": "P-256", "use": "sig",
		"x": b64(key.PublicKey.X.FillBytes(make([]byte, 32))),
		"y": b64(key.PublicKey.Y.FillBytes(make([]byte, 32))),
	}}})
	require.NoError(t, err)

	h, err := json.Marshal(map[string]any{"alg": "ES256", "kid": kid})
	require.NoError(t, err)
	c, err := json.Marshal(goodClaims())
	require.NoError(t, err)
	input := b64(h) + "." + b64(c)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	require.NoError(t, err)
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)

	got, err := newVerifier(t, &jwksDoer{body: doc}, nil).Verify(t.Context(), input+"."+b64(sig), nonce)

	require.NoError(t, err)
	require.Equal(t, "subject-1", got.Subject)
}

func TestNewVerifier_IncompleteConfiguration_IsRefused(t *testing.T) {
	t.Parallel()

	full := oidc.Config{Issuer: issuer, ClientID: clientID, JWKSURI: jwksURI}
	for name, mutate := range map[string]func(oidc.Config) oidc.Config{
		"no issuer":    func(c oidc.Config) oidc.Config { c.Issuer = ""; return c },
		"no client id": func(c oidc.Config) oidc.Config { c.ClientID = ""; return c },
		"no jwks uri":  func(c oidc.Config) oidc.Config { c.JWKSURI = ""; return c },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := oidc.NewVerifier(&jwksDoer{}, clock.NewTest(now), mutate(full))
			require.Error(t, err)
		})
	}
}

// The allowlist is the endpoints this instance FETCHES. The authorization endpoint is where the
// browser goes; putting it on the allowlist would widen the outbound surface for no fetch.
func TestConfig_Hosts_NamesOnlyWhatIsFetched(t *testing.T) {
	t.Parallel()

	hosts, err := oidc.Config{
		JWKSURI:               "https://keys.idp.example.com/jwks",
		TokenEndpoint:         "https://token.idp.example.com/oauth/token",
		AuthorizationEndpoint: "https://login.idp.example.com/authorize",
	}.Hosts()

	require.NoError(t, err)
	require.Equal(t, []string{"keys.idp.example.com", "token.idp.example.com"}, hosts)
}

// The nonce is derived from the PKCE verifier rather than stored, so the callback can recompute
// it. That only works if the derivation is stable and if it is not the verifier itself.
func TestNonceFor_IsStableAndIsNotTheVerifier(t *testing.T) {
	t.Parallel()

	require.Equal(t, oidc.NonceFor("verifier-1"), oidc.NonceFor("verifier-1"))
	require.NotEqual(t, oidc.NonceFor("verifier-1"), oidc.NonceFor("verifier-2"))
	require.NotContains(t, oidc.NonceFor("verifier-1"), "verifier-1")
}

func TestAuthorizationURL_CarriesTheDerivedNonceAndTheChallengeNotTheVerifier(t *testing.T) {
	t.Parallel()

	v := newVerifier(t, &jwksDoer{}, func(c *oidc.Config) {
		c.AuthorizationEndpoint = "https://idp.example.com/authorize"
		c.RedirectURI = "https://tod.example.com/api/v1/auth/callback/authentik"
	})

	got := v.AuthorizationURL("state-1", "verifier-1", oidc.Scopes())

	require.Contains(t, got, "nonce="+oidc.NonceFor("verifier-1"))
	require.Contains(t, got, "code_challenge_method=S256")
	require.NotContains(t, got, "verifier-1")
	require.Contains(t, got, "scope=openid+profile")
}
