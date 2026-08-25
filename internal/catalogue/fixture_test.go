package catalogue_test

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
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
	// inv is the port every window-moving write takes. It is a spy rather than the real
	// projection because these are the CATALOGUE's tests: what they need to know is that the
	// push happened, that it happened inside the transaction, and that its failure took the
	// write down with it.
	inv *spyInvalidator
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
	return &fixture{t: t, svc: svc, store: db, clock: clk, ids: ids, inv: &spyInvalidator{}}
}

// invalidation is one push at the projection: which circle or which server, and which target.
type invalidation struct {
	Circle core.CircleID
	Server core.Server
	Target core.RaidTargetID
	Scope  string
}

// spyInvalidator is the [catalogue.TimerInvalidator] this package's tests wire.
//
// It records rather than no-ops, because "did this write push the invalidation" is a question a
// no-op fake would let every write pass. `err` makes the push fail, which is the only way to
// assert that a failed push takes the write down with it. `inside` runs with the WRITING
// TRANSACTION's own query set, which is how a test asks what is visible from inside it and what
// is not.
type spyInvalidator struct {
	mu     sync.Mutex
	calls  []invalidation
	err    error
	inside func(ctx context.Context, q *sqlitegen.Queries) error
}

func (s *spyInvalidator) OnTimerChange(
	ctx context.Context, q *sqlitegen.Queries,
	circleID core.CircleID, targetID core.RaidTargetID,
) error {
	return s.record(ctx, q, invalidation{Circle: circleID, Target: targetID, Scope: "circle"})
}

func (s *spyInvalidator) OnCatalogueTimerChange(
	ctx context.Context, q *sqlitegen.Queries, server core.Server, targetID core.RaidTargetID,
) error {
	return s.record(ctx, q, invalidation{Server: server, Target: targetID, Scope: "instance"})
}

func (s *spyInvalidator) record(
	ctx context.Context, q *sqlitegen.Queries, call invalidation,
) error {
	s.mu.Lock()
	s.calls = append(s.calls, call)
	inside, err := s.inside, s.err
	s.mu.Unlock()
	if inside != nil {
		if insideErr := inside(ctx, q); insideErr != nil {
			return insideErr
		}
	}
	return err
}

func (s *spyInvalidator) recorded() []invalidation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]invalidation(nil), s.calls...)
}

func (s *spyInvalidator) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls, s.err, s.inside = nil, nil, nil
}

func (s *spyInvalidator) failWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
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
