package instancegrant_test

import (
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// fixtureNow is the fixed instant every time-dependent assertion here is relative to, so a test
// that fails does not depend on when it ran.
const fixtureNow = core.Micros(1_755_483_247_000_000)

// fixture is a real migrated SQLite database in t.TempDir() with one identity provider and two
// identities.
//
// No mock of the database anywhere: what this package promises — append-only by trigger, one tail
// per (identity, permission) by unique index, a permission column that cannot hold a circle-realm
// key — are all promises about rows, and a mock would let every one of them pass while the schema
// said otherwise.
type fixture struct {
	t       *testing.T
	store   *store.DB
	clock   *clock.Test
	ids     *core.Generator
	service *instancegrant.Service
	alice   core.IdentityID
	bob     core.IdentityID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tod.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(ctx))

	clk := clock.NewTest(fixtureNow)
	ids := core.NewGenerator(rand.Reader)
	service, err := instancegrant.New(instancegrant.Config{
		Store: db, Clock: clk, IDs: ids, Log: log,
	})
	require.NoError(t, err)

	f := &fixture{t: t, store: db, clock: clk, ids: ids, service: service}
	provider := f.seedProvider()
	f.alice = f.seedIdentity(provider, "alice")
	f.bob = f.seedIdentity(provider, "bob")
	return f
}

func (f *fixture) seedProvider() core.IdentityProviderID {
	f.t.Helper()
	id, err := core.NewID[core.IdentityProvider](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentityProvider(f.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: id.String(), Key: "local", Kind: schemaenum.IdentityProviderKindLocal,
			DisplayName: "This server", Enabled: 1, VerifiableSubject: 0,
			CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
	require.NoError(f.t, err)
	return id
}

func (f *fixture) seedIdentity(provider core.IdentityProviderID, subject string) core.IdentityID {
	f.t.Helper()
	id, err := core.NewID[core.Identity](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentity(f.t.Context(), sqlitegen.CreateIdentityParams{
		ID: id.String(), ProviderID: provider.String(), Subject: subject, DisplayName: subject,
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return id
}

// newIdentityID mints an id for an identity that does NOT exist, so a test can drive the
// unknown-identity path without relying on a hand-typed ULID.
func (f *fixture) newIdentityID() core.IdentityID {
	f.t.Helper()
	id, err := core.NewID[core.Identity](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	return id
}
