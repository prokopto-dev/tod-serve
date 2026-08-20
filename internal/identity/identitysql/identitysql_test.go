package identitysql_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity/identitysql"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// These are integration tests against real SQLite in t.TempDir(), because the properties they
// check are the SCHEMA's: the credential_ticket TTL is a CHECK, single use is a trigger, and the
// guild facts column is validated by json_valid. A mock would assert that the mock has them.

var now = core.MicrosFromTime(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))

// hashCode is the stand-in for whatever mints invite codes. It is injected precisely so this
// package does not become a second opinion about the real one.
func hashCode(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}

type fixture struct {
	ctx        context.Context
	store      *identitysql.Store
	queries    *sqlitegen.Queries
	clock      *clock.Test
	ids        *core.Generator
	providerID string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tod.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(ctx))

	testClock := clock.NewTest(now)
	adapter, err := identitysql.New(db.Queries(), testClock, hashCode)
	require.NoError(t, err)

	f := &fixture{
		ctx: ctx, store: adapter, queries: db.Queries(),
		clock: testClock, ids: core.NewGenerator(rand.Reader),
	}
	f.providerID = f.newID(t)

	clientID, secret, redirect := "111111111111111111", "operator-secret", "https://tod.example.com/cb"
	_, err = f.queries.CreateIdentityProvider(ctx, sqlitegen.CreateIdentityProviderParams{
		ID: f.providerID, Key: "discord", Kind: "discord", DisplayName: "Sign in with Discord",
		Enabled: 1, VerifiableSubject: 1,
		ClientID: &clientID, ClientSecret: &secret, RedirectUri: &redirect,
		CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)
	return f
}

func (f *fixture) newID(t *testing.T) string {
	t.Helper()
	u, err := f.ids.New(now)
	require.NoError(t, err)
	return u.String()
}

// ticket returns a well-formed ticket for the fixture's provider.
func (f *fixture) ticket(t *testing.T, secret string) identity.Ticket {
	t.Helper()
	return identity.Ticket{
		ID:          f.newID(t),
		Hash:        identity.HashTicket(secret),
		ProviderID:  f.providerID,
		Subject:     "333333333333333333",
		DisplayName: "Tankguy",
		GuildFacts: identity.GuildFacts{
			"222222222222222222": discord.GuildFact{Member: true, RoleIDs: []string{"raider"}},
		},
		CreatedAt: now,
		ExpiresAt: now.Add(identity.TicketTTL),
	}
}

func TestProvider_RoundTripsThroughTheRealTable(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got, err := f.store.ProviderByKey(f.ctx, "discord")
	require.NoError(t, err)
	require.NoError(t, got.Validate())
	require.Equal(t, identity.KindDiscord, got.Kind)
	require.True(t, got.Enabled)
	require.True(t, got.VerifiableSubject)
	require.Equal(t, "operator-secret", got.ClientSecret.Reveal())
	require.Equal(t, "***", got.ClientSecret.String(), "the secret does not render itself")

	byID, err := f.store.ProviderByID(f.ctx, f.providerID)
	require.NoError(t, err)
	require.Equal(t, got, byID)

	_, err = f.store.ProviderByKey(f.ctx, "nobody")
	require.ErrorIs(t, err, identity.ErrNotFound)
}

// The 120-second TTL is a CHECK, so a longer-lived ticket cannot be written AT ALL — not by this
// adapter, not by a future one, not by a migration that forgot. This is that, through the code
// path the flow actually takes.
func TestCredentialTicket_AnyOtherTTLThroughTheAdapter_IsUnrepresentable(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	live := f.ticket(t, "the-ticket")
	require.NoError(t, f.store.CreateTicket(f.ctx, live))

	for _, ttl := range []time.Duration{119 * time.Second, 121 * time.Second, time.Hour, 0} {
		longer := f.ticket(t, "ticket-"+ttl.String())
		longer.ExpiresAt = longer.CreatedAt.Add(ttl)
		require.Error(t, f.store.CreateTicket(f.ctx, longer),
			"a ticket with a %s lifetime must not be writable", ttl)
	}
}

// Single use, and unrepresentable rather than merely refused: the query carries
// `WHERE consumed_at IS NULL` and trg_credential_ticket_single_use aborts a write that tried
// anyway. A second redemption is a second PAT for one authorization.
//
// internal/store's TestCredentialTicket_SecondConsumption_Refused asserts the same thing at the
// database. This one asserts that the path the flow actually takes reaches it, which is the half
// that breaks when an adapter grows a convenience.
func TestCredentialTicket_SecondConsumptionThroughTheAdapter_Refused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	require.NoError(t, f.store.CreateTicket(f.ctx, f.ticket(t, "the-ticket")))
	hash := identity.HashTicket("the-ticket")

	first, err := f.store.ConsumeTicket(f.ctx, hash, now)
	require.NoError(t, err)
	require.Equal(t, "333333333333333333", first.Subject)
	require.Equal(t,
		discord.GuildFact{Member: true, RoleIDs: []string{"raider"}},
		first.GuildFacts["222222222222222222"],
		"the guild facts survive the column, three states and all")

	_, err = f.store.ConsumeTicket(f.ctx, hash, now)
	require.ErrorIs(t, err, identity.ErrNotFound)

	// And a consumed ticket reads as absent, so a redemption cannot distinguish "already used"
	// from "never existed" and use the difference as an oracle.
	_, err = f.store.ReadTicket(f.ctx, hash)
	require.ErrorIs(t, err, identity.ErrNotFound)
}

func TestAuthFlow_SecondConsumption_Refused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	flow := identity.AuthFlow{
		ID: f.newID(t), State: "state-1", PKCEVerifier: "verifier-1",
		ProviderID: f.providerID, ExpiresAt: now.Add(identity.AuthFlowTTL), CreatedAt: now,
	}
	require.NoError(t, f.store.CreateAuthFlow(f.ctx, flow))

	got, err := f.store.ConsumeAuthFlow(f.ctx, "state-1", now)
	require.NoError(t, err)
	require.Equal(t, "verifier-1", got.PKCEVerifier)
	require.Empty(t, got.CircleID, "an empty circle is NULL, not the empty string")

	_, err = f.store.ConsumeAuthFlow(f.ctx, "state-1", now)
	require.ErrorIs(t, err, identity.ErrNotFound)

	_, err = f.store.ConsumeAuthFlow(f.ctx, "a-state-nobody-issued", now)
	require.ErrorIs(t, err, identity.ErrNotFound)
}

// seedCircle creates the rows an invite needs to exist at all, and returns the circle and the
// membership that minted it.
func (f *fixture) seedCircle(t *testing.T) (circleID, membershipID string) {
	t.Helper()
	circleID, membershipID = f.newID(t), f.newID(t)

	// A human membership must carry an identity — ck_membership_human_has_an_identity — so the
	// owner gets one. That CHECK is why a fixture cannot cut this corner.
	ownerIdentityID := f.newID(t)

	_, err := f.queries.CreateCircle(f.ctx, sqlitegen.CreateCircleParams{
		CircleID: circleID, Name: "Vanquish", NameNorm: "vanquish", Description: "",
		Server: "blue", Timezone: "UTC", MinReportersToSupersede: 2,
		RevokeInvalidatesInvites: 1, State: "active", CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)

	_, err = f.queries.CreateIdentity(f.ctx, sqlitegen.CreateIdentityParams{
		ID: ownerIdentityID, ProviderID: f.providerID, Subject: "owner-" + circleID,
		DisplayName: "Owner", CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)

	_, err = f.queries.CreateMembership(f.ctx, sqlitegen.CreateMembershipParams{
		ID: membershipID, CircleID: circleID, IdentityID: &ownerIdentityID, Kind: "human",
		DisplayName: "Owner", DisplayNameNorm: "owner", Role: "owner",
		JoinedAt: int64(now), CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)
	return circleID, membershipID
}

func TestGuildGate_ReadsTheCircleProviderColumns(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	circleID, _ := f.seedCircle(t)
	guildID := "222222222222222222"

	_, err := f.queries.PutCircleProvider(f.ctx, sqlitegen.PutCircleProviderParams{
		CircleID: circleID, ProviderID: f.providerID,
		DiscordGuildID: &guildID, DiscordRequiredRoleIdsJson: `["raider","officer"]`,
		CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)

	gate, err := f.store.GuildGate(f.ctx, circleID, f.providerID)
	require.NoError(t, err)
	require.Equal(t, guildID, gate.GuildID)
	require.Equal(t, []string{"raider", "officer"}, gate.RequiredRoleIDs)

	// A circle that does not accept this provider is not-found, which is how the flow tells
	// "no gate" from "not accepted".
	_, err = f.store.GuildGate(f.ctx, circleID, "01J0000000000000NOTHERE")
	require.ErrorIs(t, err, identity.ErrNotFound)
}

// The instance-level fact createAuthorizationURL falls back to when there is no invite. It names
// no circle, which is the whole reason it can be asked on a public route.
func TestAnyCircleGatesOnAGuild_IsAnInstanceLevelBit(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	circleID, _ := f.seedCircle(t)

	gated, err := f.store.AnyCircleGatesOnAGuild(f.ctx)
	require.NoError(t, err)
	require.False(t, gated)

	_, err = f.queries.PutCircleProvider(f.ctx, sqlitegen.PutCircleProviderParams{
		CircleID: circleID, ProviderID: f.providerID,
		DiscordRequiredRoleIdsJson: "[]", CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)

	gated, err = f.store.AnyCircleGatesOnAGuild(f.ctx)
	require.NoError(t, err)
	require.False(t, gated, "accepting the provider is not the same as gating on a guild")

	guildID := "222222222222222222"
	_, err = f.queries.PutCircleProvider(f.ctx, sqlitegen.PutCircleProviderParams{
		CircleID: circleID, ProviderID: f.providerID, DiscordGuildID: &guildID,
		DiscordRequiredRoleIdsJson: "[]", CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)

	gated, err = f.store.AnyCircleGatesOnAGuild(f.ctx)
	require.NoError(t, err)
	require.True(t, gated)
}

func TestInvite_LivenessAndDeadCodes(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	circleID, membershipID := f.seedCircle(t)

	create := func(t *testing.T, code string, maxUses int64, expiresAt core.Micros) string {
		t.Helper()
		id := f.newID(t)
		_, err := f.queries.CreateInvite(f.ctx, sqlitegen.CreateInviteParams{
			ID: id, CircleID: circleID, CodeHash: hashCode(code), CodePrefix: code[:9],
			Role: "member", MaxUses: maxUses, ExpiresAt: int64(expiresAt),
			CreatedByMembershipID: membershipID, MintedByKind: "session", Note: "",
			CreatedAt: int64(now), UpdatedAt: int64(now),
		})
		require.NoError(t, err)
		return id
	}

	live := "TODI-LIVE0-00001"
	create(t, live, 5, now.Add(time.Hour))
	got, err := f.store.InviteByCode(f.ctx, live)
	require.NoError(t, err)
	require.True(t, got.Live)
	require.Equal(t, circleID, got.CircleID)
	require.Equal(t, hashCode(live), got.CodeHash)

	// The same invite by hash, which is what the callback holds: it never sees the code.
	byHash, err := f.store.InviteByCodeHash(f.ctx, hashCode(live))
	require.NoError(t, err)
	require.Equal(t, got, byHash)

	expired := "TODI-EXPIR-00001"
	create(t, expired, 5, now.Add(-time.Second))
	got, err = f.store.InviteByCode(f.ctx, expired)
	require.NoError(t, err)
	require.False(t, got.Live)
	require.Equal(t, identity.CodeInviteExpired, got.DeadCode)

	exhausted := "TODI-EXHST-00001"
	exhaustedID := create(t, exhausted, 1, now.Add(time.Hour))
	_, err = f.queries.ConsumeInvite(f.ctx, sqlitegen.ConsumeInviteParams{
		UpdatedAt: int64(now), CircleID: circleID, ID: exhaustedID, Now: int64(now),
	})
	require.NoError(t, err)
	got, err = f.store.InviteByCode(f.ctx, exhausted)
	require.NoError(t, err)
	require.False(t, got.Live)
	require.Equal(t, identity.CodeInviteExhausted, got.DeadCode)

	// Revoked wins over expired: that is the fact an officer acted on and the one they look for.
	revoked := "TODI-REVOK-00001"
	revokedID := create(t, revoked, 5, now.Add(-time.Second))
	revokedAt := int64(now)
	_, err = f.queries.RevokeInvite(f.ctx, sqlitegen.RevokeInviteParams{
		RevokedAt: &revokedAt, UpdatedAt: int64(now), CircleID: circleID, ID: revokedID,
	})
	require.NoError(t, err)
	got, err = f.store.InviteByCode(f.ctx, revoked)
	require.NoError(t, err)
	require.False(t, got.Live)
	require.Equal(t, identity.CodeInviteRevoked, got.DeadCode)

	_, err = f.store.InviteByCode(f.ctx, "TODI-NOSUC-H0001")
	require.ErrorIs(t, err, identity.ErrNotFound)
}

func TestIdentityBySubject_ReportsTheInstanceBlock(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	circleID, membershipID := f.seedCircle(t)
	identityID := f.newID(t)

	_, err := f.queries.CreateIdentity(f.ctx, sqlitegen.CreateIdentityParams{
		ID: identityID, ProviderID: f.providerID, Subject: "333333333333333333",
		DisplayName: "Tankguy", CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)

	got, err := f.store.IdentityBySubject(f.ctx, f.providerID, "333333333333333333")
	require.NoError(t, err)
	require.False(t, got.Blocked)

	blockedAt, blockedBy := int64(now), membershipID
	_, err = f.queries.BlockIdentity(f.ctx, sqlitegen.BlockIdentityParams{
		BlockedAt: &blockedAt, BlockedByMembershipID: &blockedBy,
		UpdatedAt: int64(now), ID: identityID,
	})
	require.NoError(t, err)

	got, err = f.store.IdentityBySubject(f.ctx, f.providerID, "333333333333333333")
	require.NoError(t, err)
	require.True(t, got.Blocked)

	_, err = f.store.IdentityBySubject(f.ctx, f.providerID, "a-subject-we-have-never-seen")
	require.ErrorIs(t, err, identity.ErrNotFound)

	// And the circles that identity belongs to — the lookup the callback makes on the re-auth
	// path, keyed on something the caller proved rather than something they supplied.
	_, err = f.queries.CreateMembership(f.ctx, sqlitegen.CreateMembershipParams{
		ID: f.newID(t), CircleID: circleID, IdentityID: &identityID, Kind: "human",
		DisplayName: "Tankguy", DisplayNameNorm: "tankguy", Role: "member",
		JoinedAt: int64(now), CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)

	circles, err := f.store.CircleIDsForIdentity(f.ctx, identityID)
	require.NoError(t, err)
	require.Equal(t, []string{circleID}, circles)
}

// An invite naming a DELETED circle resolves to nothing, on THIS path as well as through
// `invite.Resolve`.
//
// `createAuthorizationURL` resolves a code through here, not through `internal/invite`. Without the
// live-circle predicate it would hand back an OAuth URL and write an `auth_flow` row for a circle
// that no longer exists, while `previewInvite` called the same code invalid — two public routes
// disagreeing about one code, and a stored row for a circle nobody can join.
//
// It is `ErrNotFound` rather than a dead-code reason: a tombstoned circle is not a dead invite, it
// is a code that names nothing, and that is the same answer an unissued code gets. Saying more
// would let a former member confirm what happened to a circle they can no longer see.
func TestInvite_ForADeletedCircle_ResolvesToNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	circleID, membershipID := f.seedCircle(t)

	const code = "TODI-GONE0-00001"
	_, err := f.queries.CreateInvite(f.ctx, sqlitegen.CreateInviteParams{
		ID: f.newID(t), CircleID: circleID, CodeHash: hashCode(code), CodePrefix: code[:9],
		Role: "member", MaxUses: 5, ExpiresAt: int64(now.Add(time.Hour)),
		CreatedByMembershipID: membershipID, MintedByKind: "session", Note: "",
		CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)

	live, err := f.store.InviteByCode(f.ctx, code)
	require.NoError(t, err)
	require.True(t, live.Live)

	deletedAt := int64(now)
	_, err = f.queries.SoftDeleteCircle(f.ctx, sqlitegen.SoftDeleteCircleParams{
		DeletedAt: &deletedAt, UpdatedAt: deletedAt, CircleID: circleID,
	})
	require.NoError(t, err)

	// Both lookups, because the callback holds the hash and never the code.
	_, err = f.store.InviteByCode(f.ctx, code)
	require.ErrorIs(t, err, identity.ErrNotFound)
	_, err = f.store.InviteByCodeHash(f.ctx, hashCode(code))
	require.ErrorIs(t, err, identity.ErrNotFound)
}
