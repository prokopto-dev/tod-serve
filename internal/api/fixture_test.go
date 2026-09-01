package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/identitysql"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/instancesettings"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/setup"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// The fixture's fixed instant. Every time-dependent assertion here is relative to it, so a test
// that fails does not depend on when it ran.
const fixtureNow = core.Micros(1_755_483_247_000_000)

const (
	testPepper     = core.Secret("integration-test-pepper")
	testSessionKey = core.Secret("integration-test-session-key")
	testMetricsTok = core.Secret("integration-test-metrics-token")
	// testSetupTok arms first-run setup on the default harness. Constant rather than random so a
	// failure reproduces, and long enough that the wrong-token test can differ from it in one
	// character without also differing in LENGTH — `subtle.ConstantTimeCompare` returns early on a
	// length mismatch, so a shorter wrong token would be a weaker test than it looks.
	testSetupTok = core.Secret("integration-test-setup-token")
	testWrongTok = core.Secret("integration-test-setup-tokeN")
)

// harness is a wired server over a real migrated SQLite database in t.TempDir().
//
// There are no mocks of the database anywhere in this file, deliberately: the rules being tested —
// membership state read on every request, tenancy resolved from the membership row, idempotency
// keyed on `(membership, key)` — are rules about rows, and a mock would let every one of them pass
// while the schema said otherwise.
type harness struct {
	t         *testing.T
	server    *api.Server
	store     *store.DB
	clock     *clock.Test
	ids       *core.Generator
	minter    *auth.Minter
	codec     *auth.SessionCodec
	handler   http.Handler
	provider  core.IdentityProviderID
	circles   *circle.Service
	invites   *invite.Service
	identity  *identity.Service
	members   *membership.Service
	catalogue *catalogue.Service
	tods      *tod.Service
	states    *projection.Service
	grants    *instancegrant.Service
	bindings  *discord.Service
	// discordProvider is written on demand: an instance holds at most one `discord` provider row,
	// so a second seeder would fail the partial unique index rather than build a second one.
	discordProvider core.IdentityProviderID
	invalidator     *recordingInvalidator
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := t.Context()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tod.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(ctx))

	clk := clock.NewTest(fixtureNow)
	ids := core.NewGenerator(rand.Reader)
	minter, err := auth.NewMinter(testPepper, rand.Reader)
	require.NoError(t, err)
	codec, err := auth.NewSessionCodec(testSessionKey)
	require.NoError(t, err)
	svc := newServices(t, db, clk, ids, minter, log)
	authn, err := auth.NewAuthenticator(
		db, minter, codec, svc.grants, clk, log, auth.DefaultStepUpWindows())
	require.NoError(t, err)
	// The recorder WRAPS the real projection rather than standing in for it. Both questions are
	// worth answering in this suite: "did the route push the invalidation" needs the record, and
	// "did the board actually stop serving the old window" needs the real thing behind it.
	invalidator := &recordingInvalidator{delegate: svc.states}

	server, err := api.New(api.Config{
		Version:          "0.0.0-test",
		Store:            db,
		Auth:             authn,
		Sessions:         codec,
		Circles:          svc.circles,
		Members:          svc.members,
		Invites:          svc.invites,
		Identities:       svc.identities,
		Catalogue:        svc.catalogue,
		InstanceSettings: svc.settings,
		Tods:             svc.tods,
		States:           svc.states,
		Invalidator:      invalidator,
		Clock:            clk,
		Log:              log,
		IDs:              ids,
		Metrics:          api.MetricsConfig{Enabled: true, Token: testMetricsTok},
		DiscordBindings:  svc.bindings,
		DiscordCommands:  svc.commands,
		DiscordVerifier:  testDiscordVerifier(t, clk),
		Setup:            svc.setup,
		// Armed on the DEFAULT harness, so that "setup is reachable" is the state every test in
		// this package runs against and a route that stopped refusing correctly is a red test
		// somewhere rather than a green one everywhere.
		SetupToken: api.SetupConfig{Token: testSetupTok},
		// Response validation runs across the WHOLE integration suite: every request any test in
		// this package makes is checked against the response contract, including the ones the
		// framework answers before a handler runs.
		OnResponseViolation: func(v api.Violation) { t.Errorf("response contract: %s", v) },
	})
	require.NoError(t, err)

	return &harness{
		t: t, server: server, store: db, clock: clk, ids: ids,
		minter: minter, codec: codec, handler: server.Handler(),
		circles: svc.circles, invites: svc.invites, members: svc.members,
		identity:  svc.identities,
		catalogue: svc.catalogue, tods: svc.tods, states: svc.states, grants: svc.grants,
		bindings:    svc.bindings,
		invalidator: invalidator,
	}
}

// wiredServices is the domain half of the harness, built exactly the way `cmd/tod-serve` builds
// it: real services over the real store, with no mock anywhere. The rules under test here are
// rules about rows, and a mock would let every one of them pass while the schema said otherwise.
type wiredServices struct {
	circles    *circle.Service
	invites    *invite.Service
	members    *membership.Service
	identities *identity.Service
	catalogue  *catalogue.Service
	tods       *tod.Service
	states     *projection.Service
	grants     *instancegrant.Service
	setup      *setup.Service
	settings   *instancesettings.Service
	bindings   *discord.Service
	commands   *discord.Commander
}

func newServices(
	t *testing.T, db *store.DB, clk clock.Clock, ids *core.Generator,
	minter *auth.Minter, log *slog.Logger,
) wiredServices {
	t.Helper()
	circles, err := circle.New(circle.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	invites, err := invite.New(invite.Config{
		Store: db, Clock: clk, IDs: ids, Entropy: rand.Reader, Log: log,
	})
	require.NoError(t, err)

	identityStore, err := identitysql.New(db.Queries(), clk, invite.HashCode, invite.GrantByCodeHash)
	require.NoError(t, err)
	clients, err := identity.NewGuardedClients(clk)
	require.NoError(t, err)
	identities, err := identity.New(identity.Config{
		Store: identityStore, Clients: clients, Clock: clk, IDs: ids,
		Entropy: rand.Reader, SPAJoinURL: "https://tod.example.com/join",
		CallbackBaseURL: "https://tod.example.com/api/v1/auth/callback", Logger: log,
	})
	require.NoError(t, err)

	// The real ledger over the real table. There is no fake: what an instance-realm route answers
	// depends on rows, and a stub that returned a set would test the middleware against itself.
	grants, err := instancegrant.New(instancegrant.Config{
		Store: db, Clock: clk, IDs: ids, Log: log,
	})
	require.NoError(t, err)

	members, err := membership.New(membership.Config{
		Store: db, Clock: clk, IDs: ids, Minter: minter, Identity: identities,
		Grants: grants, Log: log, Entropy: rand.Reader,
	})
	require.NoError(t, err)

	// The catalogue is wired and left EMPTY. That is the default state of this whole suite on
	// purpose: an instance with no targets and no timers is what an operator's VPS is on day one,
	// so every test that does not deliberately seed one is exercising it.
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

	first, err := setup.New(setup.Config{
		Store: db, Circles: circles, Invites: invites, Identities: identities,
		Catalogue: catalogues, Clock: clk, Log: log,
	})
	require.NoError(t, err)

	// The real settings service over the real tables, for the reason the ledger beside it is real:
	// what these routes answer depends on rows, and the hash chain is one of the things under test.
	settings, err := instancesettings.New(instancesettings.Config{
		Store: db, Clock: clk, IDs: ids, Log: log,
	})
	require.NoError(t, err)

	bindings, err := discord.New(discord.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	commands, err := discord.NewCommander(discord.CommanderConfig{
		Bindings: bindings, Providers: identities, Circles: circles, Boards: states,
		Targets: catalogues, Reports: tods, Clock: clk, Log: log,
	})
	require.NoError(t, err)

	return wiredServices{
		circles: circles, invites: invites, members: members, identities: identities,
		catalogue: catalogues, tods: tods, states: states, grants: grants, setup: first,
		settings: settings, bindings: bindings, commands: commands,
	}
}

// newHarnessWithConsole builds the same server with a stub console behind the API.
//
// The stub is a stand-in for `internal/ui`, deliberately: what these tests are about is the SPLIT
// — which requests reach the API and which fall through — and wiring the real console would make
// them depend on whether somebody had run `make build-web`, which is exactly the kind of
// conditional coverage this repository refuses.
func newHarnessWithConsole(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := newServices(t, h.store, h.clock, h.ids, h.minter, log)
	authn, err := auth.NewAuthenticator(
		h.store, h.minter, h.codec, svc.grants, h.clock, log, auth.DefaultStepUpWindows())
	require.NoError(t, err)

	server, err := api.New(api.Config{
		Version:             "0.0.0-test",
		Store:               h.store,
		Auth:                authn,
		Sessions:            h.codec,
		Circles:             svc.circles,
		Members:             svc.members,
		Invites:             svc.invites,
		Identities:          svc.identities,
		Catalogue:           svc.catalogue,
		InstanceSettings:    svc.settings,
		Tods:                svc.tods,
		States:              svc.states,
		Invalidator:         h.invalidator,
		Clock:               h.clock,
		Log:                 log,
		IDs:                 h.ids,
		DiscordBindings:     svc.bindings,
		DiscordCommands:     svc.commands,
		DiscordVerifier:     testDiscordVerifier(t, h.clock),
		Console:             stubConsole(),
		Metrics:             api.MetricsConfig{Enabled: true, Token: testMetricsTok},
		Setup:               svc.setup,
		SetupToken:          api.SetupConfig{Token: testSetupTok},
		OnResponseViolation: func(v api.Violation) { t.Errorf("response contract: %s", v) },
	})
	require.NoError(t, err)
	h.server, h.handler = server, server.Handler()
	return h
}

// newHarnessWithoutMetrics builds the same server with metrics off, which is the DEFAULT: a
// metrics endpoint that is on unless somebody turns it off is an information leak nobody chose.
func newHarnessWithoutMetrics(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := newServices(t, h.store, h.clock, h.ids, h.minter, log)
	authn, err := auth.NewAuthenticator(
		h.store, h.minter, h.codec, svc.grants, h.clock, log, auth.DefaultStepUpWindows())
	require.NoError(t, err)

	server, err := api.New(api.Config{
		Version:             "0.0.0-test",
		Store:               h.store,
		Auth:                authn,
		Sessions:            h.codec,
		Circles:             svc.circles,
		Members:             svc.members,
		Invites:             svc.invites,
		Identities:          svc.identities,
		Catalogue:           svc.catalogue,
		InstanceSettings:    svc.settings,
		Tods:                svc.tods,
		States:              svc.states,
		Invalidator:         h.invalidator,
		Clock:               h.clock,
		Log:                 log,
		IDs:                 h.ids,
		DiscordBindings:     svc.bindings,
		DiscordCommands:     svc.commands,
		DiscordVerifier:     testDiscordVerifier(t, h.clock),
		Setup:               svc.setup,
		SetupToken:          api.SetupConfig{Token: testSetupTok},
		OnResponseViolation: func(v api.Violation) { t.Errorf("response contract: %s", v) },
	})
	require.NoError(t, err)
	h.server, h.handler = server, server.Handler()
	return h
}

// newHarnessWithoutSetupToken builds the same server with `TOD_SETUP_TOKEN` unset.
//
// It is the FIRST of the three refusals, and it gets its own constructor rather than a flag on the
// default one so that the state under test is the state a finished instance is actually in: not
// "setup with a token nobody guessed", but setup with nothing to guess.
func newHarnessWithoutSetupToken(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := newServices(t, h.store, h.clock, h.ids, h.minter, log)
	authn, err := auth.NewAuthenticator(
		h.store, h.minter, h.codec, svc.grants, h.clock, log, auth.DefaultStepUpWindows())
	require.NoError(t, err)

	server, err := api.New(api.Config{
		Version:          "0.0.0-test",
		Store:            h.store,
		Auth:             authn,
		Sessions:         h.codec,
		Circles:          svc.circles,
		Members:          svc.members,
		Invites:          svc.invites,
		Identities:       svc.identities,
		Catalogue:        svc.catalogue,
		InstanceSettings: svc.settings,
		Tods:             svc.tods,
		States:           svc.states,
		Invalidator:      h.invalidator,
		Clock:            h.clock,
		Log:              log,
		IDs:              h.ids,
		DiscordBindings:  svc.bindings,
		DiscordCommands:  svc.commands,
		DiscordVerifier:  testDiscordVerifier(t, h.clock),
		Setup:            svc.setup,
		// No SetupToken. The zero value is the whole point: an operator who never armed setup, or
		// who removed the variable afterwards, has an instance where the routes refuse everybody.
		OnResponseViolation: func(v api.Violation) { t.Errorf("response contract: %s", v) },
	})
	require.NoError(t, err)
	h.server, h.handler = server, server.Handler()
	return h
}

// newID mints a typed id at the fixture's instant.
func newID[E core.Entity](h *harness) core.ID[E] {
	h.t.Helper()
	id, err := core.NewID[E](h.ids, h.clock.Now())
	require.NoError(h.t, err)
	return id
}

// seedInstance writes the singleton row `/meta` reads.
func (h *harness) seedInstance(selfService bool) {
	h.t.Helper()
	flag := int64(0)
	if selfService {
		flag = 1
	}
	_, err := h.store.Queries().CreateInstance(h.t.Context(), sqlitegen.CreateInstanceParams{
		Name:                      "Test Instance",
		PublicUrl:                 "https://tod.example.com",
		Timezone:                  "UTC",
		SelfServiceCircleCreation: flag,
		CreatedAt:                 int64(fixtureNow),
		UpdatedAt:                 int64(fixtureNow),
	})
	require.NoError(h.t, err)
}

// seedCircle writes a circle. Every circle is pinned to one server, immutably.
func (h *harness) seedCircle(name string) core.CircleID {
	h.t.Helper()
	id := newID[core.Circle](h)
	_, err := h.store.Queries().CreateCircle(h.t.Context(), sqlitegen.CreateCircleParams{
		CircleID:                 id.String(),
		Name:                     name,
		NameNorm:                 name,
		Description:              "",
		Server:                   schemaenum.ServerBlue,
		Timezone:                 "UTC",
		MinReportersToSupersede:  2,
		RevokeInvalidatesInvites: 1,
		State:                    schemaenum.CircleStateActive,
		CreatedAt:                int64(fixtureNow),
		UpdatedAt:                int64(fixtureNow),
	})
	require.NoError(h.t, err)
	return id
}

// localProviderKey is the wire key the fixture gives the `local` provider, matching what
// `tod-serve init --local` writes.
const localProviderKey = "local"

// seedProvider writes the instance's local identity provider, which is the one that needs no third
// party. It is written once per harness: `identity_provider.kind` is unique, so an instance has at
// most one provider of each kind.
func (h *harness) seedProvider() core.IdentityProviderID {
	h.t.Helper()
	if !h.provider.IsZero() {
		return h.provider
	}
	id := newID[core.IdentityProvider](h)
	_, err := h.store.Queries().CreateIdentityProvider(h.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: id.String(),
			// The wire key `listIdentityProviders` publishes and `/join` dispatches on. It is the
			// plain kind because an instance holds at most one `local` row, so there is nothing
			// to disambiguate — and a suffixed key would be a key no client could guess from the
			// public discovery endpoint.
			Key:         localProviderKey,
			Kind:        schemaenum.IdentityProviderKindLocal,
			DisplayName: "Local",
			Enabled:     1,
			// `verifiable_subject` is a CHECK against `kind`, never a toggle: local is 0.
			VerifiableSubject: 0,
			CreatedAt:         int64(fixtureNow),
			UpdatedAt:         int64(fixtureNow),
		})
	require.NoError(h.t, err)
	h.provider = id
	return id
}

// seedMember writes an identity and a human membership in the circle.
func (h *harness) seedMember(circle core.CircleID, role authz.Role) core.MembershipID {
	h.t.Helper()
	provider := h.seedProvider()
	identity := newID[core.Identity](h)
	_, err := h.store.Queries().CreateIdentity(h.t.Context(), sqlitegen.CreateIdentityParams{
		ID:          identity.String(),
		ProviderID:  provider.String(),
		Subject:     identity.String(),
		DisplayName: string(role) + " " + identity.String()[:8],
		CreatedAt:   int64(fixtureNow),
		UpdatedAt:   int64(fixtureNow),
	})
	require.NoError(h.t, err)

	membership := newID[core.Membership](h)
	identityID := identity.String()
	_, err = h.store.Queries().CreateMembership(h.t.Context(), sqlitegen.CreateMembershipParams{
		ID:              membership.String(),
		CircleID:        circle.String(),
		IdentityID:      &identityID,
		Kind:            schemaenum.MembershipKindHuman,
		DisplayName:     string(role),
		DisplayNameNorm: string(role),
		Role:            string(role),
		JoinedAt:        int64(fixtureNow),
		CreatedAt:       int64(fixtureNow),
		UpdatedAt:       int64(fixtureNow),
	})
	require.NoError(h.t, err)
	return membership
}

// seedToken mints a token bound to a membership and returns the credential exactly once, which is
// the only moment it exists in plaintext anywhere.
func (h *harness) seedToken(membership core.MembershipID, scopes ...authz.Scope) core.Secret {
	h.t.Helper()
	minted, err := h.minter.Mint()
	require.NoError(h.t, err)
	scopesJSON, err := auth.ScopesJSON(scopes)
	require.NoError(h.t, err)

	_, err = h.store.Queries().CreateAPIToken(h.t.Context(), sqlitegen.CreateAPITokenParams{
		ID:           newID[core.APIToken](h).String(),
		MembershipID: membership.String(),
		TokenPrefix:  minted.Prefix,
		TokenHash:    minted.Hash,
		Name:         "test device",
		ScopesJson:   scopesJSON,
		CreatedAt:    int64(fixtureNow),
		UpdatedAt:    int64(fixtureNow),
	})
	require.NoError(h.t, err)
	return minted.Token
}

// revokeMembership revokes a membership directly, which is what an officer's `revokeMember` will
// do once that route exists. Membership state is read on every request, so this takes effect on
// the next one.
func (h *harness) revokeMembership(circle core.CircleID, membership core.MembershipID) {
	h.t.Helper()
	revoker := h.seedMember(circle, authz.RoleOwner)
	at := int64(h.clock.Now())
	by := revoker.String()
	_, err := h.store.Queries().RevokeMembership(h.t.Context(), sqlitegen.RevokeMembershipParams{
		RevokedAt:             &at,
		RevokedByMembershipID: &by,
		UpdatedAt:             at,
		CircleID:              circle.String(),
		ID:                    membership.String(),
	})
	require.NoError(h.t, err)
}

// identityOf returns the identity behind a human membership, which is the key an instance grant
// hangs off. A service membership has none and this fails rather than returning a zero id: a test
// that granted an instance permission to nobody would pass for the wrong reason.
func (h *harness) identityOf(member core.MembershipID) core.IdentityID {
	h.t.Helper()
	row, err := h.store.Queries().GetMembershipByID(h.t.Context(), member.String())
	require.NoError(h.t, err)
	require.NotNil(h.t, row.IdentityID, "membership %s has no identity", member)
	id, err := core.ParseID[core.Identity](*row.IdentityID)
	require.NoError(h.t, err)
	return id
}

// grantInstance records `granted` for the identity behind a membership, through the real ledger
// over the real table. There is no shortcut past the service here on purpose: an instance-realm
// route's answer depends on rows, and inserting them by hand would let the test pass against a
// schema the service could not actually write.
func (h *harness) grantInstance(member core.MembershipID, perms ...authz.Permission) {
	h.t.Helper()
	identity := h.identityOf(member)
	for _, p := range perms {
		_, err := h.grants.Decide(h.t.Context(), instancegrant.DecideRequest{
			IdentityID: identity, Permission: p,
			Decision: instancegrant.DecisionGranted,
			Reason:   "fixture",
		})
		require.NoError(h.t, err)
	}
}

// revokeInstance records `revoked` for the identity behind a membership.
func (h *harness) revokeInstance(member core.MembershipID, perm authz.Permission) {
	h.t.Helper()
	_, err := h.grants.Decide(h.t.Context(), instancegrant.DecideRequest{
		IdentityID: h.identityOf(member), Permission: perm,
		Decision: instancegrant.DecisionRevoked,
		Reason:   "fixture",
	})
	require.NoError(h.t, err)
}

// session returns a signed session cookie value for a membership.
//
// A fresh id per call, exactly as the two routes that mint one do, so two calls are two DIFFERENT
// sessions. That is what lets a test sign one out and assert the other still works — the property
// that distinguishes "this session" from "every session for the identity".
func (h *harness) session(membership core.MembershipID, steppedUp bool) string {
	h.t.Helper()
	id, err := h.ids.New(h.clock.Now())
	require.NoError(h.t, err)
	s := auth.Session{
		ID:           id.String(),
		MembershipID: membership.String(),
		IssuedAt:     h.clock.Now(),
		ExpiresAt:    h.clock.Now().Add(auth.DefaultSessionTTL),
	}
	if steppedUp {
		s.SteppedUpAt = h.clock.Now()
	}
	value, encodeErr := h.codec.Encode(s)
	require.NoError(h.t, encodeErr)
	return value
}

// sessionProvedAt returns a session cookie whose identity proof is `ago` old.
//
// [harness.session] can only say "stepped up now" or "never", which was enough when there was one
// window. It is not enough for a graded one: the interesting sessions are the ones that satisfy a
// routine tier and not a sensitive one, and they live between the two.
func (h *harness) sessionProvedAt(membership core.MembershipID, ago time.Duration) string {
	h.t.Helper()
	id, err := h.ids.New(h.clock.Now())
	require.NoError(h.t, err)
	value, encodeErr := h.codec.Encode(auth.Session{
		ID:           id.String(),
		MembershipID: membership.String(),
		IssuedAt:     h.clock.Now().Add(-ago),
		ExpiresAt:    h.clock.Now().Add(auth.DefaultSessionTTL),
		SteppedUpAt:  h.clock.Now().Add(-ago),
	})
	require.NoError(h.t, encodeErr)
	return value
}

// request describes one call to the API.
type request struct {
	Method  string
	Path    string
	Token   core.Secret
	Session string
	Body    string
	Headers map[string]string
	Metrics bool
}

// response is what the API answered, with the problem already parsed when there was one.
type response struct {
	Status  int
	Header  http.Header
	Body    string
	Problem apierr.Problem
}

// do issues a request against the wired server.
func (h *harness) do(req request) response {
	h.t.Helper()
	var body io.Reader
	if req.Body != "" {
		body = stringReader(req.Body)
	}
	r := httptest.NewRequestWithContext(h.t.Context(), req.Method, req.Path, body)
	if req.Body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if !req.Token.IsZero() {
		r.Header.Set("Authorization", auth.BearerScheme+req.Token.Reveal())
	}
	if req.Session != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: req.Session})
	}
	for k, v := range req.Headers {
		r.Header.Set(k, v)
	}

	handler := h.handler
	if req.Metrics {
		metrics, ok := h.server.MetricsHandler()
		require.True(h.t, ok, "metrics are disabled in this harness")
		handler = metrics
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r.WithContext(context.WithoutCancel(h.t.Context())))

	out := response{Status: rec.Code, Header: rec.Header(), Body: rec.Body.String()}
	if rec.Code >= http.StatusBadRequest {
		// Unmarshalled unconditionally on a failure: every error response in this API is a
		// problem, and a body that does not parse as one is a finding rather than a special case.
		require.NoError(h.t, json.Unmarshal(rec.Body.Bytes(), &out.Problem),
			"a %d response did not parse as a problem: %s", rec.Code, rec.Body.String())
	}
	return out
}

// requireProblem asserts a response is a problem with the given code, checked against the closed
// enum rather than against a string.
func (h *harness) requireProblem(got response, code apierr.Code) {
	h.t.Helper()
	def, ok := apierr.Lookup(code)
	require.True(h.t, ok, "%s is not in the catalogue", code)
	require.Equal(h.t, def.Status, got.Status, "body was: %s", got.Body)
	require.Equal(h.t, code, got.Problem.Code, "body was: %s", got.Body)
	require.Equal(h.t, def.TypeURL(), got.Problem.Type)
	require.Equal(h.t, apierr.ContentType, contentTypeOf(got))
}

func contentTypeOf(r response) string {
	ct := r.Header.Get("Content-Type")
	if i := indexByte(ct, ';'); i >= 0 {
		return ct[:i]
	}
	return ct
}

func indexByte(s string, c byte) int {
	for i := range len(s) {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func stringReader(s string) io.Reader { return &sliceReader{data: []byte(s)} }

// sliceReader is a minimal reader so a request body needs no extra dependency.
type sliceReader struct {
	data []byte
	off  int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

// advance moves the fixture clock, for the tests that need time to pass.
func (h *harness) advance(d time.Duration) { h.clock.Advance(d) }

// seedMemberIn writes a membership in a circle the fixture did not create, for the tests that build
// a circle through the real service rather than through seedCircle.
func (h *harness) seedMemberIn(circleID core.CircleID, role authz.Role) core.MembershipID {
	h.t.Helper()
	return h.seedMember(circleID, role)
}

// timerChange is one push at the projection: which circle, which target, and which of the three
// fan-outs it was — one circle's override, one server's catalogue timer, or the whole instance
// because a target's `is_quake_target` moved.
type timerChange struct {
	Circle core.CircleID
	Target core.RaidTargetID
	Server core.Server
	Scope  string
}

// recordingInvalidator is the [catalogue.TimerInvalidator] the suite wires.
//
// It records rather than no-ops, because "did this route push the invalidation" is the question
// TestRouteRegistry_EveryTimerWritingRoute_PushesTheInvalidation exists to answer, and a no-op
// fake would let every route pass it. The failure it guards is a route that writes a window and
// tells nobody, which is invisible from the response.
//
// It is called INSIDE the writing transaction, and `q` is that transaction's query set — so it
// hands `q` on to the delegate rather than substituting one, and `inside` lets a test read the
// database through the same transaction the write is using.
type recordingInvalidator struct {
	mu      sync.Mutex
	changes []timerChange
	// delegate is the REAL projection. Recording and doing are both wanted: the record answers
	// "did this route push the invalidation", and the delegate is what makes the next board read
	// in the same test show the new window rather than the cached old one.
	delegate catalogue.TimerInvalidator
	// err, when set, is returned by every method INSTEAD of delegating. A write whose invalidation
	// failed must not report success — and, since ADR-0013, must leave no row behind either. That
	// is only assertable if the fake can fail.
	err error
	// inside, when set, runs with the writing transaction's own query set before the delegate.
	inside func(ctx context.Context, q *sqlitegen.Queries) error
}

func (r *recordingInvalidator) OnTimerChange(
	ctx context.Context, q *sqlitegen.Queries,
	circleID core.CircleID, targetID core.RaidTargetID,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes,
		timerChange{Circle: circleID, Target: targetID, Scope: "circle"})
	if r.inside != nil {
		if err := r.inside(ctx, q); err != nil {
			return err
		}
	}
	if r.err != nil {
		return r.err
	}
	return r.delegate.OnTimerChange(ctx, q, circleID, targetID)
}

func (r *recordingInvalidator) OnCatalogueTimerChange(
	ctx context.Context, q *sqlitegen.Queries, server core.Server, targetID core.RaidTargetID,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes,
		timerChange{Server: server, Target: targetID, Scope: "instance"})
	if r.inside != nil {
		if err := r.inside(ctx, q); err != nil {
			return err
		}
	}
	if r.err != nil {
		return r.err
	}
	return r.delegate.OnCatalogueTimerChange(ctx, q, server, targetID)
}

func (r *recordingInvalidator) OnQuakeTargetChange(
	ctx context.Context, q *sqlitegen.Queries, targetID core.RaidTargetID,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, timerChange{Target: targetID, Scope: "quake_target"})
	if r.inside != nil {
		if err := r.inside(ctx, q); err != nil {
			return err
		}
	}
	if r.err != nil {
		return r.err
	}
	return r.delegate.OnQuakeTargetChange(ctx, q, targetID)
}

// observeInside installs a hook that runs with the writing transaction's query set. It is how
// TestTimerWrite_TheInvalidationRunsInsideTheWritingTransaction asks what that transaction can
// already see and what the pool cannot.
func (r *recordingInvalidator) observeInside(fn func(ctx context.Context, q *sqlitegen.Queries) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inside = fn
}

// recorded returns the pushes so far.
func (r *recordingInvalidator) recorded() []timerChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]timerChange(nil), r.changes...)
}

// reset clears the RECORD, so a test can drive one route and assert on that route alone. It
// deliberately leaves `err` and `inside` alone: a test arms the failure and then resets the
// record, and a reset that disarmed it would make the assertion pass against a working push.
func (r *recordingInvalidator) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = nil
}

// failWith makes every method fail, for the assertion that a write whose invalidation failed
// answers with the failure rather than reporting success.
func (r *recordingInvalidator) failWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// timerFingerprint is everything a window-moving write can touch, for one circle and one target,
// rendered as a comparable string.
//
// It is what TestRouteRegistry_EveryTimerWritingRoute_RollsBackWhenTheInvalidationFails compares
// before and after a failed write. It reads through the generated queries rather than dumping
// tables, so the reader can see exactly which four things are being watched — the override, the
// catalogue timer on every server, the derived state, and the audit log — and a fifth would have
// to be added here deliberately.
func (h *harness) timerFingerprint(t *testing.T, subject timerSubject) string {
	t.Helper()
	ctx := t.Context()
	q := h.store.Queries()
	var out []string

	render := func(kind string, v any) {
		// JSON, not %+v: these rows carry pointer fields, and %v prints their ADDRESSES — which
		// differ between two reads of the same row and would make this comparison fail for every
		// write, including the ones that correctly rolled back.
		raw, marshalErr := json.Marshal(v)
		require.NoError(t, marshalErr)
		out = append(out, kind+" "+string(raw))
	}

	overrides, err := q.ListCircleTimerOverrides(ctx, subject.circle.String())
	require.NoError(t, err)
	for _, row := range overrides {
		render("override", row)
	}
	for _, server := range core.Servers() {
		row, timerErr := q.GetRaidTargetTimer(ctx, sqlitegen.GetRaidTargetTimerParams{
			TargetID: subject.target, Server: string(server),
		})
		if store.IsNotFound(timerErr) {
			continue
		}
		require.NoError(t, timerErr)
		render("timer", row)
	}
	states, err := q.ListTargetStates(ctx, subject.circle.String())
	require.NoError(t, err)
	for _, row := range states {
		render("state", row)
	}
	// The audit log is in here because a rollback has to take the audit row with it: an audit row
	// that survives the write it describes is worse than no row, because it is believed.
	entries, err := q.ListAuditLog(ctx, sqlitegen.ListAuditLogParams{
		CircleID: subject.circle.String(), AfterID: "", RowLimit: 1000,
	})
	require.NoError(t, err)
	for _, row := range entries {
		render("audit", row)
	}
	return strings.Join(out, "\n")
}

// sweepAuthFlows deletes every `auth_flow` row and returns how many there were.
//
// It counts by sweeping rather than by SELECT because `*sql.DB` is held only by internal/store
// (law 2) and `db/queries` carries no count — nothing in the product needs one, and adding a
// statement to the shipped surface for a test's benefit is how that surface grows. The sweep is
// the operation the product DOES have, `:execrows` already reports what it removed, and these
// rows are litter, so destroying them to count them costs a test nothing.
//
// The cutoff is far in the future on the injected clock, which is what makes it "every row"
// rather than "the expired ones".
func (h *harness) sweepAuthFlows() int {
	h.t.Helper()
	n, err := h.store.Queries().DeleteExpiredAuthFlows(
		h.t.Context(), int64(h.clock.Now().Add(365*24*time.Hour)))
	require.NoError(h.t, err)
	return int(n)
}

// The Discord application this suite's instance is configured with.
//
// The seed is a constant so the key is the same on every run: a random one would make a failing
// signature test impossible to reproduce, and this is the credential whose FAILURE path is the
// point. `crypto/rand` is not what mints it and does not need to be — nothing here is a secret an
// attacker could gain by predicting; it is a test's stand-in for an operator's public key.
var testDiscordSeed = bytes.Repeat([]byte{0x2c}, ed25519.SeedSize)

func testDiscordKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	return ed25519.NewKeyFromSeed(testDiscordSeed)
}

func testDiscordVerifier(t *testing.T, clk clock.Clock) *discord.Verifier {
	t.Helper()
	key := testDiscordKey(t)
	v, err := discord.NewVerifier(
		hex.EncodeToString(key.Public().(ed25519.PublicKey)), clk.Now)
	require.NoError(t, err)
	return v
}

// signedInteraction returns the headers and body of an interaction signed the way Discord signs
// one: hex Ed25519 over the timestamp concatenated with the RAW body.
func signedInteraction(t *testing.T, at core.Micros, payload any) (map[string]string, []byte) {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	timestamp := strconv.FormatInt(at.Time().Unix(), 10)
	signature := ed25519.Sign(testDiscordKey(t), append([]byte(timestamp), body...))
	return map[string]string{
		api.DiscordSignatureHeader: hex.EncodeToString(signature),
		api.DiscordTimestampHeader: timestamp,
	}, body
}
