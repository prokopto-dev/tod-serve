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

// The two halves of the freshness window. **They are different numbers, and the asymmetry is the
// point.**
//
// Ed25519 says the body was signed by the application's key; it says nothing about WHEN. Without a
// window at all, a single captured interaction is replayable for ever — and the reply to a replayed
// `/tod board` is the circle's board, delivered to whoever kept the request.
//
// The two directions are bounded by different things:
//
//   - [ReplayWindow] is the PAST half, and it is a security number: how long a captured request
//     stays useful. Five minutes is wide enough that ordinary drift does not refuse a real command.
//   - [FutureSkewTolerance] is the FUTURE half, and it is a CORRECTNESS number. The signed instant
//     is what a `/tod report` records as `died_at` — see [Commander.Dispatch] — and the report log
//     carries `CHECK (died_at <= reported_at + 120000000)`. A window wider than that would verify
//     an interaction whose report the database then refuses, so a member with a two-minute-fast
//     clock would get a signature accepted and a write rejected, which is the worst pair of
//     answers available. Holding the two equal makes that unrepresentable rather than unlikely,
//     and `TestFreshness_TheFutureHalf_MatchesWhatTheReportLogAccepts` compares them.
//
// A timestamp in the future is a forgery signal as well as a skew signal — accepting a far-future
// one would let a captured request be scheduled rather than merely replayed — so the tighter of the
// two bounds is the right one to enforce in that direction either way.
const (
	ReplayWindow = 5 * time.Minute
	// FutureSkewTolerance is `tod.FutureTolerance` and the schema CHECK behind it, spelled here
	// rather than imported: this package must not depend on the report log to verify a signature.
	// The test above is the mechanism that keeps the two the same number.
	FutureSkewTolerance = 120 * time.Second
)

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

// Verify checks the signature over `timestamp || body`, which is what Discord signs, and returns
// the instant the interaction was signed at.
//
// **It RETURNS the instant rather than leaving the caller to parse the header again**, and that is
// a safety property rather than a convenience: the returned value is one that provably came from a
// verified signature, so a handler cannot accidentally use an attacker-supplied timestamp. It is
// what a `/tod report` records as `died_at`, which makes the recorded instant a function of the
// signed payload — see [Commander.Dispatch].
//
// Every refusal returns [ErrSignature] and nothing else. The order below is chosen so that no
// branch is cheaper than another in a way a caller could measure for anything useful: the key
// check comes first because an unconfigured instance has nothing to compare against at all.
//
// **The body must be the bytes as they arrived.** Re-marshalling a parsed payload changes member
// order and whitespace, and the signature is over the original: a verifier fed a re-encoded body
// refuses every genuine interaction and is discovered only in production.
func (v *Verifier) Verify(signatureHex, timestamp string, body []byte) (core.Micros, error) {
	if !v.Configured() {
		return 0, ErrSignature
	}
	if timestamp == "" || len(body) == 0 {
		return 0, ErrSignature
	}
	sig, err := hex.DecodeString(signatureHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return 0, ErrSignature
	}
	signedAt, err := v.checkFreshness(timestamp)
	if err != nil {
		return 0, err
	}
	// The signed message is the timestamp header concatenated with the raw body, in that order.
	signed := make([]byte, 0, len(timestamp)+len(body))
	signed = append(signed, timestamp...)
	signed = append(signed, body...)
	if !ed25519.Verify(v.key, signed, sig) {
		return 0, ErrSignature
	}
	return signedAt, nil
}

// checkFreshness refuses a timestamp outside the window, and returns the instant it names.
//
// Discord sends seconds since the epoch. A value that is not one is refused rather than treated as
// zero: a zero would be fifty-odd years old and refused anyway today, but it would start being
// accepted the moment somebody widened the window, which is a trap rather than a rule.
func (v *Verifier) checkFreshness(timestamp string) (core.Micros, error) {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return 0, ErrSignature
	}
	signedAt := core.Micros(seconds * int64(time.Second/time.Microsecond))
	// Signed and split by direction, never an absolute drift: the two halves bound different
	// things and collapsing them to one number was what let a verified interaction carry an
	// instant the report log would refuse.
	ahead := signedAt.Sub(v.now())
	if ahead > FutureSkewTolerance {
		return 0, ErrSignature
	}
	if -ahead > ReplayWindow {
		return 0, ErrSignature
	}
	return signedAt, nil
}
