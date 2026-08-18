package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity/outbound"
)

// The cache's bounds. Every one of them exists because the document on the other end is written
// by somebody the operator trusts and this instance does not.
const (
	// DefaultKeyTTL is how long a fetched key set is used without asking again. Issuers rotate on
	// the order of days; an hour is short enough that a rotation is picked up before anybody
	// notices and long enough that a join does not wait on a network round trip.
	DefaultKeyTTL = time.Hour

	// DefaultMinRefreshInterval bounds how often an UNKNOWN kid can force a refetch. Without it,
	// a stream of tokens carrying random `kid` values is an amplifier: every one becomes an
	// outbound request to the issuer, on this instance's behalf and at this instance's expense.
	DefaultMinRefreshInterval = time.Minute

	// DefaultMaxKeys caps how many keys one document may declare. A JWKS with four keys is a
	// rotation in progress; one with forty thousand is a memory-exhaustion attempt wearing a
	// content type.
	DefaultMaxKeys = 16
)

// publicKey is one verified key from a JWKS, in whichever of the two shapes it arrived.
//
// It is a struct with two typed pointers rather than a crypto.PublicKey, so that the algorithm
// family check in verifyWith is a compile-checked accessor rather than a type switch somebody can
// forget to write the default arm of.
type publicKey struct {
	rsaKey   *rsa.PublicKey
	ecdsaKey *ecdsa.PublicKey
}

func (k publicKey) rsa() (*rsa.PublicKey, bool)     { return k.rsaKey, k.rsaKey != nil }
func (k publicKey) ecdsa() (*ecdsa.PublicKey, bool) { return k.ecdsaKey, k.ecdsaKey != nil }

// jwk is one key as the document spells it.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// keySet caches one issuer's keys. It is safe for concurrent use: several joins verifying at once
// is the normal case, and each one holding its own copy of the document would defeat the cache.
type keySet struct {
	http  outbound.Doer
	uri   string
	clock clock.Clock

	ttl        time.Duration
	minRefresh time.Duration
	maxKeys    int

	mu          sync.Mutex
	keys        map[string]publicKey
	fetchedAt   core.Micros
	lastAttempt core.Micros
	haveFetched bool
}

// keyFor returns the key with the given kid, fetching or refreshing if it has to.
//
// An empty kid — legal, and what a single-key issuer often emits — matches when the set holds
// exactly one key. It deliberately does NOT try every key in turn: trying keys until one verifies
// is how a verifier ends up accepting a signature from a key that was rotated out for a reason.
func (s *keySet) keyFor(ctx context.Context, kid string) (publicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	stale := !s.haveFetched || now.Sub(s.fetchedAt) >= s.ttl
	if stale {
		if err := s.fetchLocked(ctx, now); err != nil {
			return publicKey{}, err
		}
		now = s.clock.Now()
	}

	if k, ok := s.lookupLocked(kid); ok {
		return k, nil
	}

	// An unknown kid is the signal that the issuer rotated between now and the last fetch, so one
	// refetch is warranted — rate-limited, because it is also the signal an attacker sends to
	// turn this instance into a request amplifier.
	if now.Sub(s.lastAttempt) >= s.minRefresh {
		if err := s.fetchLocked(ctx, now); err != nil {
			return publicKey{}, err
		}
		if k, ok := s.lookupLocked(kid); ok {
			return k, nil
		}
	}
	return publicKey{}, fmt.Errorf("no key %q in the provider's key set: %w", kid, ErrCredentialInvalid)
}

func (s *keySet) lookupLocked(kid string) (publicKey, bool) {
	if kid != "" {
		k, ok := s.keys[kid]
		return k, ok
	}
	if len(s.keys) != 1 {
		return publicKey{}, false
	}
	for _, k := range s.keys {
		return k, true
	}
	return publicKey{}, false
}

// fetchLocked reads the JWKS. lastAttempt moves whether or not the fetch succeeds, so a failing
// issuer is retried at the same bounded rate as a rotating one rather than on every request.
func (s *keySet) fetchLocked(ctx context.Context, now core.Micros) error {
	s.lastAttempt = now

	resp, err := s.http.Do(ctx, http.MethodGet, s.uri, http.Header{"Accept": {"application/json"}}, nil)
	if err != nil {
		return fmt.Errorf("fetch key set %s: %w", s.uri, errors.Join(ErrUnreachable, err))
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("fetch key set %s: status %d: %w", s.uri, resp.Status, ErrUnreachable)
	}

	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return fmt.Errorf("parse key set %s: %w", s.uri, errors.Join(ErrUnreachable, err))
	}
	if len(doc.Keys) > s.maxKeys {
		return fmt.Errorf("key set %s declares %d keys, over the %d cap: %w", s.uri, len(doc.Keys), s.maxKeys, ErrUnreachable)
	}

	keys := make(map[string]publicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		// `use: enc` is an encryption key. Verifying a signature with one is a category error,
		// and issuers do publish both in one document.
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		parsedKey, err := parseJWK(k)
		if err != nil {
			// One unusable key does not spoil the set: issuers publish algorithms we do not
			// verify, and refusing the whole document would make an issuer adding EdDSA an
			// outage. The key is skipped, and a token that names it fails as an unknown kid.
			continue
		}
		keys[k.Kid] = parsedKey
	}
	if len(keys) == 0 {
		return fmt.Errorf("key set %s holds no usable signing key: %w", s.uri, ErrUnreachable)
	}

	s.keys, s.fetchedAt, s.haveFetched = keys, now, true
	return nil
}

// parseJWK turns one JWK into a key this package can verify with.
func parseJWK(k jwk) (publicKey, error) {
	// The JWK `kty` values for the two asymmetric key types spell the same words as the JWS
	// signature families, so the same constants name both.
	switch k.Kty {
	case familyRSA:
		n, err := decodeBigInt(k.N)
		if err != nil {
			return publicKey{}, fmt.Errorf("rsa modulus: %w", err)
		}
		e, err := decodeBigInt(k.E)
		if err != nil {
			return publicKey{}, fmt.Errorf("rsa exponent: %w", err)
		}
		if !e.IsInt64() || e.Int64() <= 0 || e.Int64() > 1<<31 {
			return publicKey{}, errors.New("rsa exponent is out of range")
		}
		// 2048 bits is the floor every current guideline names. A 512-bit modulus in a JWKS is
		// either a decade-old issuer or an attacker offering a key they can factor.
		if n.BitLen() < 2048 {
			return publicKey{}, fmt.Errorf("rsa modulus is %d bits, under the 2048-bit floor", n.BitLen())
		}
		return publicKey{rsaKey: &rsa.PublicKey{N: n, E: int(e.Int64())}}, nil

	case familyEC:
		var (
			curve elliptic.Curve
			size  int // the fixed width of each coordinate on this curve, in bytes
		)
		switch k.Crv {
		case "P-256":
			curve, size = elliptic.P256(), 32
		case "P-384":
			curve, size = elliptic.P384(), 48
		case "P-521":
			curve, size = elliptic.P521(), 66
		default:
			return publicKey{}, fmt.Errorf("unsupported curve %q", k.Crv)
		}
		x, err := decodeBigInt(k.X)
		if err != nil {
			return publicKey{}, fmt.Errorf("ec x: %w", err)
		}
		y, err := decodeBigInt(k.Y)
		if err != nil {
			return publicKey{}, fmt.Errorf("ec y: %w", err)
		}
		if x.BitLen() > size*8 || y.BitLen() > size*8 {
			return publicKey{}, errors.New("ec coordinate is wider than the named curve")
		}
		// The uncompressed point encoding, parsed by the standard library so the ON-CURVE check
		// is the standard library's. An off-curve point is an invalid-curve attack, not a typo,
		// and hand-rolling that check is exactly the kind of arithmetic worth not owning.
		point := make([]byte, 1+2*size)
		point[0] = 4
		x.FillBytes(point[1 : 1+size])
		y.FillBytes(point[1+size:])
		pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
		if err != nil {
			return publicKey{}, fmt.Errorf("ec point: %w", err)
		}
		return publicKey{ecdsaKey: pub}, nil

	default:
		return publicKey{}, fmt.Errorf("unsupported key type %q", k.Kty)
	}
}

func decodeBigInt(s string) (*big.Int, error) {
	if s == "" {
		return nil, errors.New("empty")
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64url: %w", err)
	}
	return new(big.Int).SetBytes(b), nil
}
