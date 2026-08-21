package tod_test

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
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// fixtureNow is the fixture's fixed instant, so a time-dependent assertion does not depend on when
// it ran. 2026-08-18T02:14:07Z — the timestamp in the design's own example log line.
const fixtureNow = core.Micros(1_755_483_247_000_000)

// fixture is a real service over a real migrated SQLite database in t.TempDir().
//
// There is no mock of the database anywhere here, deliberately: the rules under test are rules
// about rows — an append-only trigger, a partial unique index on the natural key, a CHECK on
// `died_at` — and a mock would let every one of them pass while the schema said otherwise.
type fixture struct {
	t         *testing.T
	db        *store.DB
	clock     *clock.Test
	ids       *core.Generator
	catalogue *catalogue.Service
	tods      *tod.Service
	circle    core.CircleID
	reporter  core.MembershipID
	provider  core.IdentityProviderID
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
	cat, err := catalogue.New(catalogue.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	tods, err := tod.New(tod.Config{
		Store: db, Clock: clk, IDs: ids, Catalogue: cat, Log: log,
	})
	require.NoError(t, err)

	f := &fixture{t: t, db: db, clock: clk, ids: ids, catalogue: cat, tods: tods}
	f.circle = f.seedCircle("Riot", schemaenum.ServerBlue)
	f.reporter = f.seedMember(f.circle, "Tankguy")
	return f
}

func newID[E core.Entity](f *fixture) core.ID[E] {
	f.t.Helper()
	id, err := core.NewID[E](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	return id
}

// seedCircle writes a circle pinned to one server, immutably.
func (f *fixture) seedCircle(name, server string) core.CircleID {
	f.t.Helper()
	id := newID[core.Circle](f)
	_, err := f.db.Queries().CreateCircle(f.t.Context(), sqlitegen.CreateCircleParams{
		CircleID: id.String(), Name: name, NameNorm: core.Normalise(name),
		Description: "", Server: server, Timezone: "UTC",
		MinReportersToSupersede: 1, RevokeInvalidatesInvites: 1,
		State:     schemaenum.CircleStateActive,
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return id
}

// seedProvider writes the instance's `local` identity provider, once. `identity_provider.kind` is
// unique, so an instance holds at most one row of each kind.
func (f *fixture) seedProvider() core.IdentityProviderID {
	f.t.Helper()
	if !f.provider.IsZero() {
		return f.provider
	}
	id := newID[core.IdentityProvider](f)
	_, err := f.db.Queries().CreateIdentityProvider(f.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: id.String(), Key: "local", Kind: schemaenum.IdentityProviderKindLocal,
			DisplayName: "Local", Enabled: 1,
			// `verifiable_subject` is a CHECK against `kind`, never a toggle: local is 0.
			VerifiableSubject: 0,
			CreatedAt:         int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
	require.NoError(f.t, err)
	f.provider = id
	return id
}

// seedMember writes an identity and the human membership over it. A human membership carries a
// CHECK that it has one, so the two travel together.
func (f *fixture) seedMember(circleID core.CircleID, name string) core.MembershipID {
	f.t.Helper()
	provider := f.seedProvider()
	identity := newID[core.Identity](f)
	_, err := f.db.Queries().CreateIdentity(f.t.Context(), sqlitegen.CreateIdentityParams{
		ID: identity.String(), ProviderID: provider.String(), Subject: identity.String(),
		DisplayName: name, CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)

	id := newID[core.Membership](f)
	identityID := identity.String()
	_, err = f.db.Queries().CreateMembership(f.t.Context(), sqlitegen.CreateMembershipParams{
		ID: id.String(), CircleID: circleID.String(), IdentityID: &identityID,
		Kind:        schemaenum.MembershipKindHuman,
		DisplayName: name, DisplayNameNorm: core.Normalise(name),
		Role:      schemaenum.MembershipRoleMember,
		JoinedAt:  int64(fixtureNow),
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return id
}

// revoke revokes a membership. Their reports still count and their retractions still apply.
func (f *fixture) revoke(circleID core.CircleID, memberID core.MembershipID) {
	f.t.Helper()
	at := int64(f.clock.Now())
	by := memberID.String()
	_, err := f.db.Queries().RevokeMembership(f.t.Context(), sqlitegen.RevokeMembershipParams{
		RevokedAt: &at, RevokedByMembershipID: &by, UpdatedAt: at,
		CircleID: circleID.String(), ID: memberID.String(),
	})
	require.NoError(f.t, err)
}

// seedTarget adds a raid target through the catalogue, which is the only thing that writes one.
func (f *fixture) seedTarget(name, zone string, aliases ...string) catalogue.Target {
	f.t.Helper()
	target, err := f.catalogue.Create(f.t.Context(), catalogue.CreateRequest{
		Name: name, Zone: zone,
		Expansion: schemaenum.RaidTargetExpansionVelious,
		Category:  schemaenum.RaidTargetCategoryNToV,
		Aliases:   aliases,
	})
	require.NoError(f.t, err)
	return target
}

// report is the common-case ingest: one log line, from the fixture's reporter, about one target.
func (f *fixture) report(
	target catalogue.Target, diedAt core.Micros, source string,
) tod.Created {
	f.t.Helper()
	created, err := f.tods.Create(f.t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: f.reporter, TargetID: target.ID.String(),
		Server: schemaenum.ServerBlue, DiedAt: diedAt, Source: source,
	})
	require.NoError(f.t, err)
	return created
}
