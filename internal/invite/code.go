package invite

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The code's shape. `TODI-XXXXX-XXXXX`: a fixed scheme so a code is recognisable in a Discord
// message, and two groups of five so somebody reading one aloud has a place to pause.
const (
	// Scheme prefixes every code. It is greppable on purpose — the same reasoning as
	// `tods_pat_`: a credential that looks like random base32 is one nobody can find in a paste.
	Scheme = "TODI"
	// GroupLen is the number of payload characters in each group.
	GroupLen = 5
	// Groups is how many groups a code carries.
	Groups = 2
	// PayloadLen is the number of payload characters, and CodeBits/5.
	PayloadLen = GroupLen * Groups
	// CodeBits is the entropy in a code. Fifty bits over an instance-unique index, behind a
	// shared rate limit, is what makes guessing one uneconomic.
	CodeBits = PayloadLen * 5
)

// alphabet is Crockford base32 — the same alphabet ULIDs and token prefixes use, so an operator
// reading a log line meets one alphabet rather than three. It already excludes I, L, O and U.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ErrMalformedCode is returned for a string that is not a code.
var ErrMalformedCode = errors.New("malformed invite code")

// Code is a canonical invite or owner-grant code: `TODI-XXXXX-XXXXX`, uppercase, separators
// included. It is a distinct type so that a raw string a caller typed cannot be stored, hashed or
// compared without going through [Parse].
type Code string

// String returns the canonical form.
func (c Code) String() string { return string(c) }

// Prefix returns the display-only half — the first group — which is what `invite.code_prefix`
// holds and what an officer recognises in a list. It is never looked up.
func (c Code) Prefix() string {
	payload := strings.ReplaceAll(strings.TrimPrefix(string(c), Scheme+"-"), "-", "")
	if len(payload) < GroupLen {
		return payload
	}
	return payload[:GroupLen]
}

// Mint returns a new code from random.
//
// The randomness is injected rather than reached for, so the wiring site chooses it and a test can
// prove the encoding is exactly what it claims. It must be a cryptographic source.
func Mint(random io.Reader) (Code, error) {
	if random == nil {
		return "", errors.New("mint invite code: randomness source is nil")
	}
	// Seven bytes is 56 bits, of which the top 50 are used. Reading whole bytes and discarding
	// the remainder keeps the draw uniform: taking the low bits of a modulus would not be.
	buf := make([]byte, 7)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", fmt.Errorf("mint invite code: %w", err)
	}
	var bits uint64
	for _, b := range buf {
		bits = bits<<8 | uint64(b)
	}
	bits >>= 56 - CodeBits

	payload := make([]byte, PayloadLen)
	for i := PayloadLen - 1; i >= 0; i-- {
		payload[i] = alphabet[bits&0x1f]
		bits >>= 5
	}
	return Code(Scheme + "-" + string(payload[:GroupLen]) + "-" + string(payload[GroupLen:])), nil
}

// Parse reads a code a human typed.
//
// It is deliberately generous about everything that carries no information — case, whitespace,
// separators, the scheme — and strict about everything that does. A code arrives pasted out of a
// Discord message that capitalised the first letter, typed by somebody reading it off a phone, or
// copied without the `TODI-` because the link's fragment was truncated. All of those are the same
// code, and a server that refused them would send the person back to an officer for a fresh one.
//
// The Crockford substitutions are part of the same bargain: `I` and `l` are `1`, `o` is `0`. `U`
// is NOT substituted — Crockford excludes it from the alphabet rather than folding it, so a `U`
// is a typo we cannot guess the intent of, and guessing would be the confident mistake.
func Parse(raw string) (Code, error) {
	var compact strings.Builder
	compact.Grow(len(Scheme) + PayloadLen)
	for _, r := range strings.ToUpper(raw) {
		switch r {
		case ' ', '\t', '\n', '\r', '-', '_':
			// Separators and whitespace carry nothing.
		default:
			compact.WriteRune(substitute(r))
		}
	}

	// The scheme is optional and is compared AFTER substitution, not before it. `TODI` contains an
	// `O` and an `I`, which the substitution above turns into `0` and `1` — so a prefix check
	// written against the literal four characters never matches its own scheme. That was a real
	// bug, found by the end-to-end test, and this comment is here so it is not reintroduced by
	// somebody moving the strip earlier for readability.
	got := compact.String()
	if len(got) == len(Scheme)+PayloadLen && strings.HasPrefix(got, substituteAll(Scheme)) {
		got = got[len(Scheme):]
	}
	if len(got) != PayloadLen {
		return "", fmt.Errorf("parse invite code: %w: %d payload characters, not %d",
			ErrMalformedCode, len(got), PayloadLen)
	}
	for _, r := range got {
		if !strings.ContainsRune(alphabet, r) {
			return "", fmt.Errorf("parse invite code: %w: %q is not in the alphabet",
				ErrMalformedCode, string(r))
		}
	}
	return Code(Scheme + "-" + got[:GroupLen] + "-" + got[GroupLen:]), nil
}

// substitute folds the characters Crockford base32 treats as look-alikes.
//
// `I` and `l` are `1`; `O` is `0`. `U` is NOT folded — Crockford excludes it from the alphabet
// rather than substituting it, so a `U` is a typo whose intent we cannot guess, and guessing would
// be exactly the confident mistake this project is built against.
func substitute(r rune) rune {
	switch r {
	case 'I', 'L':
		return '1'
	case 'O':
		return '0'
	default:
		return r
	}
}

func substituteAll(s string) string {
	return strings.Map(substitute, strings.ToUpper(s))
}

// Hash is how a code becomes `invite.code_hash`.
//
// SHA-256 with no salt and no stretching is correct here and would not be for a password: the
// input is fifty bits of server-minted entropy in a fixed alphabet, so there is no dictionary to
// run, and the lookup is on the hot path of every join.
func Hash(c Code) []byte {
	sum := sha256.Sum256([]byte(c))
	return sum[:]
}

// HashCode is the hash `identitysql.New` is handed.
//
// It takes a raw string rather than a [Code] because internal/identity resolves whatever the
// caller sent and never parses one itself. A string that is not a code hashes to something that
// matches no row, which is the same answer as an unknown code — so an unparseable code and a code
// nobody issued are indistinguishable, which is what they should be to somebody guessing.
func HashCode(raw string) []byte {
	code, err := Parse(raw)
	if err != nil {
		sum := sha256.Sum256([]byte("unparseable:" + raw))
		return sum[:]
	}
	return Hash(code)
}
