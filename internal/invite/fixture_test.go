package invite_test

import (
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// fixtureNow is the fixed instant every time-dependent assertion here is relative to, so a test
// that fails does not depend on when it ran.
const fixtureNow = core.Micros(1_755_483_247_000_000)

// fixture is a real migrated SQLite database in t.TempDir() with one circle and one officer.
//
// No mock of the database anywhere: the rules under test — `uses <= max_uses`, the unique index on
// `code_hash`, the compare-and-swap that makes an owner grant single-use — are rules about rows,
// and a mock would let every one of them pass while the schema said otherwise.
type fixture struct {
	t       *testing.T
	store   *store.DB
	clock   *clock.Test
	ids     *core.Generator
	service *invite.Service
	circle  core.CircleID
	officer core.MembershipID
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
	service, err := invite.New(invite.Config{
		Store: db, Clock: clk, IDs: ids, Entropy: rand.Reader, Log: log,
	})
	require.NoError(t, err)

	f := &fixture{t: t, store: db, clock: clk, ids: ids, service: service}
	f.circle = f.seedCircle("Riot Blue", schemaenum.ServerBlue)
	f.officer = f.seedMember(f.circle, "officer")
	return f
}

func (f *fixture) newID(entity string) string {
	f.t.Helper()
	switch entity {
	case "circle":
		id, err := core.NewID[core.Circle](f.ids, f.clock.Now())
		require.NoError(f.t, err)
		return id.String()
	default:
		id, err := core.NewID[core.Membership](f.ids, f.clock.Now())
		require.NoError(f.t, err)
		return id.String()
	}
}

func (f *fixture) seedCircle(name, server string) core.CircleID {
	f.t.Helper()
	raw := f.newID("circle")
	_, err := f.store.Queries().CreateCircle(f.t.Context(), sqlitegen.CreateCircleParams{
		CircleID: raw, Name: name, NameNorm: core.Normalise(name),
		Server: server, Timezone: "UTC", MinReportersToSupersede: 1,
		RevokeInvalidatesInvites: 1, State: schemaenum.CircleStateActive,
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	id, err := core.ParseID[core.Circle](raw)
	require.NoError(f.t, err)
	return id
}

// seedProvider writes the instance's `local` provider. `verifiable_subject` is 0 because it is a
// CHECK against `kind`, not a value this fixture chooses.
func (f *fixture) seedProvider() string {
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
	return id.String()
}

func (f *fixture) seedMember(circleID core.CircleID, role string) core.MembershipID {
	f.t.Helper()
	provider := f.seedProviderOnce()
	identityID, err := core.NewID[core.Identity](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentity(f.t.Context(), sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: provider, Subject: identityID.String(),
		DisplayName: role, CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)

	raw := f.newID("membership")
	subject := identityID.String()
	_, err = f.store.Queries().CreateMembership(f.t.Context(), sqlitegen.CreateMembershipParams{
		ID: raw, CircleID: circleID.String(), IdentityID: &subject,
		Kind: schemaenum.MembershipKindHuman, DisplayName: role,
		DisplayNameNorm: core.Normalise(role), Role: role, JoinedAt: int64(fixtureNow),
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	id, err := core.ParseID[core.Membership](raw)
	require.NoError(f.t, err)
	return id
}

// seedProviderOnce writes the `local` provider at most once per fixture: `identity_provider.key`
// is unique and there is at most one `local` row.
func (f *fixture) seedProviderOnce() string {
	f.t.Helper()
	row, err := f.store.Queries().GetIdentityProviderByKey(f.t.Context(), "local")
	if err == nil {
		return row.ID
	}
	require.True(f.t, store.IsNotFound(err))
	return f.seedProvider()
}

// acceptProvider makes the circle accept the instance's `local` provider, which is what forces
// `max_uses = 1` on every invite into it.
func (f *fixture) acceptProvider(circleID core.CircleID) {
	f.t.Helper()
	_, err := f.store.Queries().PutCircleProvider(f.t.Context(), sqlitegen.PutCircleProviderParams{
		CircleID: circleID.String(), ProviderID: f.seedProviderOnce(),
		DiscordRequiredRoleIdsJson: "[]",
		CreatedAt:                  int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
}
