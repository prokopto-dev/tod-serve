package sweep_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/sweep"
)

const fixtureNow = core.Micros(1_755_483_247_000_000)

// ticketTTL mirrors the CHECK on `credential_ticket`: `expires_at = created_at + 120 * 1000000`.
// A ticket row with any other lifetime cannot be written at all, so the fixture derives
// `created_at` from the expiry it is asked for rather than the other way round.
const ticketTTL = 120 * time.Second

// TestSweep_ARowPastTheGrace_IsRemovedAndOneInsideItIsNot is the whole contract, per table: what
// is old enough goes, what is not stays, and the count says how many went.
//
// Each case seeds three rows -- one long expired, one expired only moments ago, and one not
// expired at all -- so a sweep that took everything and a sweep that took nothing both fail. The
// survivors are checked by identity rather than by a count: a run that deleted the live row and
// left the stale one would balance.
func TestSweep_ARowPastTheGrace_IsRemovedAndOneInsideItIsNot(t *testing.T) {
	t.Parallel()

	stale := fixtureNow.Add(-sweep.Grace - time.Hour) // expired well past the grace
	recent := fixtureNow.Add(-time.Minute)            // expired, but inside the grace
	live := fixtureNow.Add(time.Hour)                 // not expired at all

	tests := []struct {
		name  string
		seed  func(f *fixture, key string, expiresAt core.Micros)
		alive func(f *fixture, key string) bool
		count func(sweep.Report) int64
	}{
		{
			name:  "auth_flow",
			seed:  (*fixture).seedAuthFlow,
			alive: (*fixture).authFlowExists,
			count: func(r sweep.Report) int64 { return r.AuthFlows },
		},
		{
			name:  "credential_ticket",
			seed:  (*fixture).seedCredentialTicket,
			alive: (*fixture).credentialTicketExists,
			count: func(r sweep.Report) int64 { return r.CredentialTickets },
		},
		{
			name:  "idempotency_record",
			seed:  (*fixture).seedIdempotencyRecord,
			alive: (*fixture).idempotencyRecordExists,
			count: func(r sweep.Report) int64 { return r.IdempotencyRecords },
		},
		{
			name:  "session_revocation",
			seed:  (*fixture).seedSessionRevocation,
			alive: (*fixture).sessionRevocationExists,
			count: func(r sweep.Report) int64 { return r.SessionRevocations },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			tc.seed(f, "stale", stale)
			tc.seed(f, "recent", recent)
			tc.seed(f, "live", live)

			report, err := f.svc.Sweep(t.Context())
			require.NoError(t, err)

			require.Equal(t, int64(1), tc.count(report),
				"exactly the one row past the grace period should have been counted")
			require.Equal(t, int64(1), report.Total(),
				"no other table should have lost a row")

			require.False(t, tc.alive(f, "stale"), "the row past the grace period should be gone")
			require.True(t, tc.alive(f, "recent"),
				"a row expired only a minute ago is still inside the grace period")
			require.True(t, tc.alive(f, "live"), "an unexpired row must never be swept")
		})
	}
}

// TestSweep_EveryTableAtOnce_IsCountedSeparately. The counts are the product requirement -- a
// sweep that removed rows without saying which table they came from is the thing the house rule
// against hiding a row silently forbids -- so a single total is not enough. Different numbers per
// table catch a report that filled every field from the same result.
func TestSweep_EveryTableAtOnce_IsCountedSeparately(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	stale := fixtureNow.Add(-sweep.Grace - time.Hour)

	f.seedAuthFlow("a1", stale)
	f.seedCredentialTicket("c1", stale)
	f.seedCredentialTicket("c2", stale)
	f.seedIdempotencyRecord("i1", stale)
	f.seedIdempotencyRecord("i2", stale)
	f.seedIdempotencyRecord("i3", stale)
	f.seedSessionRevocation("s1", stale)
	f.seedSessionRevocation("s2", stale)
	f.seedSessionRevocation("s3", stale)
	f.seedSessionRevocation("s4", stale)

	report, err := f.svc.Sweep(t.Context())
	require.NoError(t, err)

	require.Equal(t, int64(1), report.AuthFlows)
	require.Equal(t, int64(2), report.CredentialTickets)
	require.Equal(t, int64(3), report.IdempotencyRecords)
	require.Equal(t, int64(4), report.SessionRevocations)
	require.Equal(t, int64(10), report.Total())
}

// TestSweep_AnInstanceWithNothingExpired_RemovesNothingAndSaysSo. The quiet case still reports and
// still logs: silence that means "nothing to do" and silence that means "this has not run in three
// weeks" have to be distinguishable, and only the second is a problem.
func TestSweep_AnInstanceWithNothingExpired_RemovesNothingAndSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedAuthFlow("live", fixtureNow.Add(time.Hour))

	report, err := f.svc.Sweep(t.Context())
	require.NoError(t, err)

	require.Zero(t, report.Total())
	require.True(t, f.authFlowExists("live"))
	require.Contains(t, f.logs.String(), sweep.SweptMessage,
		"a run that removed nothing still logs, or its silence is ambiguous")
}

// TestSweep_TheCutoff_IsOneGraceBeforeTheInjectedNow pins the arithmetic to the injected clock.
// `time.Now` is banned outside internal/clock, and a sweep that read the wall clock would delete a
// different set of rows than the one the caller's clock describes.
func TestSweep_TheCutoff_IsOneGraceBeforeTheInjectedNow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	report, err := f.svc.Sweep(t.Context())
	require.NoError(t, err)

	require.Equal(t, fixtureNow, report.AsOf)
	require.Equal(t, fixtureNow.Add(-sweep.Grace), report.Before)
}

// TestSweep_TheLog_NamesEveryCountStructurally. The log line is the operator's only view of a job
// that runs unattended, so it is asserted field by field rather than as a substring: an attribute
// that quietly stopped being emitted is exactly the row being hidden.
func TestSweep_TheLog_NamesEveryCountStructurally(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	stale := fixtureNow.Add(-sweep.Grace - time.Hour)
	f.seedAuthFlow("a1", stale)
	f.seedCredentialTicket("c1", stale)

	_, err := f.svc.Sweep(t.Context())
	require.NoError(t, err)

	var line map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(f.logs.Bytes()), &line))
	require.Equal(t, sweep.SweptMessage, line["msg"])
	require.Equal(t, float64(1), line["auth_flows"])
	require.Equal(t, float64(1), line["credential_tickets"])
	require.Equal(t, float64(0), line["idempotency_records"])
	require.Equal(t, float64(0), line["session_revocations"])
	require.Equal(t, float64(2), line["total"])
	require.Equal(t, fixtureNow.Add(-sweep.Grace).String(), line["before"])
}

// TestNew_AMissingDependency_IsAConstructionError. The clock especially: a nil one would send the
// service to the wall clock, which is the ban in law form.
func TestNew_AMissingDependency_IsAConstructionError(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name string
		cfg  sweep.Config
	}{
		{"no store", sweep.Config{Clock: clock.NewTest(fixtureNow), Log: log}},
		{"no clock", sweep.Config{Store: &store.DB{}, Log: log}},
		{"no logger", sweep.Config{Store: &store.DB{}, Clock: clock.NewTest(fixtureNow)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := sweep.New(tc.cfg)
			require.Error(t, err)
		})
	}
}

type fixture struct {
	t          *testing.T
	store      *store.DB
	ids        *core.Generator
	svc        *sweep.Service
	logs       *bytes.Buffer
	providerID string
	membership string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tod.db"),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(t.Context()))

	svc, err := sweep.New(sweep.Config{
		Store: db, Clock: clock.NewTest(fixtureNow), Log: log,
	})
	require.NoError(t, err)

	f := &fixture{t: t, store: db, ids: core.NewGenerator(rand.Reader), svc: svc, logs: logs}
	f.seedProvider()
	f.seedMembership()
	return f
}

func (f *fixture) seedProvider() {
	f.t.Helper()
	id, err := core.NewID[core.IdentityProvider](f.ids, fixtureNow)
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentityProvider(f.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: id.String(), Key: "local", Kind: schemaenum.IdentityProviderKindLocal,
			DisplayName: "This server", Enabled: 1, VerifiableSubject: 0,
			CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
	require.NoError(f.t, err)
	f.providerID = id.String()
}

// seedMembership exists only because `idempotency_record.principal_membership_id` is a foreign key:
// the principal is the MEMBERSHIP, never the token.
func (f *fixture) seedMembership() {
	f.t.Helper()
	circleID, err := core.NewID[core.Circle](f.ids, fixtureNow)
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateCircle(f.t.Context(), sqlitegen.CreateCircleParams{
		CircleID: circleID.String(), Name: "Riot Blue", NameNorm: core.Normalise("Riot Blue"),
		Server: schemaenum.ServerBlue, Timezone: "UTC", MinReportersToSupersede: 1,
		RevokeInvalidatesInvites: 1, State: schemaenum.CircleStateActive,
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)

	identityID, err := core.NewID[core.Identity](f.ids, fixtureNow)
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdentity(f.t.Context(), sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: f.providerID, Subject: identityID.String(),
		DisplayName: "Tankguy", CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)

	id, err := core.NewID[core.Membership](f.ids, fixtureNow)
	require.NoError(f.t, err)
	subject := identityID.String()
	_, err = f.store.Queries().CreateMembership(f.t.Context(), sqlitegen.CreateMembershipParams{
		ID: id.String(), CircleID: circleID.String(), IdentityID: &subject,
		Kind: schemaenum.MembershipKindHuman, DisplayName: "Tankguy",
		DisplayNameNorm: "tankguy", Role: schemaenum.MembershipRoleOwner,
		JoinedAt: int64(fixtureNow), CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
	f.membership = id.String()
}

func (f *fixture) seedAuthFlow(key string, expiresAt core.Micros) {
	f.t.Helper()
	id, err := core.NewID[core.AuthFlow](f.ids, fixtureNow)
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateAuthFlow(f.t.Context(), sqlitegen.CreateAuthFlowParams{
		ID: id.String(), State: key, PkceVerifier: "verifier-" + key,
		ProviderID: f.providerID, ExpiresAt: int64(expiresAt),
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
}

func (f *fixture) authFlowExists(key string) bool {
	f.t.Helper()
	_, err := f.store.Queries().GetAuthFlowByState(f.t.Context(), key)
	return f.exists(err)
}

func (f *fixture) seedCredentialTicket(key string, expiresAt core.Micros) {
	f.t.Helper()
	id, err := core.NewID[core.CredentialTicket](f.ids, fixtureNow)
	require.NoError(f.t, err)
	// created_at is derived, not chosen: the CHECK pins the 120-second lifetime.
	_, err = f.store.Queries().CreateCredentialTicket(f.t.Context(),
		sqlitegen.CreateCredentialTicketParams{
			ID: id.String(), TicketHash: []byte(key), ProviderID: f.providerID,
			Subject: "subject-" + key, DisplayName: "Tankguy", GuildRolesJson: "[]",
			ExpiresAt: int64(expiresAt), CreatedAt: int64(expiresAt.Add(-ticketTTL)),
			UpdatedAt: int64(fixtureNow),
		})
	require.NoError(f.t, err)
}

func (f *fixture) credentialTicketExists(key string) bool {
	f.t.Helper()
	_, err := f.store.Queries().GetCredentialTicketByHash(f.t.Context(), []byte(key))
	return f.exists(err)
}

func (f *fixture) seedIdempotencyRecord(key string, expiresAt core.Micros) {
	f.t.Helper()
	id, err := core.NewID[core.IdempotencyRecord](f.ids, fixtureNow)
	require.NoError(f.t, err)
	_, err = f.store.Queries().CreateIdempotencyRecord(f.t.Context(),
		sqlitegen.CreateIdempotencyRecordParams{
			ID: id.String(), PrincipalMembershipID: f.membership, Key: key,
			RequestHash: []byte("hash-" + key), ExpiresAt: int64(expiresAt),
			CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
	require.NoError(f.t, err)
}

func (f *fixture) idempotencyRecordExists(key string) bool {
	f.t.Helper()
	_, err := f.store.Queries().GetIdempotencyRecord(f.t.Context(),
		sqlitegen.GetIdempotencyRecordParams{
			PrincipalMembershipID: f.membership, Key: key,
		})
	return f.exists(err)
}

// seedSessionRevocation writes a signed-out session whose own expiry is expiresAt. The row's whole
// job is to outlive the cookie by nothing: past that instant the codec refuses the cookie on its
// own, so keeping the row buys nobody anything.
func (f *fixture) seedSessionRevocation(key string, expiresAt core.Micros) {
	f.t.Helper()
	err := f.store.Queries().RevokeSession(f.t.Context(), sqlitegen.RevokeSessionParams{
		SessionID: key, ExpiresAt: int64(expiresAt),
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(f.t, err)
}

func (f *fixture) sessionRevocationExists(key string) bool {
	f.t.Helper()
	n, err := f.store.Queries().CountSessionRevocations(f.t.Context(), key)
	require.NoError(f.t, err)
	return n > 0
}

// exists separates "the row is gone" from "the read failed", so a broken query cannot read as a
// successful deletion.
func (f *fixture) exists(err error) bool {
	f.t.Helper()
	if err == nil {
		return true
	}
	require.True(f.t, store.IsNotFound(err), "unexpected read error: %v", err)
	return false
}
