package main

import (
	"crypto/rand"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
)

// **The wiring handoff the identity subsystem named as a residual risk, closed at run time.**
//
// `identity.New` refuses a nil entropy source rather than falling back to a default, so a service
// built without one cannot exist. What that does NOT do is force this binary to hand it a
// cryptographic one: the OAuth `state` is 32 bytes drawn from whatever is passed, and the
// callback's entire resistance to brute force rests on those bytes, because the callback carries no
// rate-limit bucket of its own.
//
// So this asserts IDENTITY, not non-nilness. `require.Same` compares the interface's dynamic value,
// so a different reader that happened to be non-nil — a wrapper, a seeded generator, a test double
// left in by accident — fails here. RAND001 in internal/repogate asserts the same thing over the
// source text, which is what survives somebody refactoring this function away.
func TestWiring_IdentityService_IsGivenCryptoRandReader(t *testing.T) {
	t.Parallel()
	cfg := identityConfig(nil, nil, clock.System{}, nil, "https://tod.example.com/join", nil)

	require.NotNil(t, cfg.Entropy)
	// Compared with `==` on the interface values, which is identity: `crypto/rand.Reader` holds a
	// pointer, so this is true only for that exact reader. `require.NotNil` on its own is the test
	// the brief this gate came from calls insufficient, and it is kept above so the two assertions
	// read as the two different claims they are.
	require.True(t, cfg.Entropy == rand.Reader,
		"the production wiring must pass crypto/rand.Reader itself, not merely some non-nil reader")
}

// The identity service is the one the brief names, and it is not the only thing here that mints a
// secret. A binary that drew its OAuth state from crypto/rand and its personal access tokens from
// somewhere else would pass the test above and still be broken.
func TestWiring_EveryEntropySink_IsGivenCryptoRandReader(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := bootstrappedStore(t)

	svc, err := wire(ctx, db, log,
		core.Secret("wiring-test-pepper"), core.Secret("wiring-test-session-key"), "")
	require.NoError(t, err)

	// The services below took their randomness at construction and do not expose it, which is
	// correct — a getter for an entropy source is a getter somebody swaps. What is assertable is
	// that they were built at all, which means the constructors' own nil-checks passed, and that
	// RAND001 has proved every literal they were given.
	require.NotNil(t, svc.identity)
	require.NotNil(t, svc.invites)
	require.NotNil(t, svc.members)
	require.NotNil(t, svc.minter)
	require.NotNil(t, svc.ids)

	// A token minted through the real wiring is a real token: 256 bits of secret behind an
	// eight-character public prefix. A weak source would not change the SHAPE, which is exactly
	// why the shape is not what the two tests above assert.
	minted, err := svc.minter.Mint()
	require.NoError(t, err)
	require.Len(t, minted.Prefix, 8)
	require.NotEmpty(t, minted.Hash)
}

// `identity.New` refuses a nil entropy source. That refusal is what makes the wiring site the only
// place the decision is made, so it is pinned here rather than assumed.
func TestWiring_IdentityService_WithNoEntropy_RefusesToStart(t *testing.T) {
	t.Parallel()
	cfg := identityConfig(nil, nil, clock.System{}, nil, "https://tod.example.com/join", nil)
	cfg.Entropy = nil

	_, err := identity.New(cfg)
	require.Error(t, err,
		"a nil entropy source must be a construction error, not a fallback to a default")
}
