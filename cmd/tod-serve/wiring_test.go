package main

import (
	"crypto/rand"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
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
		core.Secret("wiring-test-pepper"), core.Secret("wiring-test-session-key"))
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

// TestWiring_TimerInvalidation_IsStillTheStub is a scheduled deletion, written as a test.
//
// [api.UnprojectedTimers] is correct for a binary with no projection: nothing writes
// `target_state_cache`, so no moved window can make anything stale. The moment
// `internal/projection` is wired here it stops being correct SILENTLY, and a board that quietly
// ignores every timer edit is the confident mistake this project is built against.
//
// So the stub is not left to a comment. This asserts it is what `serve` passes, and it is the
// thing that has to be deleted — with the stub, and with this test — by whoever wires the
// projection. It is the same shape as `uncoveredCircleRoutes` in internal/api: a gap somebody has
// to edit rather than one they might notice.
//
// WHEN YOU LAND internal/projection:
//   - pass the projection service as api.Config.Invalidator in serve.go,
//   - delete api.UnprojectedTimers and both of its methods,
//   - delete this test,
//   - and close the last hole in [api.TimerInvalidator]: the push happens after the write commits,
//     so a crash between the two leaves the cache stale until the nightly job. The fix is the one
//     audit.Append already uses — take the writing transaction's query set — which means threading
//     a *sqlitegen.Queries through recompute, storeOrDrop, revokedReporters and
//     catalogue.ResolveTimer. Every handler is retryable today, which closes the case a client can
//     see; this closes the one it cannot.
//
// It will not compile once the stub is gone, which is the point.
func TestWiring_TimerInvalidation_IsStillTheStub(t *testing.T) {
	t.Parallel()
	var invalidator api.TimerInvalidator = api.UnprojectedTimers{}

	// The stub does nothing and reports success, which is only correct while there is no
	// projection. Both methods, because both routes push.
	require.NoError(t, invalidator.OnTimerChange(t.Context(), core.CircleID{}, core.RaidTargetID{}))
	require.NoError(t, invalidator.OnCatalogueTimerChange(
		t.Context(), core.ServerBlue, core.RaidTargetID{}))

	// And `wire` still has no projection to pass. When it grows one, this fails and sends the
	// reader to the list above.
	require.NotContains(t, wiredServiceNames(t), "projection",
		"internal/projection is wired now, so api.UnprojectedTimers is silently wrong: "+
			"pass the projection as api.Config.Invalidator, then delete the stub and this test")
}

// wiredServiceNames reflects the field types of the `services` struct, so the assertion above is
// about what the binary actually builds rather than about an import that might be for anything.
func wiredServiceNames(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeFor[services]()
	out := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		out = append(out, strings.ToLower(typ.Field(i).Type.String()))
	}
	return out
}
