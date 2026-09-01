package discord_test

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// fixture is a real migrated SQLite database in t.TempDir(), with the real domain services behind
// the dispatcher.
//
// No mock of the database and no stub of the board: what this package promises — that a channel
// resolves to at most one circle, that a stranger is answered as a stranger, that a repeated report
// appends one row — are all promises about rows, and a stub would let every one of them pass while
// the schema said otherwise.
type fixture struct {
	t         *testing.T
	store     *store.DB
	clock     *clock.Test
	ids       *core.Generator
	bindings  *discord.Service
	commander *discord.Commander
	providers *stubProviders
	circles   *circle.Service
	catalogue *catalogue.Service
	tods      *tod.Service
	discordID core.IdentityProviderID
}

// stubProviders stands in for `internal/identity` and nothing else.
//
// It is the ONE stub here, and it is a stub because the alternative is wiring an OAuth application
// — a guarded HTTP client, a redirect URI and a client secret — to answer a question that is one
// row: which provider row is the `discord` one. Everything downstream of that answer is real.
type stubProviders struct {
	providers []identity.Provider
	err       error
}

func (s *stubProviders) EnabledProviders(context.Context) ([]identity.Provider, error) {
	return s.providers, s.err
}

const fixtureNow = core.Micros(1_755_483_247_000_000)

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

	circles, err := circle.New(circle.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	catalogues, err := catalogue.New(catalogue.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	tods, err := tod.New(tod.Config{
		Store: db, Clock: clk, IDs: ids, Catalogue: catalogues, Log: log,
	})
	require.NoError(t, err)
	states, err := projection.New(projection.Config{
		Store: db, Clock: clk, Catalogue: catalogues, Log: log,
	})
	require.NoError(t, err)

	bindings, err := discord.New(discord.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)

	f := &fixture{
		t: t, store: db, clock: clk, ids: ids, bindings: bindings,
		providers: &stubProviders{}, circles: circles, catalogue: catalogues, tods: tods,
	}
	f.discordID = f.seedDiscordProvider()

	f.commander, err = discord.NewCommander(discord.CommanderConfig{
		Bindings: bindings, Providers: f.providers, Circles: circles, Boards: states,
		Targets: catalogues, Reports: tods, Clock: clk, Log: log,
	})
	require.NoError(t, err)
	return f
}

// seedDiscordProvider writes the one `discord` provider row an instance may have, and tells the
// stub about it.
func (f *fixture) seedDiscordProvider() core.IdentityProviderID {
	f.t.Helper()
	id, err := core.NewID[core.IdentityProvider](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentityProvider(f.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: id.String(), Key: "discord", Kind: schemaenum.IdentityProviderKindDiscord,
			DisplayName: "Discord", Enabled: 1, VerifiableSubject: 1,
			ClientID:  ptr("1234567890"),
			CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
	require.NoError(f.t, err)
	f.providers.providers = []identity.Provider{{
		ID: id.String(), Key: "discord", Kind: identity.KindDiscord,
		DisplayName: "Discord", Enabled: true, VerifiableSubject: true, ClientID: "1234567890",
	}}
	return id
}

func (f *fixture) seedCircle(name, server string) core.CircleID {
	f.t.Helper()
	id, err := core.NewID[core.Circle](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateCircle(f.t.Context(), sqlitegen.CreateCircleParams{
		CircleID: id.String(), Name: name, NameNorm: core.Normalise(name), Server: server,
		Timezone: "UTC", MinReportersToSupersede: 2, State: "active",
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return id
}

// seedMember writes an identity with the given Discord snowflake and a membership for it.
func (f *fixture) seedMember(
	circleID core.CircleID, subject, name, role string,
) core.MembershipID {
	f.t.Helper()
	identityID, err := core.NewID[core.Identity](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentity(f.t.Context(), sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: f.discordID.String(), Subject: subject,
		DisplayName: name, CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)

	membershipID, err := core.NewID[core.Membership](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	subjectID := identityID.String()
	_, err = f.store.Queries().CreateMembership(f.t.Context(), sqlitegen.CreateMembershipParams{
		ID: membershipID.String(), CircleID: circleID.String(), IdentityID: &subjectID,
		Kind: schemaenum.MembershipKindHuman, DisplayName: name,
		DisplayNameNorm: core.Normalise(name), Role: role,
		JoinedAt:  int64(fixtureNow),
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return membershipID
}

// bind writes a channel binding through the service, so the audit row and the refusals are the
// ones a real bind produces.
func (f *fixture) bind(
	circleID core.CircleID, channelID, guildID string, allowVisible bool, by core.MembershipID,
) discord.Binding {
	f.t.Helper()
	b, err := f.bindings.Bind(f.t.Context(), discord.BindRequest{
		CircleID: circleID, ChannelID: channelID, GuildID: guildID,
		AllowVisible: allowVisible, By: by,
	})
	require.NoError(f.t, err)
	return b
}

// seedTarget adds a raid target so `/tod status` and `/tod report` have something to resolve.
func (f *fixture) seedTarget(name string) core.RaidTargetID {
	f.t.Helper()
	target, err := f.catalogue.Create(f.t.Context(), catalogue.CreateRequest{
		Name: name, Expansion: "classic", Zone: "Plane of Fear", Category: "planar",
	})
	require.NoError(f.t, err)
	return target.ID
}

func ptr[T any](v T) *T { return &v }

// revoke revokes a membership through the query the real revocation path uses, so the dispatcher
// sees the same row an officer's `revokeMember` would have written.
func (f *fixture) revoke(circleID core.CircleID, member, by core.MembershipID) {
	f.t.Helper()
	_, err := f.store.Queries().RevokeMembership(f.t.Context(), sqlitegen.RevokeMembershipParams{
		CircleID: circleID.String(), ID: member.String(),
		RevokedAt: ptr(int64(f.clock.Now())), RevokedByMembershipID: ptr(by.String()),
		UpdatedAt: int64(f.clock.Now()),
	})
	require.NoError(f.t, err)
}

// tombstone soft-deletes a circle. Its bindings stay behind, exactly as `circle_provider`'s rows
// do, which is the state the resolve has to see through.
func (f *fixture) tombstone(circleID core.CircleID) {
	f.t.Helper()
	_, err := f.store.Queries().SoftDeleteCircle(f.t.Context(), sqlitegen.SoftDeleteCircleParams{
		CircleID: circleID.String(), DeletedAt: ptr(int64(f.clock.Now())),
		UpdatedAt: int64(f.clock.Now()),
	})
	require.NoError(f.t, err)
}

// auditActions returns the actions recorded against a circle, so a test can assert that a
// disclosure decision left a record without reaching through the service that wrote it.
func (f *fixture) auditActions(circleID core.CircleID) []string {
	f.t.Helper()
	rows, err := f.store.Queries().ListAuditLog(f.t.Context(), sqlitegen.ListAuditLogParams{
		CircleID: circleID.String(), RowLimit: 100,
	})
	require.NoError(f.t, err)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action)
	}
	return out
}

// seedMemberFor adds a membership for an identity that already exists, which is what a person in
// two circles is. `identity` is instance-wide and keyed on `(provider, subject)`, so a second
// membership reuses the row rather than creating a second person — and `membership` carries no
// per-server uniqueness, so the two circles may be on ONE server.
func (f *fixture) seedMemberFor(
	circleID core.CircleID, subject, name, role string,
) core.MembershipID {
	f.t.Helper()
	person, err := f.store.Queries().GetIdentityByProviderSubject(f.t.Context(),
		sqlitegen.GetIdentityByProviderSubjectParams{
			ProviderID: f.discordID.String(), Subject: subject,
		})
	require.NoError(f.t, err)

	membershipID, err := core.NewID[core.Membership](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	identityID := person.ID
	_, err = f.store.Queries().CreateMembership(f.t.Context(), sqlitegen.CreateMembershipParams{
		ID: membershipID.String(), CircleID: circleID.String(), IdentityID: &identityID,
		Kind: schemaenum.MembershipKindHuman, DisplayName: name,
		DisplayNameNorm: core.Normalise(name), Role: role,
		JoinedAt:  int64(fixtureNow),
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	return membershipID
}
