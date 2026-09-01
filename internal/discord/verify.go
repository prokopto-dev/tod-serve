package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// ErrSignature is returned for every interaction this server will not accept as Discord's.
//
// It is ONE sentinel for every cause — no header, a malformed header, the wrong key, a body that
// was edited in flight, a timestamp outside the freshness window, and an instance with no public
// key configured at all. An unverified interaction is an unauthenticated write, and telling a
// caller *which* part of their forgery was wrong is telling them what to fix.
var ErrSignature = errors.New("the interaction signature is not this application's")

// FreshnessWindow is how far an interaction's timestamp may be from this instance's clock.
//
// Ed25519 says the body was signed by the application's key; it says nothing about WHEN. Without
// this, a single captured interaction is replayable for ever — and the reply to a replayed
// `/tod board` is the circle's board, delivered to whoever kept the request. Five minutes is wide
// enough that ordinary clock drift on either side does not refuse a real command and narrow enough
// that a captured request is not a standing subscription.
//
// It is symmetric on purpose: a timestamp in the FUTURE is as much a forgery signal as an old one,
// and accepting one would let a captured request be scheduled rather than merely replayed.
const FreshnessWindow = 5 * time.Minute

// Verifier checks that an interaction body was signed by the Discord application this instance is
// configured with, recently.
//
// A verifier with NO key is a usable value, and every [Verifier.Verify] on it refuses. That is
// deliberate: an instance with no key configured and an instance presented with a forged signature
// answer identically, so neither tells a stranger which one they found.
type Verifier struct {
	key ed25519.PublicKey
	now func() core.Micros
}

// NewVerifier returns a verifier over the application's hex-encoded Ed25519 public key.
//
// An EMPTY key is not an error here and is refused at every call instead. That is the same shape
// `TOD_SETUP_TOKEN` takes and for the same reason: an instance with no key configured and an
// instance presented with a wrong signature must answer identically, and a constructor that
// refused would instead make the difference visible as a startup failure on one and a 401 on the
// other.
//
// A key that is present and unusable IS a construction error. An operator who pasted half a key
// wants to know at boot, not from the first person who runs a command.
func NewVerifier(publicKeyHex string, now func() core.Micros) (*Verifier, error) {
	if now == nil {
		return nil, errors.New("discord verifier: no clock")
	}
	if publicKeyHex == "" {
		return &Verifier{now: now}, nil
	}
	raw, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode the Discord application public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf(
			"the Discord application public key is %d bytes; an Ed25519 public key is %d",
			len(raw), ed25519.PublicKeySize)
	}
	return &Verifier{key: ed25519.PublicKey(raw), now: now}, nil
}

// Configured reports whether a public key was supplied. It exists for `tod-serve doctor`, which
// tells an operator what is missing; nothing on the request path branches on it.
func (v *Verifier) Configured() bool { return v != nil && len(v.key) == ed25519.PublicKeySize }

// Verify checks the signature over `timestamp || body`, which is what Discord signs.
//
// Every refusal returns [ErrSignature] and nothing else. The order below is chosen so that no
// branch is cheaper than another in a way a caller could measure for anything useful: the key
// check comes first because an unconfigured instance has nothing to compare against at all.
//
// **The body must be the bytes as they arrived.** Re-marshalling a parsed payload changes member
// order and whitespace, and the signature is over the original: a verifier fed a re-encoded body
// refuses every genuine interaction and is discovered only in production.
func (v *Verifier) Verify(signatureHex, timestamp string, body []byte) error {
	if !v.Configured() {
		return ErrSignature
	}
	if timestamp == "" || len(body) == 0 {
		return ErrSignature
	}
	sig, err := hex.DecodeString(signatureHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrSignature
	}
	if err := v.checkFreshness(timestamp); err != nil {
		return err
	}
	// The signed message is the timestamp header concatenated with the raw body, in that order.
	signed := make([]byte, 0, len(timestamp)+len(body))
	signed = append(signed, timestamp...)
	signed = append(signed, body...)
	if !ed25519.Verify(v.key, signed, sig) {
		return ErrSignature
	}
	return nil
}

// checkFreshness refuses a timestamp outside [FreshnessWindow] in either direction.
//
// Discord sends seconds since the epoch. A value that is not one is refused rather than treated as
// zero: a zero would be fifty-odd years old and refused anyway today, but it would start being
// accepted the moment somebody widened the window, which is a trap rather than a rule.
func (v *Verifier) checkFreshness(timestamp string) error {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrSignature
	}
	signedAt := core.Micros(seconds * int64(time.Second/time.Microsecond))
	drift := v.now().Sub(signedAt)
	if drift < 0 {
		drift = -drift
	}
	if drift > FreshnessWindow {
		return ErrSignature
	}
	return nil
}
