package identity_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/identitysql"
	"github.com/prokopto-dev/tod-serve/internal/store"
)

// raceNow is the fixed instant this file's clock reports.
var raceNow = core.MicrosFromTime(time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))

// racingStore is the real store with ONE competing writer wedged into the seam this test is about.
//
// `AddProvider` reads to check the key and the kind, then writes; the two are not one transaction,
// so a second request that passed those reads a moment earlier can commit in between. Racing two
// goroutines would exercise that only sometimes and prove nothing on the run where it did not, so
// the competing write happens HERE, deterministically, at exactly the moment it is dangerous.
type racingStore struct {
	identity.Store
	// competitor is written through the real store before the call under test reaches it, exactly
	// once. It is the request that got there first.
	competitor *identity.Provider
	at         core.Micros
}

func (r *racingStore) CreateProvider(
	ctx context.Context, p identity.Provider, at core.Micros,
) (identity.Provider, error) {
	if r.competitor != nil {
		winner := *r.competitor
		r.competitor = nil
		if _, err := r.Store.CreateProvider(ctx, winner, r.at); err != nil {
			return identity.Provider{}, err
		}
	}
	return r.Store.CreateProvider(ctx, p, at)
}

// A request that loses the race to an identical one answers the documented `409 conflict`, not a
// `500`.
//
// The preflight reads in `AddProvider` are outside the write, so the loser's error comes from a
// unique index rather than from the check that exists to produce a friendly answer. Returning it
// uncoded made the edge render `internal_error`: the same request, twice, with the outcome
// decided by which was slower.
func TestAddProvider_LosingAUniquenessRace_IsAConflictAndNotAnInternalError(t *testing.T) {
	t.Parallel()

	oidc := identity.AddProviderRequest{
		Key: "corp", Kind: identity.KindOIDC, DisplayName: "Corp SSO",
		Issuer: "https://sso.example.com", JWKSURI: "https://sso.example.com/jwks",
		ClientID: "tod-serve", RedirectURI: callbackBaseURL + "/corp",
	}
	discordReq := identity.AddProviderRequest{
		Key: "discord", Kind: identity.KindDiscord, DisplayName: "Discord",
		ClientID: "1234567890", RedirectURI: callbackBaseURL + "/discord",
	}

	tests := []struct {
		name string
		// request is what the loser asks for; competitor is the row that got there first.
		request    identity.AddProviderRequest
		competitor identity.Provider
	}{
		{
			// The same key: `ux_identity_provider_key`.
			name: "the same key", request: oidc,
			competitor: identity.Provider{
				ID: "01K3TGT8N9M4X0Q7R2VB6C5D1E", Key: "corp", Kind: identity.KindOIDC,
				DisplayName: "Corp SSO", VerifiableSubject: true,
				Issuer: "https://sso.example.com", JWKSURI: "https://sso.example.com/jwks",
				ClientID: "tod-serve",
			},
		},
		{
			// A different key and the same KIND: `ux_identity_provider_discord`, which the key
			// index would not have caught at all.
			name: "another provider of the same kind", request: discordReq,
			competitor: identity.Provider{
				ID: "01K3TGT8N9M4X0Q7R2VB6C5D1F", Key: "discord-eu",
				Kind: identity.KindDiscord, DisplayName: "Discord EU", VerifiableSubject: true,
				ClientID: "9999999999", RedirectURI: "https://tod.example.com/cb",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, racing := newRacingService(t)
			racing.competitor = &tt.competitor

			_, err := svc.AddProvider(t.Context(), tt.request)
			require.Error(t, err)

			code, ok := identity.CodeOf(err)
			require.True(t, ok,
				"the loser's error carries no code, so the edge renders 500: %v", err)
			require.Equal(t, identity.CodeConflict, code)
		})
	}
}

// And with no competitor the same call succeeds, so the refusals above are about the race rather
// than about the request being wrong.
func TestAddProvider_WithNoRace_Succeeds(t *testing.T) {
	t.Parallel()
	svc, _ := newRacingService(t)

	created, err := svc.AddProvider(t.Context(), identity.AddProviderRequest{
		Key: "corp", Kind: identity.KindOIDC, DisplayName: "Corp SSO",
		Issuer: "https://sso.example.com", JWKSURI: "https://sso.example.com/jwks",
		ClientID: "tod-serve", RedirectURI: callbackBaseURL + "/corp",
	})
	require.NoError(t, err)
	require.Equal(t, "corp", created.Key)
	require.True(t, created.VerifiableSubject)
}

// newRacingService wires the real service over real SQLite in t.TempDir(), with the competing
// writer in front of the store. There is no fake here on purpose: the error being translated is
// the DRIVER's, and a fake returning something that looked like one would test the translation
// against a guess about what SQLite says.
func newRacingService(t *testing.T) (*identity.Service, *racingStore) {
	t.Helper()
	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tod.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(ctx))

	clk := clock.NewTest(raceNow)
	real, err := identitysql.New(db.Queries(), clk, func(code string) []byte {
		sum := sha256.Sum256([]byte(code))
		return sum[:]
	})
	require.NoError(t, err)

	racing := &racingStore{Store: real, at: raceNow}
	svc, err := identity.New(identity.Config{
		Store: racing, Clients: &stubClients{}, Clock: clk,
		IDs: core.NewGenerator(rand.Reader), Entropy: rand.Reader,
		SPAJoinURL: "https://tod.example.com/join", CallbackBaseURL: callbackBaseURL, Logger: log,
	})
	require.NoError(t, err)
	return svc, racing
}
