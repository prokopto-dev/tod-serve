package instancesettings_test

import (
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/instancesettings"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// fixtureNow is the fixed instant every time-dependent assertion here is relative to, so a test
// that fails does not depend on when it ran.
const fixtureNow = core.Micros(1_755_483_247_000_000)

// fixture is a real migrated SQLite database in t.TempDir(), with one identity to attribute a
// change to.
//
// No mock of the database anywhere: what this package promises — append-only by trigger, a chain
// whose tail is derived rather than sorted, a `setting` column that cannot hold `public_url` — are
// all promises about rows, and a mock would let every one of them pass while the schema said
// otherwise.
type fixture struct {
	t       *testing.T
	store   *store.DB
	clock   *clock.Test
	ids     *core.Generator
	service *instancesettings.Service
	alice   core.IdentityID
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
	service, err := instancesettings.New(instancesettings.Config{
		Store: db, Clock: clk, IDs: ids, Log: log,
	})
	require.NoError(t, err)

	f := &fixture{t: t, store: db, clock: clk, ids: ids, service: service}
	f.alice = f.seedIdentity(f.seedProvider(), "alice")
	return f
}

// seedInstance writes the singleton the wizard would have written.
func (f *fixture) seedInstance(selfService bool) {
	f.t.Helper()
	flag := int64(0)
	if selfService {
		flag = 1
	}
	_, err := f.store.Queries().CreateInstance(f.t.Context(), sqlitegen.CreateInstanceParams{
		Name: "Test Instance", PublicUrl: "https://tod.example.com", Timezone: "UTC",
		SelfServiceCircleCreation: flag,
		CreatedAt:                 int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
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

// rows returns the ledger straight out of the database, oldest first, so a test can walk the chain
// without going back through the service that wrote it.
func (f *fixture) rows() []sqlitegen.InstanceSettingChange {
	f.t.Helper()
	out, err := f.store.Queries().ListInstanceSettingChanges(f.t.Context())
	require.NoError(f.t, err)
	// The query answers newest first, because that is what an administrator reads. Chain order is
	// the reverse, and reversing here rather than adding a second query keeps one ordering rule.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func ptr[T any](v T) *T { return &v }
