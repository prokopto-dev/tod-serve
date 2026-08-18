package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// TokenScheme prefixes every personal access token. It is deliberately greppable: a scanner
// looking for leaked credentials in a repository or a paste needs something to match on, and a
// token that looks like random base64 is one nobody can find.
const TokenScheme = "tods_pat_"

// PrefixLen is the length of the public, loggable half of a token. The database CHECKs it.
const PrefixLen = 8

// prefixEntropyBytes is how many random bytes the public prefix encodes. Five bytes are exactly
// eight Crockford base32 characters, so the encoding needs no padding and no truncation.
const prefixEntropyBytes = 5

// secretBytes is how many random bytes the secret half carries. 256 bits, because the only thing
// standing between a guessed token and a circle's data is this number.
const secretBytes = 32

// crockford is the base32 alphabet the prefix uses — the same one ULIDs use, so an operator
// reading a log line sees one alphabet rather than two. It excludes I, L, O and U, which is what
// makes a prefix safe to read aloud over voice chat.
var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

var (
	// ErrMalformedToken is returned for a string that is not shaped like a token at all.
	ErrMalformedToken = errors.New("malformed token")
	// ErrNoPepper is returned when a minter is built without one. A zero pepper would make the
	// stored hash a plain SHA-256 of the secret, which a stolen database file turns into an
	// offline attack; failing to start is the correct response.
	ErrNoPepper = errors.New("token pepper is empty")
)

// Minted is a freshly minted token: the string to hand the caller exactly once, and the two
// columns that go to the database.
type Minted struct {
	// Token is the whole credential, `tods_pat_<prefix>_<secret>`. It is a [core.Secret] because
	// this is the one moment it exists in plaintext anywhere.
	Token core.Secret
	// Prefix is the eight-character public half. Loggable, stored, and how a leaked token is
	// traced back to the device it was minted for.
	Prefix string
	// Hash is `HMAC-SHA256(pepper, secret)`, the only form of the secret that is ever persisted.
	Hash []byte
}

// Minter mints and verifies personal access tokens.
type Minter struct {
	pepper core.Secret
	random io.Reader
}

// NewMinter returns a minter over the instance pepper and a source of randomness.
//
// The randomness is injected rather than reached for so that a test can prove the encoding is
// exactly what it claims, and so that a future hardware source is a wiring change. It must be a
// cryptographic source; `crypto/rand.Reader` is the one to pass.
func NewMinter(pepper core.Secret, random io.Reader) (*Minter, error) {
	if pepper.IsZero() {
		return nil, fmt.Errorf("new minter: %w", ErrNoPepper)
	}
	if random == nil {
		return nil, errors.New("new minter: randomness source is nil")
	}
	return &Minter{pepper: pepper, random: random}, nil
}

// Mint returns a new token.
func (m *Minter) Mint() (Minted, error) {
	prefixRaw := make([]byte, prefixEntropyBytes)
	if _, err := io.ReadFull(m.random, prefixRaw); err != nil {
		return Minted{}, fmt.Errorf("mint token prefix: %w", err)
	}
	secretRaw := make([]byte, secretBytes)
	if _, err := io.ReadFull(m.random, secretRaw); err != nil {
		return Minted{}, fmt.Errorf("mint token secret: %w", err)
	}

	prefix := crockford.EncodeToString(prefixRaw)
	secret := base64.RawURLEncoding.EncodeToString(secretRaw)
	return Minted{
		Token:  core.Secret(TokenScheme + prefix + "_" + secret),
		Prefix: prefix,
		Hash:   m.hash(secret),
	}, nil
}

// Parse splits a presented token into its public prefix and its secret half, validating the shape
// before anything is hashed or looked up.
//
// The secret comes back as a [core.Secret] so that a caller who logs the parse result — which is
// the natural thing to do when a token is rejected — logs `***`.
func Parse(token string) (prefix string, secret core.Secret, err error) {
	rest, ok := strings.CutPrefix(token, TokenScheme)
	if !ok {
		return "", "", fmt.Errorf("parse token: %w: wrong scheme", ErrMalformedToken)
	}
	prefix, secretPart, ok := strings.Cut(rest, "_")
	if !ok {
		return "", "", fmt.Errorf("parse token: %w: no secret", ErrMalformedToken)
	}
	if len(prefix) != PrefixLen {
		return "", "", fmt.Errorf("parse token: %w: prefix is %d characters, not %d",
			ErrMalformedToken, len(prefix), PrefixLen)
	}
	if _, decodeErr := crockford.DecodeString(prefix); decodeErr != nil {
		return "", "", fmt.Errorf("parse token: %w: prefix is not Crockford base32",
			ErrMalformedToken)
	}
	if len(secretPart) == 0 {
		return "", "", fmt.Errorf("parse token: %w: empty secret", ErrMalformedToken)
	}
	return prefix, core.Secret(secretPart), nil
}

// Verify parses a presented token and returns the prefix and the hash to look up.
//
// It does NOT compare anything: the comparison is the unique index on `api_token.token_hash`,
// which is constant-time in the sense that matters — there is no per-byte loop over a secret in
// Go at all, and a lookup that misses is indistinguishable from one that hits a revoked row.
func (m *Minter) Verify(token string) (prefix string, hash []byte, err error) {
	prefix, secret, err := Parse(token)
	if err != nil {
		return "", nil, err
	}
	return prefix, m.hash(secret.Reveal()), nil
}

// hash is `HMAC-SHA256(pepper, secret)`. HMAC rather than a bare digest because the pepper is what
// makes a stolen database file useless on its own: without it, the stored value is a hash of a
// high-entropy string that an attacker who also has the code can compute.
func (m *Minter) hash(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(m.pepper.Reveal()))
	// Deliberate waiver: hash.Hash.Write is documented never to return an error, and there is
	// nothing a caller could do with one if it did.
	_, _ = mac.Write([]byte(secret))
	return mac.Sum(nil)
}
