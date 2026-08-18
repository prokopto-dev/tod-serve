package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// The failure modes, as sentinels. Like internal/identity/discord, this package must not import
// the one that owns the wire error codes, so internal/identity maps these.
var (
	// ErrCredentialInvalid is a token that does not parse, does not verify, or names claims that
	// do not hold.
	ErrCredentialInvalid = errors.New("id token is not valid")

	// ErrCredentialExpired is a token past its `exp`.
	ErrCredentialExpired = errors.New("id token has expired")

	// ErrAudienceMismatch is a token minted for another client. This is the check OIDC gets for
	// free that Discord needs a whole extra request for.
	ErrAudienceMismatch = errors.New("id token audience is not this instance")

	// ErrUnreachable is the JWKS endpoint being unavailable or unparseable.
	ErrUnreachable = errors.New("the provider's key set is unreachable")
)

// The algorithms this package will verify. An ALLOWLIST, keyed on the `alg` header, because the
// alternative lets the token pick.
//
// `none` is absent, and so is every `HS*`: an HMAC algorithm verified with the RSA public key
// from the JWKS turns a document anyone can read into a signing key. That is the oldest JWT
// forgery there is and it only ever works against a verifier that took `alg` as instruction.
var algorithms = map[string]struct {
	hash    crypto.Hash
	family  string
	sigSize int // ECDSA only: the fixed width of r and s, in bytes
}{
	"RS256": {crypto.SHA256, "RSA", 0},
	"RS384": {crypto.SHA384, "RSA", 0},
	"RS512": {crypto.SHA512, "RSA", 0},
	"PS256": {crypto.SHA256, "RSA-PSS", 0},
	"PS384": {crypto.SHA384, "RSA-PSS", 0},
	"PS512": {crypto.SHA512, "RSA-PSS", 0},
	"ES256": {crypto.SHA256, "EC", 32},
	"ES384": {crypto.SHA384, "EC", 48},
	"ES512": {crypto.SHA512, "EC", 66},
}

// header is the JOSE header, of which only two fields are read. Everything else — `jku`, `x5u`,
// `jwk` — names a key the TOKEN chose, and this verifier only ever uses a key from the JWKS the
// operator configured.
type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// claims is the ID token payload. `aud` is `[]string` in the spec and a bare string in practice,
// which is why it is decoded by hand.
type claims struct {
	Issuer    string          `json:"iss"`
	Subject   string          `json:"sub"`
	Audience  json.RawMessage `json:"aud"`
	AZP       string          `json:"azp"`
	Expiry    int64           `json:"exp"`
	IssuedAt  int64           `json:"iat"`
	NotBefore int64           `json:"nbf"`
	Nonce     string          `json:"nonce"`
}

// parsed is a decoded, signature-verified token.
type parsed struct {
	claims claims
	// raw carries the whole payload so a configured `subject_claim` other than `sub` can be read
	// without this package having to know every claim an issuer might mint.
	raw map[string]json.RawMessage
}

// verifySignature splits the compact serialisation, checks the algorithm against the allowlist,
// finds the key, and verifies. It does not look at a single claim: a verifier that reads claims
// before checking the signature is a verifier that acts on attacker-controlled data.
func verifySignature(token string, keyFor func(kid string) (publicKey, error)) (parsed, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return parsed{}, fmt.Errorf("id token has %d segments, not 3: %w", len(parts), ErrCredentialInvalid)
	}

	headerBytes, err := decodeSegment(parts[0])
	if err != nil {
		return parsed{}, fmt.Errorf("decode id token header: %w", errors.Join(ErrCredentialInvalid, err))
	}
	var h header
	if err := json.Unmarshal(headerBytes, &h); err != nil {
		return parsed{}, fmt.Errorf("parse id token header: %w", errors.Join(ErrCredentialInvalid, err))
	}
	alg, ok := algorithms[h.Alg]
	if !ok {
		return parsed{}, fmt.Errorf("id token algorithm %q is not one this verifier accepts: %w", h.Alg, ErrCredentialInvalid)
	}

	key, err := keyFor(h.Kid)
	if err != nil {
		return parsed{}, err
	}

	sig, err := decodeSegment(parts[2])
	if err != nil {
		return parsed{}, fmt.Errorf("decode id token signature: %w", errors.Join(ErrCredentialInvalid, err))
	}

	signingInput := []byte(parts[0] + "." + parts[1])
	hasher := alg.hash.New()
	// Deliberate waiver: hash.Hash's Write never returns an error, which its own documentation
	// states. Checking it would be a branch no input can reach.
	_, _ = hasher.Write(signingInput)
	digest := hasher.Sum(nil)

	if err := verifyWith(key, alg.family, alg.hash, alg.sigSize, digest, sig); err != nil {
		return parsed{}, err
	}

	payload, err := decodeSegment(parts[1])
	if err != nil {
		return parsed{}, fmt.Errorf("decode id token payload: %w", errors.Join(ErrCredentialInvalid, err))
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return parsed{}, fmt.Errorf("parse id token payload: %w", errors.Join(ErrCredentialInvalid, err))
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return parsed{}, fmt.Errorf("parse id token payload: %w", errors.Join(ErrCredentialInvalid, err))
	}
	return parsed{claims: c, raw: raw}, nil
}

// verifyWith checks a signature against one key. The key's Go type and the header's algorithm
// family must agree: an RS256 header verified against an EC key is a confusion attack, not a
// configuration mistake to paper over.
func verifyWith(key publicKey, family string, hash crypto.Hash, sigSize int, digest, sig []byte) error {
	switch family {
	case "RSA":
		pub, ok := key.rsa()
		if !ok {
			return fmt.Errorf("id token names an RSA algorithm but the key is not RSA: %w", ErrCredentialInvalid)
		}
		if err := rsa.VerifyPKCS1v15(pub, hash, digest, sig); err != nil {
			return fmt.Errorf("id token signature: %w", errors.Join(ErrCredentialInvalid, err))
		}
	case "RSA-PSS":
		pub, ok := key.rsa()
		if !ok {
			return fmt.Errorf("id token names an RSA algorithm but the key is not RSA: %w", ErrCredentialInvalid)
		}
		opts := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: hash}
		if err := rsa.VerifyPSS(pub, hash, digest, sig, opts); err != nil {
			return fmt.Errorf("id token signature: %w", errors.Join(ErrCredentialInvalid, err))
		}
	case "EC":
		pub, ok := key.ecdsa()
		if !ok {
			return fmt.Errorf("id token names an EC algorithm but the key is not EC: %w", ErrCredentialInvalid)
		}
		if len(sig) != sigSize*2 {
			return fmt.Errorf("id token EC signature is %d bytes, not %d: %w", len(sig), sigSize*2, ErrCredentialInvalid)
		}
		r := new(big.Int).SetBytes(sig[:sigSize])
		s := new(big.Int).SetBytes(sig[sigSize:])
		if !ecdsa.Verify(pub, digest, r, s) {
			return fmt.Errorf("id token signature does not verify: %w", ErrCredentialInvalid)
		}
	default:
		return fmt.Errorf("unsupported algorithm family %q: %w", family, ErrCredentialInvalid)
	}
	return nil
}

// decodeSegment reads one base64url segment. JWT segments are unpadded; a padded one is a
// different encoding and is refused rather than fixed up.
func decodeSegment(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64url: %w", err)
	}
	return b, nil
}

// audiences reads `aud`, which the spec allows to be a string or an array of strings.
func (c claims) audiences() ([]string, error) {
	if len(c.Audience) == 0 {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(c.Audience, &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(c.Audience, &many); err != nil {
		return nil, fmt.Errorf("parse aud: %w", errors.Join(ErrCredentialInvalid, err))
	}
	return many, nil
}
