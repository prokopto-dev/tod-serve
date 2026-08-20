package catalogue_test

import (
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// The fixture's fixed instant, so a failing assertion does not depend on when it ran.
const fixtureNow = core.Micros(1_755_483_247_000_000)

// fixture is the catalogue service over a real migrated SQLite database in t.TempDir().
//
// There is no mock of the store here, deliberately: the rules under test are rules about rows —
// `name_norm` unique across the catalogue, the four window CHECK constraints, a seed that must
// leave nothing behind when it fails — and a mock would let every one of them pass while the
// schema said otherwise.
//
// It starts EMPTY, which is the state that matters most: no targets, no timers. Every test that
// wants a catalogue asks for one.
type fixture struct {
	t     *testing.T
	svc   *catalogue.Service
	store *store.DB
	clock *clock.Test
	ids   *core.Generator
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tod.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(t.Context()))

	clk := clock.NewTest(fixtureNow)
	ids := core.NewGenerator(rand.Reader)
	svc, err := catalogue.New(catalogue.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	return &fixture{t: t, svc: svc, store: db, clock: clk, ids: ids}
}

// seedEmbedded loads the shipped identity — and nothing else. No timer is written by this, ever:
// that is the licence boundary, and a fixture that quietly added one would make every `no_timer`
// assertion below pass for the wrong reason.
func (f *fixture) seedEmbedded() catalogue.TargetSeedReport {
	f.t.Helper()
	report, err := f.svc.SeedTargets(f.t.Context())
	require.NoError(f.t, err)
	return report
}

// target adds one target with the given aliases and returns it.
func (f *fixture) target(name, zone string, aliases ...string) catalogue.Target {
	f.t.Helper()
	view, err := f.svc.Create(f.t.Context(), catalogue.CreateRequest{
		Name: name, Zone: zone,
		Expansion: schemaenum.RaidTargetExpansionVelious,
		Category:  schemaenum.RaidTargetCategoryNToV,
		Aliases:   aliases,
	})
	require.NoError(f.t, err)
	return view
}

// circle writes a circle directly, because internal/catalogue has no business constructing one
// through the circle service just to have a tenancy key to hang an override on.
func (f *fixture) circle(name string) core.CircleID {
	f.t.Helper()
	id, err := core.NewID[core.Circle](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateCircle(f.t.Context(), sqlitegen.CreateCircleParams{
		CircleID: id.String(), Name: name, NameNorm: core.Normalise(name),
		Server: schemaenum.ServerBlue, Timezone: "UTC",
		MinReportersToSupersede: 1, RevokeInvalidatesInvites: 1,
		State:     schemaenum.CircleStateActive,
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return id
}

// member writes a membership, which an override's `created_by_membership_id` references.
//
// It writes the provider and identity behind it too, because `ck_membership_human_has_an_identity`
// is a CHECK and not a convention: a human membership with no identity is not a row this schema
// will hold, and a fixture that could write one would be testing a database nobody runs.
func (f *fixture) member(circleID core.CircleID) core.MembershipID {
	f.t.Helper()
	providerID, err := core.NewID[core.IdentityProvider](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentityProvider(f.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: providerID.String(), Key: "local", Kind: schemaenum.IdentityProviderKindLocal,
			DisplayName: "Local", Enabled: 1,
			// verifiable_subject is a CHECK against kind, never a toggle: local is 0.
			VerifiableSubject: 0,
			CreatedAt:         int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
	require.NoError(f.t, err)

	identityID, err := core.NewID[core.Identity](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentity(f.t.Context(), sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: providerID.String(),
		Subject: identityID.String(), DisplayName: "Officer",
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)

	id, err := core.NewID[core.Membership](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	subject := identityID.String()
	_, err = f.store.Queries().CreateMembership(f.t.Context(), sqlitegen.CreateMembershipParams{
		ID: id.String(), CircleID: circleID.String(), IdentityID: &subject,
		Kind: schemaenum.MembershipKindHuman, DisplayName: "Officer",
		DisplayNameNorm: "officer", Role: schemaenum.MembershipRoleOfficer,
		JoinedAt: int64(fixtureNow), CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return id
}

// ptr is the one-liner every optional window offset needs.
func ptr[T any](v T) *T { return &v }
