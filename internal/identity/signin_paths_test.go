package identity_test

import (
	"context"
	"crypto/rand"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/identitysql"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// These tests drive sign-in against the REAL identitysql store over real SQLite, and that is the
// point of them existing beside the fake-store tests in flow_test.go.
//
// A `fakeStore` keyed on a map of invites answers "found" for whatever the test put in the map, so
// it agrees with any lookup the adapter performs — including one that resolves the `invite` table
// and nothing else. That is how a first-run owner code, which is a `tod_meta` entry under
// `owner_grant/` and not an invite row at all, could be refused by `createAuthorizationURL` on
// every real instance while the flow suite stayed green.
//
// The fake still earns its place: it is what makes "no store call received the access token"
// assertable. It just cannot answer whether the adapter looks in the right places.
type signInHarness struct {
	service *identity.Service
	store   *countingStore
	queries *sqlitegen.Queries
	clock   *clock.Test
	ids     *core.Generator
	invites *invite.Service
	circle  circle.Circle

	providerID string
}

// countingStore is the real store with a tally of the two invite lookups.
//
// It exists for one assertion that cannot be made any other way: a sign-in carrying NO invite code
// must not resolve an invite at all. "The returned URL looks right" would pass for an
// implementation that resolved something and ignored it.
type countingStore struct {
	identity.Store
	inviteLookups int
}

func (c *countingStore) InviteByCode(ctx context.Context, code string) (identity.Invite, error) {
	c.inviteLookups++
	return c.Store.InviteByCode(ctx, code)
}

func (c *countingStore) InviteByCodeHash(ctx context.Context, hash []byte) (identity.Invite, error) {
	c.inviteLookups++
	return c.Store.InviteByCodeHash(ctx, hash)
}

// newSignInHarness stands up a fresh instance the way an operator does: one enabled Discord
// provider and one circle that accepts it.
func newSignInHarness(t *testing.T) *signInHarness {
	t.Helper()
	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tod.db"), log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Migrate(ctx))

	clk := clock.NewTest(at)
	ids := core.NewGenerator(rand.Reader)

	providerID, err := core.NewID[core.IdentityProvider](ids, at)
	require.NoError(t, err)
	clientID, secret := discordAppID, "operator-client-secret"
	redirect := callbackBaseURL + "/discord"
	_, err = db.Queries().CreateIdentityProvider(ctx, sqlitegen.CreateIdentityProviderParams{
		ID: providerID.String(), Key: "discord", Kind: schemaenum.IdentityProviderKindDiscord,
		DisplayName: "Sign in with Discord", Enabled: 1, VerifiableSubject: 1,
		ClientID: &clientID, ClientSecret: &secret, RedirectUri: &redirect,
		CreatedAt: int64(at), UpdatedAt: int64(at),
	})
	require.NoError(t, err)

	circles, err := circle.New(circle.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	require.NoError(t, err)
	// A new circle auto-accepts every enabled provider with a verifiable subject, so this is the
	// shape `tod-serve init` leaves behind rather than one only a fixture can reach.
	view, err := circles.Create(ctx, circle.CreateRequest{
		Name: "Riot Blue", Server: core.Server(schemaenum.ServerBlue),
	})
	require.NoError(t, err)

	invites, err := invite.New(invite.Config{
		Store: db, Clock: clk, IDs: ids, Entropy: rand.Reader, Log: log,
	})
	require.NoError(t, err)

	real, err := identitysql.New(db.Queries(), clk, invite.HashCode, invite.GrantByCodeHash)
	require.NoError(t, err)
	counting := &countingStore{Store: real}

	doer := &discordDoer{answers: happyDiscordAnswers(t)}
	client, err := newDiscordClient(doer)
	require.NoError(t, err)

	service, err := identity.New(identity.Config{
		Store: counting, Clients: &stubClients{discord: client}, Clock: clk, IDs: ids,
		Entropy: rand.Reader, SPAJoinURL: spaJoinURL, CallbackBaseURL: callbackBaseURL,
		Logger: log,
	})
	require.NoError(t, err)

	return &signInHarness{
		service: service, store: counting, queries: db.Queries(), clock: clk, ids: ids,
		invites: invites, circle: view, providerID: providerID.String(),
	}
}

// ownerGrant mints the code `tod-serve init` prints, through the same function the CLI calls.
func (h *signInHarness) ownerGrant(t *testing.T) invite.Code {
	t.Helper()
	code, err := invite.MintGrant(
		t.Context(), h.queries, rand.Reader, h.circle.ID, h.clock.Now(), invite.DefaultGrantTTL)
	require.NoError(t, err)
	return code
}

// ordinaryInvite mints a `member` invite the way an officer's console does. `invite` carries
// `CHECK (role <> 'owner')`, so this is the shape every member after the first arrives on.
func (h *signInHarness) ordinaryInvite(t *testing.T) invite.Code {
	t.Helper()
	minted, err := h.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: h.circle.ID, Actor: h.seedOwner(t), Role: authz.RoleMember,
	})
	require.NoError(t, err)
	return minted.Code
}

// seedOwner writes the identity and membership an invite's `created_by_membership_id` must name.
// `ck_membership_human_has_an_identity` is why the identity row is not optional here.
func (h *signInHarness) seedOwner(t *testing.T) core.MembershipID {
	t.Helper()
	identityID, err := core.NewID[core.Identity](h.ids, h.clock.Now())
	require.NoError(t, err)
	membershipID, err := core.NewID[core.Membership](h.ids, h.clock.Now())
	require.NoError(t, err)

	_, err = h.queries.CreateIdentity(t.Context(), sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: h.providerID, Subject: "owner-subject",
		DisplayName: "Owner", CreatedAt: int64(at), UpdatedAt: int64(at),
	})
	require.NoError(t, err)

	id := identityID.String()
	_, err = h.queries.CreateMembership(t.Context(), sqlitegen.CreateMembershipParams{
		ID: membershipID.String(), CircleID: h.circle.ID.String(), IdentityID: &id, Kind: "human",
		DisplayName: "Owner", DisplayNameNorm: "owner", Role: string(authz.RoleOwner),
		JoinedAt: int64(at), CreatedAt: int64(at), UpdatedAt: int64(at),
	})
	require.NoError(t, err)
	return membershipID
}

// seedMemberIdentity writes the Discord identity and membership an EXISTING member re-authenticates
// as. The subject is the one the stub transport reports, so the callback resolves this row.
func (h *signInHarness) seedMemberIdentity(t *testing.T) {
	t.Helper()
	identityID, err := core.NewID[core.Identity](h.ids, h.clock.Now())
	require.NoError(t, err)
	membershipID, err := core.NewID[core.Membership](h.ids, h.clock.Now())
	require.NoError(t, err)

	_, err = h.queries.CreateIdentity(t.Context(), sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: h.providerID, Subject: discordSubject,
		DisplayName: "Tankguy", CreatedAt: int64(at), UpdatedAt: int64(at),
	})
	require.NoError(t, err)

	id := identityID.String()
	_, err = h.queries.CreateMembership(t.Context(), sqlitegen.CreateMembershipParams{
		ID: membershipID.String(), CircleID: h.circle.ID.String(), IdentityID: &id, Kind: "human",
		DisplayName: "Tankguy", DisplayNameNorm: "tankguy", Role: string(authz.RoleMember),
		JoinedAt: int64(at), CreatedAt: int64(at), UpdatedAt: int64(at),
	})
	require.NoError(t, err)
}

// gateOnAGuild makes the circle require guild membership through this provider.
func (h *signInHarness) gateOnAGuild(t *testing.T) {
	t.Helper()
	gate := guildID
	_, err := h.queries.PutCircleProvider(t.Context(), sqlitegen.PutCircleProviderParams{
		CircleID: h.circle.ID.String(), ProviderID: h.providerID, DiscordGuildID: &gate,
		DiscordRequiredRoleIdsJson: `["raider"]`, CreatedAt: int64(at), UpdatedAt: int64(at),
	})
	require.NoError(t, err)
}

// browserSignIn runs the whole browser flow and returns the callback's Location.
func (h *signInHarness) browserSignIn(t *testing.T, code string) string {
	t.Helper()
	authorization, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "discord", InviteCode: code})
	require.NoError(t, err)

	callback, err := h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
		ProviderKey: "discord", State: authorization.State, Code: "authorization-code",
	})
	require.NoError(t, err)
	return callback.Location
}

// **The named test for the whole change.** Every way somebody signs in to this instance, driven
// end to end against the real store.
//
// Making the owner grant redeemable is necessary and not sufficient: an instance where the first
// owner can get in and nobody else can fails later and for more people. The four paths are
// enumerated rather than sampled, because "the invite paths still work" is exactly the kind of
// class-level claim that hid this bug in the first place.
func TestSignInPaths_AgainstTheRealStore_EveryPathStillCompletes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// sign performs one whole sign-in and returns the credential the caller ends up holding:
		// a callback Location for the three browser paths, the verified subject for bearer_token.
		sign func(t *testing.T, h *signInHarness) string
		// want is a substring the result must contain.
		want string
	}{
		{
			// The broken one. A `tod_meta` row, not an invite.
			name: "the first-run owner grant, through the browser",
			sign: func(t *testing.T, h *signInHarness) string {
				return h.browserSignIn(t, string(h.ownerGrant(t)))
			},
			want: "#ticket=",
		},
		{
			// Every member after the first. An `invite` row, and the path the fallback must not
			// have disturbed.
			name: "an ordinary invite, through the browser",
			sign: func(t *testing.T, h *signInHarness) string {
				return h.browserSignIn(t, string(h.ordinaryInvite(t)))
			},
			want: "#ticket=",
		},
		{
			// The remembered-circle path in the console: no code at all. `req.InviteCode == ""`
			// skips the whole invite block, so this path must be untouched — see the test below,
			// which asserts the skip rather than inferring it from a working redirect.
			name: "an existing member re-authenticating with no invite code",
			sign: func(t *testing.T, h *signInHarness) string {
				h.seedMemberIdentity(t)
				return h.browserSignIn(t, "")
			},
			want: "#ticket=",
		},
		{
			// The non-browser credential: a client with no browser presents a Discord access
			// token to /join or /sessions directly. It does NOT go through
			// createAuthorizationURL or completeAuthorization — see the comment on
			// TestSignInPaths_TheBearerTokenPath_ReachesNoInviteLookup — so what has to be shown
			// here is that it still verifies against the same real store.
			name: "the non-browser bearer_token credential",
			sign: func(t *testing.T, h *signInHarness) string {
				provider, err := h.store.ProviderByKey(t.Context(), "discord")
				require.NoError(t, err)
				verified, err := h.service.Verify(t.Context(), identity.VerifyRequest{
					Provider: provider,
					Credential: identity.Credential{
						Kind: identity.CredentialBearerToken, Token: core.Secret("a-token"),
					},
					GuildIDs: []string{guildID},
				})
				require.NoError(t, err)
				return verified.Subject
			},
			want: discordSubject,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newSignInHarness(t)
			require.Contains(t, tt.sign(t, h), tt.want)
		})
	}
}

// A sign-in with no invite code resolves NO invite, and that is asserted by counting rather than
// inferred from a redirect that looked right.
//
// `req.InviteCode == ""` skips the whole invite block: there is no circle to resolve, and
// resolving one from anything else would be the circle-existence oracle canonical §7 closes. The
// fallback added for owner grants lives BELOW that branch, so it cannot reach this path — and this
// is the assertion that keeps it that way.
func TestSignInPaths_NoInviteCode_ResolvesNoInviteAndRecordsNoCircle(t *testing.T) {
	t.Parallel()

	h := newSignInHarness(t)
	h.seedMemberIdentity(t)

	authorization, err := h.service.CreateAuthorizationURL(t.Context(),
		identity.AuthorizationRequest{ProviderKey: "discord"})
	require.NoError(t, err)

	flow, err := h.queries.GetAuthFlowByState(t.Context(), authorization.State)
	require.NoError(t, err)
	require.Nil(t, flow.CircleID, "a codeless sign-in recorded a circle it was never given")
	require.Empty(t, flow.InviteCodeHash)

	callback, err := h.service.CompleteAuthorization(t.Context(), identity.CallbackRequest{
		ProviderKey: "discord", State: authorization.State, Code: "authorization-code",
	})
	require.NoError(t, err)
	require.Contains(t, callback.Location, "#ticket=")

	require.Zero(t, h.store.inviteLookups,
		"a sign-in carrying no code asked the store to resolve one %d times",
		h.store.inviteLookups)
}

// The `bearer_token` path reaches no invite lookup either, which is why the fallback cannot have
// changed it.
//
// Worth stating because it was reported the other way round: `flow.go`'s second and third
// `InviteByCodeHash` calls are in `completeAuthorization` and in `guildsToAsk`, and `guildsToAsk`
// has exactly one caller — `exchangeAndRead`, inside the browser callback. `Service.Verify`
// dispatches `bearer_token` to `verifyDiscordBearer` and never enters either. The guild set a
// bearer caller gets facts for is passed IN by `/join`, which resolved the code through
// internal/invite.
func TestSignInPaths_TheBearerTokenPath_ReachesNoInviteLookup(t *testing.T) {
	t.Parallel()

	h := newSignInHarness(t)
	provider, err := h.store.ProviderByKey(t.Context(), "discord")
	require.NoError(t, err)

	_, err = h.service.Verify(t.Context(), identity.VerifyRequest{
		Provider: provider,
		Credential: identity.Credential{
			Kind: identity.CredentialBearerToken, Token: core.Secret("a-token"),
		},
		GuildIDs: []string{guildID},
	})
	require.NoError(t, err)
	require.Zero(t, h.store.inviteLookups)
}

// **The regression test.** The first-run owner code must survive a whole browser sign-in: the
// authorization request that starts it and the callback that finishes it.
//
// Before the fix this failed on the very first call — `createAuthorizationURL` resolved the code
// against the `invite` table alone and answered `invite_invalid`, which is what a real instance
// showed as `#error=invite_invalid` on the join page after a successful Discord sign-in.
func TestOwnerGrant_CompletesAWholeBrowserAuthorization(t *testing.T) {
	t.Parallel()

	h := newSignInHarness(t)
	code := h.ownerGrant(t)
	ctx := t.Context()

	authorization, err := h.service.CreateAuthorizationURL(ctx, identity.AuthorizationRequest{
		ProviderKey: "discord", InviteCode: string(code),
	})
	require.NoError(t, err, "the first-run owner code was refused before the browser ever left")
	require.NotEmpty(t, authorization.URL)

	// The flow row carries the grant's circle, because that is what selects the scopes and the
	// guild to ask about. A flow that resolved the code but recorded no circle would complete and
	// then hand `/join` nothing to gate on.
	flow, err := h.queries.GetAuthFlowByState(ctx, authorization.State)
	require.NoError(t, err)
	require.NotNil(t, flow.CircleID)
	require.Equal(t, h.circle.ID.String(), *flow.CircleID)
	require.Equal(t, invite.Hash(code), flow.InviteCodeHash)

	callback, err := h.service.CompleteAuthorization(ctx, identity.CallbackRequest{
		ProviderKey: "discord", State: authorization.State, Code: "authorization-code",
	})
	require.NoError(t, err, "the callback refused the owner code it had already accepted")
	require.Contains(t, callback.Location, "#ticket=",
		"a completed first-run authorization must hand the browser a ticket")
}

// A DEAD ordinary invite keeps its specific reason. `invite_expired`, `invite_revoked` and
// `invite_exhausted` send a person to three different places, and collapsing them into
// `invite_invalid` would be a regression the owner-grant fallback could plausibly cause: the
// fallback fires when the invite lookup finds NO ROW, and a dead invite is a row.
func TestSignInPaths_ADeadOrdinaryInvite_KeepsItsSpecificReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kill func(t *testing.T, h *signInHarness, id core.InviteID)
		want identity.Code
	}{
		{
			name: "expired",
			kill: func(_ *testing.T, h *signInHarness, _ core.InviteID) {
				h.clock.Advance(48 * time.Hour)
			},
			want: identity.CodeInviteExpired,
		},
		{
			name: "revoked",
			kill: func(t *testing.T, h *signInHarness, id core.InviteID) {
				revokedAt := int64(h.clock.Now())
				_, err := h.queries.RevokeInvite(t.Context(), sqlitegen.RevokeInviteParams{
					RevokedAt: &revokedAt, UpdatedAt: revokedAt,
					CircleID: h.circle.ID.String(), ID: id.String(),
				})
				require.NoError(t, err)
			},
			want: identity.CodeInviteRevoked,
		},
		{
			name: "exhausted",
			kill: func(t *testing.T, h *signInHarness, id core.InviteID) {
				for range 5 {
					_, err := h.queries.ConsumeInvite(t.Context(), sqlitegen.ConsumeInviteParams{
						UpdatedAt: int64(h.clock.Now()), CircleID: h.circle.ID.String(),
						ID: id.String(), Now: int64(h.clock.Now()),
					})
					require.NoError(t, err)
				}
			},
			want: identity.CodeInviteExhausted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newSignInHarness(t)
			// An explicit TTL rather than the default, so the expired case advances past a span
			// this test states rather than one it inherited.
			minted, err := h.invites.Create(t.Context(), invite.CreateRequest{
				CircleID: h.circle.ID, Actor: h.seedOwner(t), Role: authz.RoleMember,
				MaxUses: 5, TTL: time.Hour,
			})
			require.NoError(t, err)
			tt.kill(t, h, minted.ID)

			_, err = h.service.CreateAuthorizationURL(t.Context(), identity.AuthorizationRequest{
				ProviderKey: "discord", InviteCode: string(minted.Code),
			})
			require.Error(t, err)
			got, ok := identity.CodeOf(err)
			require.True(t, ok, "an uncoded refusal renders 500: %v", err)
			require.Equal(t, tt.want, got,
				"a dead invite lost its reason and became %q", got)
		})
	}
}

// A dead owner grant answers exactly what a dead invite answers, and an unknown code answers what
// an unknown code has always answered. Widening the fix into a distinguishable reply would make an
// owner grant findable by a guesser, which is the disclosure internal/invite deliberately closes.
func TestOwnerGrant_DeadOrUnknown_AnswersWhatAnInviteAnswers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// kill mutates the world after the grant is minted, and returns the code to present.
		kill func(t *testing.T, h *signInHarness, code invite.Code) string
		want identity.Code
	}{
		{
			name: "a redeemed grant is exhausted, as a spent invite is",
			kill: func(t *testing.T, h *signInHarness, code invite.Code) string {
				t.Helper()
				_, err := invite.ConsumeGrant(t.Context(), h.queries, code, h.clock.Now())
				require.NoError(t, err)
				return string(code)
			},
			want: identity.CodeInviteExhausted,
		},
		{
			name: "an expired grant is expired, as a lapsed invite is",
			kill: func(_ *testing.T, h *signInHarness, code invite.Code) string {
				h.clock.Advance(invite.DefaultGrantTTL * time.Microsecond)
				return string(code)
			},
			want: identity.CodeInviteExpired,
		},
		{
			name: "a grant whose circle is gone is invalid, not gone",
			kill: func(t *testing.T, h *signInHarness, code invite.Code) string {
				t.Helper()
				_, err := h.queries.SoftDeleteCircle(t.Context(), sqlitegen.SoftDeleteCircleParams{
					DeletedAt: ptr(int64(h.clock.Now())), UpdatedAt: int64(h.clock.Now()),
					CircleID: h.circle.ID.String(),
				})
				require.NoError(t, err)
				return string(code)
			},
			want: identity.CodeInviteInvalid,
		},
		{
			name: "a code nobody ever issued is invalid",
			kill: func(_ *testing.T, _ *signInHarness, _ invite.Code) string {
				return "TODI-4KQ7M-9XPB2"
			},
			want: identity.CodeInviteInvalid,
		},
		{
			name: "a code that is not a code at all is the same invalid",
			kill: func(_ *testing.T, _ *signInHarness, _ invite.Code) string {
				return "not-a-code"
			},
			want: identity.CodeInviteInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newSignInHarness(t)
			presented := tt.kill(t, h, h.ownerGrant(t))

			_, err := h.service.CreateAuthorizationURL(t.Context(), identity.AuthorizationRequest{
				ProviderKey: "discord", InviteCode: presented,
			})
			require.Error(t, err)
			got, ok := identity.CodeOf(err)
			require.True(t, ok, "an uncoded refusal renders 500: %v", err)
			require.Equal(t, tt.want, got)
		})
	}
}

// A grant that dies between the authorization request and the callback is refused at the callback,
// exactly as an invite that dies there is. Without this the completion side would resolve only the
// `invite` table and answer "this invite no longer exists" for a grant that is perfectly alive.
//
// It is also the browser-level statement of the grant's single use: two flows started from one
// printed code, the first redeemed, the second refused.
func TestOwnerGrant_RedeemedMidFlow_IsRefusedAtTheCallback(t *testing.T) {
	t.Parallel()

	h := newSignInHarness(t)
	code := h.ownerGrant(t)
	ctx := t.Context()

	authorization, err := h.service.CreateAuthorizationURL(ctx, identity.AuthorizationRequest{
		ProviderKey: "discord", InviteCode: string(code),
	})
	require.NoError(t, err)

	// Somebody else finishes first with the same printed code. One grant, one owner.
	_, err = invite.ConsumeGrant(ctx, h.queries, code, h.clock.Now())
	require.NoError(t, err)

	callback, err := h.service.CompleteAuthorization(ctx, identity.CallbackRequest{
		ProviderKey: "discord", State: authorization.State, Code: "authorization-code",
	})
	require.Error(t, err)
	require.Equal(t, identity.CodeInviteExhausted, callback.Code)
}

// The guild gate applies to a grant-carrying flow exactly as it applies to an invite-carrying one.
//
// Two halves, and both matter. A circle that GATES makes the authorization request ask for
// `guilds.members.read`, so the callback has a member object to evaluate — a grant that skipped
// that would make the first owner the one person the gate never checked. A circle that does not
// ACCEPT the provider refuses before any row is written.
func TestOwnerGrant_TheGuildGate_AppliesToAGrantCarryingFlow(t *testing.T) {
	t.Parallel()

	t.Run("a gated circle still asks for the guild scope", func(t *testing.T) {
		t.Parallel()
		h := newSignInHarness(t)
		h.gateOnAGuild(t)

		got, err := h.service.CreateAuthorizationURL(t.Context(), identity.AuthorizationRequest{
			ProviderKey: "discord", InviteCode: string(h.ownerGrant(t)),
		})
		require.NoError(t, err)
		require.Contains(t, got.URL, "guilds.members.read",
			"a grant-carrying flow into a gated circle asked for no member object")
	})

	t.Run("a circle that does not accept the provider is refused", func(t *testing.T) {
		t.Parallel()
		h := newSignInHarness(t)
		code := h.ownerGrant(t)
		_, err := h.queries.DeleteCircleProvider(t.Context(), sqlitegen.DeleteCircleProviderParams{
			CircleID: h.circle.ID.String(), ProviderID: h.providerID,
		})
		require.NoError(t, err)

		_, err = h.service.CreateAuthorizationURL(t.Context(), identity.AuthorizationRequest{
			ProviderKey: "discord", InviteCode: string(code),
		})
		require.Error(t, err)
		got, ok := identity.CodeOf(err)
		require.True(t, ok, "an uncoded refusal renders 500: %v", err)
		require.Equal(t, identity.CodeProviderNotAccepted, got)
	})
}

func ptr[T any](v T) *T { return &v }
