package discord_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/discord"
)

// verifyNow is the instant every signature test is signed and checked at. A constant, because the
// freshness window is one of the things under test and a wall-clock reading would make the
// boundary cases flaky rather than exact.
const verifyNow = core.Micros(1_755_483_247_000_000)

// signingKey is deterministic on purpose: a failing signature test has to be reproducible, and
// this is the one credential whose FAILURE path is the point of the test.
func signingKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func verifierFor(t *testing.T, key ed25519.PrivateKey, at core.Micros) *discord.Verifier {
	t.Helper()
	clk := clock.NewTest(at)
	pub, ok := key.Public().(ed25519.PublicKey)
	require.True(t, ok)
	v, err := discord.NewVerifier(hex.EncodeToString(pub), clk.Now)
	require.NoError(t, err)
	return v
}

func sign(t *testing.T, key ed25519.PrivateKey, timestamp string, body []byte) string {
	t.Helper()
	return hex.EncodeToString(ed25519.Sign(key, append([]byte(timestamp), body...)))
}

// The happy path, so the rejections below mean something. A test suite that only drove rejections
// would pass with a verifier that refused everything, which is the same hole facing the other way.
func TestVerify_AGenuineInteraction_IsAccepted(t *testing.T) {
	t.Parallel()
	key := signingKey(t)
	body := []byte(`{"type":1,"id":"42"}`)
	timestamp := strconv.FormatInt(verifyNow.Time().Unix(), 10)

	require.NoError(t, verifierFor(t, key, verifyNow).
		Verify(sign(t, key, timestamp, body), timestamp, body))
}

// **This is the test that matters.** A suite driving only valid signatures passes with the check
// commented out, which is exactly how this repository's gate defects have shipped: the hole is
// invisible from the happy path, and every one of these cases is an UNAUTHENTICATED WRITE if it
// gets through.
//
// Every case is refused with the same [discord.ErrSignature] and no other information, because
// telling a forger which part of their forgery was wrong is telling them what to fix.
func TestVerify_AnUnsignedOrForgedInteraction_IsRefused(t *testing.T) {
	t.Parallel()
	key := signingKey(t)
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	body := []byte(`{"type":2,"id":"42","channel_id":"1"}`)
	timestamp := strconv.FormatInt(verifyNow.Time().Unix(), 10)
	good := sign(t, key, timestamp, body)

	tests := []struct {
		name      string
		signature string
		timestamp string
		body      []byte
	}{
		{"no signature at all", "", timestamp, body},
		{"no timestamp", good, "", body},
		{"empty body", good, timestamp, nil},
		{"signature is not hex", "not-hex-at-all", timestamp, body},
		{"signature is hex of the wrong length", hex.EncodeToString([]byte{1, 2, 3}), timestamp, body},
		{
			// The single commonest forgery: a body edited after signing. One byte is enough.
			name: "the body was edited in flight", signature: good, timestamp: timestamp,
			body: []byte(`{"type":2,"id":"43","channel_id":"1"}`),
		},
		{
			// The signature covers the timestamp too, so replaying a body under a fresh timestamp
			// does not verify — which is what stops the freshness window being sidestepped.
			name:      "the timestamp was changed to a fresh one",
			signature: good,
			timestamp: strconv.FormatInt(verifyNow.Add(time.Minute).Time().Unix(), 10),
			body:      body,
		},
		{
			name:      "signed by a different application",
			signature: sign(t, other, timestamp, body),
			timestamp: timestamp, body: body,
		},
		{
			// A well-formed signature over a stale timestamp. Ed25519 says WHO signed, never
			// WHEN: without the window a single captured interaction is replayable for ever, and
			// the reply to a replayed `/tod board` is the circle's board.
			name: "signed six minutes ago",
			signature: sign(t, key,
				strconv.FormatInt(verifyNow.Add(-6*time.Minute).Time().Unix(), 10), body),
			timestamp: strconv.FormatInt(verifyNow.Add(-6*time.Minute).Time().Unix(), 10),
			body:      body,
		},
		{
			// And the same in the other direction, so a captured request cannot be scheduled.
			name: "signed six minutes from now",
			signature: sign(t, key,
				strconv.FormatInt(verifyNow.Add(6*time.Minute).Time().Unix(), 10), body),
			timestamp: strconv.FormatInt(verifyNow.Add(6*time.Minute).Time().Unix(), 10),
			body:      body,
		},
		{
			name:      "the timestamp is not a number",
			signature: sign(t, key, "yesterday", body),
			timestamp: "yesterday", body: body,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := verifierFor(t, key, verifyNow).Verify(tc.signature, tc.timestamp, tc.body)
			require.ErrorIs(t, err, discord.ErrSignature,
				"an unverified interaction is an unauthenticated write")
		})
	}
}

// The boundary of the freshness window, from both sides, so the constant means what it says. Five
// minutes minus a second is accepted and five minutes plus a second is not: a test that only drove
// "six minutes" would pass with a window of an hour.
func TestVerify_TheFreshnessWindow_IsFiveMinutesEitherWay(t *testing.T) {
	t.Parallel()
	key := signingKey(t)
	body := []byte(`{"type":1}`)

	for _, tc := range []struct {
		name    string
		offset  time.Duration
		refused bool
	}{
		{"just inside, in the past", -(discord.FreshnessWindow - time.Second), false},
		{"just inside, in the future", discord.FreshnessWindow - time.Second, false},
		{"just outside, in the past", -(discord.FreshnessWindow + time.Second), true},
		{"just outside, in the future", discord.FreshnessWindow + time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := verifyNow.Add(tc.offset)
			timestamp := strconv.FormatInt(at.Time().Unix(), 10)
			err := verifierFor(t, key, verifyNow).
				Verify(sign(t, key, timestamp, body), timestamp, body)
			if tc.refused {
				require.ErrorIs(t, err, discord.ErrSignature)
				return
			}
			require.NoError(t, err)
		})
	}
}

// An instance with no `TOD_DISCORD_PUBLIC_KEY` refuses every interaction, and it refuses one
// carrying a perfectly valid signature the same way it refuses garbage. An operator who has not
// set the key up has an endpoint that answers strangers exactly as it answers Discord.
func TestVerify_AnUnconfiguredInstance_RefusesEverything(t *testing.T) {
	t.Parallel()
	key := signingKey(t)
	body := []byte(`{"type":1}`)
	timestamp := strconv.FormatInt(verifyNow.Time().Unix(), 10)

	clk := clock.NewTest(verifyNow)
	v, err := discord.NewVerifier("", clk.Now)
	require.NoError(t, err, "an unset key is a state, not a startup failure")
	require.False(t, v.Configured())
	require.ErrorIs(t, v.Verify(sign(t, key, timestamp, body), timestamp, body),
		discord.ErrSignature)
}

// A key that is present and unusable is a CONSTRUCTION error: an operator who pasted half a key
// finds out at boot rather than from the first person who runs a command. That is the opposite of
// the empty case above, and the two are one decision apart.
func TestNewVerifier_AMalformedKey_IsAStartupFailure(t *testing.T) {
	t.Parallel()
	clk := clock.NewTest(verifyNow)

	for _, tc := range []struct{ name, key string }{
		{"not hex", "zzzz"},
		{"too short", hex.EncodeToString([]byte{1, 2, 3})},
		{"too long", strings.Repeat("ab", ed25519.PublicKeySize+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := discord.NewVerifier(tc.key, clk.Now)
			require.Error(t, err)
		})
	}
}

// The bytes as they arrived, never a re-encoding of a parsed payload. Discord signs the body it
// sent, so a verifier fed a re-marshalled body refuses every genuine interaction — a failure that
// looks like a wrong key and is discovered in production.
func TestVerify_AReencodedBody_DoesNotVerify(t *testing.T) {
	t.Parallel()
	key := signingKey(t)
	// The same JSON, with different whitespace and member order: identical to a parser, different
	// to a signature.
	asSent := []byte(`{"type": 1, "id": "42"}`)
	reencoded := []byte(`{"id":"42","type":1}`)
	timestamp := strconv.FormatInt(verifyNow.Time().Unix(), 10)
	signature := sign(t, key, timestamp, asSent)

	v := verifierFor(t, key, verifyNow)
	require.NoError(t, v.Verify(signature, timestamp, asSent))
	require.ErrorIs(t, v.Verify(signature, timestamp, reencoded), discord.ErrSignature)
}
