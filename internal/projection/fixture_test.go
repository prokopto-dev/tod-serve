package projection_test

import (
	"bytes"
	"crypto/rand"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// fixtureNow is the fixture's fixed instant, so a time-dependent assertion does not depend on when
// it ran.
const fixtureNow = core.Micros(1_755_483_247_000_000)

// fixture is the whole ToD subsystem over a real migrated SQLite database in t.TempDir(): the
// catalogue that resolves timers, the ingest that appends, and the projection over both.
//
// **The default state is an instance with no timers seeded at all**, because that is the operator's
// VPS on day one — canonical §15 says timer data does not ship. A test that wants a window says so.
type fixture struct {
	t         *testing.T
	db        *store.DB
	clock     *clock.Test
	ids       *core.Generator
	log       *recordingLog
	catalogue *catalogue.Service
	tods      *tod.Service
	states    *projection.Service
	circle    core.CircleID
	reporter  core.MembershipID
	provider  core.IdentityProviderID
}

// recordingLog captures what the service logged, so a test can assert that the verify job ALERTED
// rather than only that it repaired. A repair that happened quietly is a repair nobody
// investigates, which is the half of the requirement a return value cannot check.
type recordingLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (r *recordingLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buffer.Write(p)
}

// errorLines returns the ERROR records written so far.
func (r *recordingLog) errorLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, line := range strings.Split(r.buffer.String(), "\n") {
		if strings.Contains(line, "level=ERROR") {
			out = append(out, line)
		}
	}
	return out
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	recorder := &recordingLog{}
	log := slog.New(slog.NewTextHandler(recorder, &slog.HandlerOptions{Level: slog.LevelInfo}))

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
	states, err := projection.New(projection.Config{
		Store: db, Clock: clk, Catalogue: cat, Log: log,
	})
	require.NoError(t, err)

	f := &fixture{
		t: t, db: db, clock: clk, ids: ids, log: recorder,
		catalogue: cat, tods: tods, states: states,
	}
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

func (f *fixture) seedProvider() core.IdentityProviderID {
	f.t.Helper()
	if !f.provider.IsZero() {
		return f.provider
	}
	id := newID[core.IdentityProvider](f)
	_, err := f.db.Queries().CreateIdentityProvider(f.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: id.String(), Key: "local", Kind: schemaenum.IdentityProviderKindLocal,
			DisplayName: "Local", Enabled: 1, VerifiableSubject: 0,
			CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
	require.NoError(f.t, err)
	f.provider = id
	return id
}

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

// seedTarget adds a raid target. It seeds NO timer: an unseeded instance is the default here.
func (f *fixture) seedTarget(name, zone string, quakeTarget bool) catalogue.Target {
	f.t.Helper()
	target, err := f.catalogue.Create(f.t.Context(), catalogue.CreateRequest{
		Name: name, Zone: zone,
		Expansion:     schemaenum.RaidTargetExpansionVelious,
		Category:      schemaenum.RaidTargetCategoryNToV,
		IsQuakeTarget: quakeTarget,
	})
	require.NoError(f.t, err)
	return target
}

// seedCatalogueTimer gives a target a variance window on the circle's server — what a seeded
// instance looks like.
func (f *fixture) seedCatalogueTimer(target catalogue.Target, open, close time.Duration) {
	f.t.Helper()
	openSeconds, closeSeconds := int64(open.Seconds()), int64(close.Seconds())
	_, err := f.catalogue.PutTimer(f.t.Context(), target.ID, core.Server(schemaenum.ServerBlue),
		catalogue.WindowRequest{
			WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
			WindowOpenOffsetSeconds:  &openSeconds,
			WindowCloseOffsetSeconds: &closeSeconds,
			Source:                   "test",
		})
	require.NoError(f.t, err)
}

// seedOverride is the circle disagreeing with the catalogue, which is the whole reason
// `circle_timer_override` exists.
func (f *fixture) seedOverride(target catalogue.Target, open, close time.Duration) {
	f.t.Helper()
	openSeconds, closeSeconds := int64(open.Seconds()), int64(close.Seconds())
	_, err := f.catalogue.PutOverride(f.t.Context(), f.circle, target.ID, f.reporter,
		catalogue.WindowRequest{
			WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
			WindowOpenOffsetSeconds:  &openSeconds,
			WindowCloseOffsetSeconds: &closeSeconds,
			Note:                     "we have tracked this for two years",
		})
	require.NoError(f.t, err)
}

// report appends one kill from the fixture's reporter.
func (f *fixture) report(target catalogue.Target, diedAt core.Micros, source string) tod.Created {
	f.t.Helper()
	return f.reportAs(f.reporter, target, diedAt, source)
}

func (f *fixture) reportAs(
	reporter core.MembershipID, target catalogue.Target, diedAt core.Micros, source string,
) tod.Created {
	f.t.Helper()
	created, err := f.tods.Create(f.t.Context(), tod.CreateRequest{
		CircleID: f.circle, Reporter: reporter, TargetID: target.ID.String(),
		Server: schemaenum.ServerBlue, DiedAt: diedAt, Source: source,
	})
	require.NoError(f.t, err)
	return created
}

// cached reads the cache row directly, which is the only place in these tests it is read as
// anything other than an implementation detail.
func (f *fixture) cached(target catalogue.Target) (sqlitegen.TargetStateCache, bool) {
	f.t.Helper()
	row, err := f.db.Queries().GetTargetState(f.t.Context(), sqlitegen.GetTargetStateParams{
		CircleID: f.circle.String(), TargetID: target.ID.String(),
	})
	if store.IsNotFound(err) {
		return sqlitegen.TargetStateCache{}, false
	}
	require.NoError(f.t, err)
	return row, true
}

// entryFor finds one target on the board.
func (f *fixture) entryFor(
	entries []projection.BoardEntry, target catalogue.Target,
) projection.BoardEntry {
	f.t.Helper()
	for _, e := range entries {
		if e.Target.ID == target.ID {
			return e
		}
	}
	require.FailNow(f.t, "target is not on the board", target.Name)
	return projection.BoardEntry{}
}

// board renders the whole board with no filter.
func (f *fixture) board() []projection.BoardEntry {
	f.t.Helper()
	entries, hasMore, err := f.states.Board(f.t.Context(), f.circle,
		projection.BoardFilter{Limit: 200})
	require.NoError(f.t, err)
	require.False(f.t, hasMore)
	return entries
}

// todQuake is the quake request the board tests use, spelled once so the import of the ingest
// package stays where the fixture is.
func todQuake(f *fixture, at core.Micros) tod.ReportQuakeRequest {
	return tod.ReportQuakeRequest{CircleID: f.circle, Reporter: f.reporter, OccurredAt: at}
}

// revoke revokes a membership. Their reports still count and their retractions still apply; this
// only makes the fact visible.
func (f *fixture) revoke(memberID core.MembershipID) {
	f.t.Helper()
	at := int64(f.clock.Now())
	by := memberID.String()
	_, err := f.db.Queries().RevokeMembership(f.t.Context(), sqlitegen.RevokeMembershipParams{
		RevokedAt: &at, RevokedByMembershipID: &by, UpdatedAt: at,
		CircleID: f.circle.String(), ID: memberID.String(),
	})
	require.NoError(f.t, err)
}

// seedCatalogueTimerOn is [fixture.seedCatalogueTimer] for a server other than blue, for the tests
// about a catalogue timer's per-server fan-out.
func (f *fixture) seedCatalogueTimerOn(
	target catalogue.Target, server string, open, closeAt time.Duration,
) {
	f.t.Helper()
	openSeconds, closeSeconds := int64(open.Seconds()), int64(closeAt.Seconds())
	_, err := f.catalogue.PutTimer(f.t.Context(), target.ID, core.Server(server),
		catalogue.WindowRequest{
			WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
			WindowOpenOffsetSeconds:  &openSeconds,
			WindowCloseOffsetSeconds: &closeSeconds,
			Source:                   "test",
		})
	require.NoError(f.t, err)
}

// seedOverrideIn is [fixture.seedOverride] for a circle other than the fixture's own.
func (f *fixture) seedOverrideIn(
	circleID core.CircleID, target catalogue.Target, open, closeAt time.Duration,
) {
	f.t.Helper()
	openSeconds, closeSeconds := int64(open.Seconds()), int64(closeAt.Seconds())
	_, err := f.catalogue.PutOverride(f.t.Context(), circleID, target.ID,
		f.seedMember(circleID, "Officer"), catalogue.WindowRequest{
			WindowKind:               schemaenum.RaidTargetTimerWindowKindVariance,
			WindowOpenOffsetSeconds:  &openSeconds,
			WindowCloseOffsetSeconds: &closeSeconds,
			Note:                     "we have tracked this for two years",
		})
	require.NoError(f.t, err)
}

// reportIn appends a kill in a circle other than the fixture's own.
func (f *fixture) reportIn(
	circleID core.CircleID, target catalogue.Target, diedAt core.Micros, source string,
) tod.Created {
	f.t.Helper()
	circle, err := f.db.Queries().GetCircle(f.t.Context(), circleID.String())
	require.NoError(f.t, err)
	created, err := f.tods.Create(f.t.Context(), tod.CreateRequest{
		CircleID: circleID, Reporter: f.seedMember(circleID, "Reporter"),
		TargetID: target.ID.String(), Server: circle.Server, DiedAt: diedAt, Source: source,
	})
	require.NoError(f.t, err)
	return created
}

// cachedIn reads the cache row for a circle other than the fixture's own.
func (f *fixture) cachedIn(
	circleID core.CircleID, target catalogue.Target,
) (sqlitegen.TargetStateCache, bool) {
	f.t.Helper()
	row, err := f.db.Queries().GetTargetState(f.t.Context(), sqlitegen.GetTargetStateParams{
		CircleID: circleID.String(), TargetID: target.ID.String(),
	})
	if store.IsNotFound(err) {
		return sqlitegen.TargetStateCache{}, false
	}
	require.NoError(f.t, err)
	return row, true
}
