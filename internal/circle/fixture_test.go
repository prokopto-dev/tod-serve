package circle_test

import (
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

const fixtureNow = core.Micros(1_755_483_247_000_000)

// fixture is a real migrated SQLite database in t.TempDir(). Revocation strength is derived from
// rows, so it is derived from real rows here.
type fixture struct {
	t       *testing.T
	store   *store.DB
	clock   *clock.Test
	ids     *core.Generator
	service *circle.Service
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
	service, err := circle.New(circle.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	return &fixture{t: t, store: db, clock: clk, ids: ids, service: service}
}

// provider writes one row of the instance registry.
//
// `verifiable_subject` is derived from the kind rather than passed in, because the schema derives
// it too — `CHECK ((kind = 'local') = (verifiable_subject = 0))` — and a fixture that could write
// a verifiable `local` row would be testing a database this project does not have.
func (f *fixture) provider(key, kind string, enabled bool) string {
	f.t.Helper()
	id, err := core.NewID[core.IdentityProvider](f.ids, f.clock.Now())
	require.NoError(f.t, err)

	params := sqlitegen.CreateIdentityProviderParams{
		ID: id.String(), Key: key, Kind: kind, DisplayName: key,
		Enabled: boolToInt(enabled), VerifiableSubject: 1,
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	}
	if kind == schemaenum.IdentityProviderKindLocal {
		params.VerifiableSubject = 0
	} else {
		clientID := key + "-client-id"
		params.ClientID = &clientID
	}
	if kind == schemaenum.IdentityProviderKindOIDC {
		issuer := "https://" + key + ".example.com"
		jwks := issuer + "/jwks"
		params.Issuer, params.JwksUri = &issuer, &jwks
	}
	_, err = f.store.Queries().CreateIdentityProvider(f.t.Context(), params)
	require.NoError(f.t, err)
	return id.String()
}

func (f *fixture) disableProvider(id string) {
	f.t.Helper()
	_, err := f.store.Queries().SetIdentityProviderEnabled(f.t.Context(),
		sqlitegen.SetIdentityProviderEnabledParams{
			Enabled: 0, UpdatedAt: int64(f.clock.Now()), ID: id,
		})
	require.NoError(f.t, err)
}

func (f *fixture) create(name, server string) circle.Circle {
	f.t.Helper()
	view, err := f.service.Create(f.t.Context(), circle.CreateRequest{
		Name: name, Server: core.Server(server),
	})
	require.NoError(f.t, err)
	return view
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// seedMember writes a membership, so a deletion has an actor to attribute to — `audit_log`'s
// foreign key wants a real one.
func (f *fixture) seedMember(circleID core.CircleID) core.MembershipID {
	f.t.Helper()
	providerID := f.providerOnce()
	identityID, err := core.NewID[core.Identity](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentity(f.t.Context(), sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: providerID, Subject: identityID.String(),
		DisplayName: "Tankguy", CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)

	id, err := core.NewID[core.Membership](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	subject := identityID.String()
	_, err = f.store.Queries().CreateMembership(f.t.Context(), sqlitegen.CreateMembershipParams{
		ID: id.String(), CircleID: circleID.String(), IdentityID: &subject,
		Kind: schemaenum.MembershipKindHuman, DisplayName: "Tankguy",
		DisplayNameNorm: "tankguy", Role: schemaenum.MembershipRoleOwner,
		JoinedAt: int64(fixtureNow), CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return id
}

// providerOnce writes the `local` provider at most once per fixture.
func (f *fixture) providerOnce() string {
	f.t.Helper()
	row, err := f.store.Queries().GetIdentityProviderByKey(f.t.Context(), "local")
	if err == nil {
		return row.ID
	}
	return f.provider("local", schemaenum.IdentityProviderKindLocal, true)
}
