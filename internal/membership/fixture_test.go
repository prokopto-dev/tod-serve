package membership_test

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity/identitysql"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

const fixtureNow = core.Micros(1_755_483_247_000_000)

// fixture wires every service the join path touches, over a real migrated SQLite database.
//
// Nothing is mocked. The rules under test — the partial unique index that makes a revoked person
// unrejoinable, `uses <= max_uses`, the guild gate reading the facts frozen on a
// `credential_ticket` — are rules about rows, and a mock would let every one of them pass while
// the schema said otherwise.
type fixture struct {
	t        *testing.T
	store    *store.DB
	clock    *clock.Test
	ids      *core.Generator
	minter   *auth.Minter
	circles  *circle.Service
	invites  *invite.Service
	members  *membership.Service
	identity *identity.Service
	grants   *instancegrant.Service
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
	minter, err := auth.NewMinter(core.Secret("membership-test-pepper"), rand.Reader)
	require.NoError(t, err)

	identityStore, err := identitysql.New(db.Queries(), clk, invite.HashCode)
	require.NoError(t, err)
	clients, err := identity.NewGuardedClients(clk)
	require.NoError(t, err)
	identities, err := identity.New(identity.Config{
		Store: identityStore, Clients: clients, Clock: clk, IDs: ids,
		Entropy: rand.Reader, SPAJoinURL: "https://tod.example.com/join",
		CallbackBaseURL: "https://tod.example.com/api/v1/auth/callback", Logger: log,
	})
	require.NoError(t, err)

	circles, err := circle.New(circle.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	invites, err := invite.New(invite.Config{
		Store: db, Clock: clk, IDs: ids, Entropy: rand.Reader, Log: log,
	})
	require.NoError(t, err)
	grants, err := instancegrant.New(instancegrant.Config{
		Store: db, Clock: clk, IDs: ids, Log: log,
	})
	require.NoError(t, err)
	members, err := membership.New(membership.Config{
		Store: db, Clock: clk, IDs: ids, Minter: minter, Identity: identities,
		Grants: grants, Log: log, Entropy: rand.Reader,
	})
	require.NoError(t, err)

	return &fixture{
		t: t, store: db, clock: clk, ids: ids, minter: minter,
		circles: circles, invites: invites, members: members, identity: identities,
		grants: grants,
	}
}

// provider writes one row of the instance registry. `verifiable_subject` and `client_id` are
// derived from the kind because the schema derives them too.
func (f *fixture) provider(key, kind string) string {
	f.t.Helper()
	id, err := core.NewID[core.IdentityProvider](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	params := sqlitegen.CreateIdentityProviderParams{
		ID: id.String(), Key: key, Kind: kind, DisplayName: key,
		Enabled: 1, VerifiableSubject: 1,
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

// localCircle is the ordinary fixture: an instance offering `local`, and a circle that accepts it.
func (f *fixture) localCircle(name string) circle.Circle {
	f.t.Helper()
	f.provider("local", schemaenum.IdentityProviderKindLocal)
	view, err := f.circles.Create(f.t.Context(), circle.CreateRequest{
		Name: name, Server: core.Server(schemaenum.ServerBlue),
	})
	require.NoError(f.t, err)
	updated, err := f.circles.SetProviders(f.t.Context(), view.ID, circle.SetProvidersRequest{
		Providers:                 []circle.AcceptedProvider{{Key: "local"}},
		AcknowledgeWeakRevocation: true,
	})
	require.NoError(f.t, err)
	return updated
}

// discordCircle is a circle gated on a Discord guild, with the roles the gate requires.
func (f *fixture) discordCircle(name, guildID string, roleIDs []string) (circle.Circle, string) {
	f.t.Helper()
	providerID := f.provider("discord", schemaenum.IdentityProviderKindDiscord)
	view, err := f.circles.Create(f.t.Context(), circle.CreateRequest{
		Name: name, Server: core.Server(schemaenum.ServerBlue),
	})
	require.NoError(f.t, err)
	updated, err := f.circles.SetProviders(f.t.Context(), view.ID, circle.SetProvidersRequest{
		Providers: []circle.AcceptedProvider{{
			Key: "discord", DiscordGuildID: guildID, DiscordRequiredRoleIDs: roleIDs,
		}},
	})
	require.NoError(f.t, err)
	return updated, providerID
}

// ownerGrant mints the one-time code the CLI prints, so a test has an owner to act as.
func (f *fixture) ownerGrant(view circle.Circle) invite.Code {
	f.t.Helper()
	code, _, err := f.invites.MintOwnerGrant(f.t.Context(), view.ID)
	require.NoError(f.t, err)
	return code
}

// ticket writes a `credential_ticket` carrying the provider's facts, exactly as the OAuth callback
// would, and returns the plaintext ticket a client presents.
//
// The facts are FROZEN on the row — a trigger refuses an edit after minting — because the guild
// roles on it ARE the gate's input, and a gate evaluated against an edited copy is not a gate.
func (f *fixture) ticket(
	providerID, subject, displayName string, facts discord.GuildFacts,
) string {
	f.t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(f.t, err)
	plaintext := base64.RawURLEncoding.EncodeToString(raw)

	id, err := core.NewID[core.CredentialTicket](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	factsJSON, err := discord.MarshalFacts(facts)
	require.NoError(f.t, err)

	now := f.clock.Now()
	_, err = f.store.Queries().CreateCredentialTicket(f.t.Context(),
		sqlitegen.CreateCredentialTicketParams{
			ID: id.String(), TicketHash: identity.HashTicket(plaintext),
			ProviderID: providerID, Subject: subject, DisplayName: displayName,
			GuildRolesJson: factsJSON,
			// The 120-second TTL is a CHECK on the row, so a longer-lived ticket cannot be written
			// at all — this fixture computes it rather than choosing it.
			ExpiresAt: int64(now.Add(identity.TicketTTL)),
			CreatedAt: int64(now), UpdatedAt: int64(now),
		})
	require.NoError(f.t, err)
	return plaintext
}

// joinLocal is the ordinary join: a `local` credential, which mints its own subject.
func (f *fixture) joinLocal(code invite.Code, displayName string) (membership.Joined, error) {
	f.t.Helper()
	return f.members.Join(f.t.Context(), membership.JoinRequest{
		Code: string(code), ProviderKey: "local",
		Credential:  identity.Credential{Kind: identity.CredentialNone},
		DisplayName: displayName, ClientName: "test",
		IdempotencyKey: "join-" + displayName,
	})
}

// blockIdentity is the instance operator's decision, written the way the admin route will write
// it. `ck_identity_block_is_attributed` refuses a block with nobody's name on it, so the caller
// names one — an unattributed block is a decision no audit can trace.
func (f *fixture) blockIdentity(id string, by core.MembershipID) {
	f.t.Helper()
	at := int64(f.clock.Now())
	actor := by.String()
	_, err := f.store.Queries().BlockIdentity(f.t.Context(), sqlitegen.BlockIdentityParams{
		BlockedAt: &at, BlockedByMembershipID: &actor, UpdatedAt: at, ID: id,
	})
	require.NoError(f.t, err)
}

// The small helpers the tests share, so a table stays readable.
func circleRequest(name string) circle.CreateRequest {
	return circle.CreateRequest{Name: name, Server: core.Server(schemaenum.ServerBlue)}
}

func providersRequest(keys ...string) circle.SetProvidersRequest {
	out := circle.SetProvidersRequest{AcknowledgeWeakRevocation: true}
	for _, key := range keys {
		out.Providers = append(out.Providers, circle.AcceptedProvider{Key: key})
	}
	return out
}

func gatedProviders(guildID string, roleIDs []string) circle.SetProvidersRequest {
	return circle.SetProvidersRequest{Providers: []circle.AcceptedProvider{{
		Key: "discord", DiscordGuildID: guildID, DiscordRequiredRoleIDs: roleIDs,
	}}}
}

func listRedemptionParams(
	circleID core.CircleID, inviteID core.InviteID,
) sqlitegen.ListInviteRedemptionsParams {
	return sqlitegen.ListInviteRedemptionsParams{
		CircleID: circleID.String(), InviteID: inviteID.String(),
	}
}
