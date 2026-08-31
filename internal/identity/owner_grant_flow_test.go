package identity_test

import (
	"crypto/rand"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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

// These tests drive the browser OAuth flow against the REAL identitysql store over real SQLite,
// and that is the point of them existing beside the fake-store tests above.
//
// The first-run owner code is not an `invite` row — it is a `tod_meta` entry under
// `owner_grant/` — so a fake store keyed on a map of invites answers "found" for it no matter what
// the real lookup does. Every existing owner-grant test redeems through `/join` with a `local`
// provider or a hand-built `credential_ticket`, both of which start AFTER
// `createAuthorizationURL`. The result was a green suite over a first-run flow that could not be
// completed through any OAuth provider at all.
type grantHarness struct {
	service *identity.Service
	queries *sqlitegen.Queries
	clock   *clock.Test
	circle  circle.Circle
}

// newGrantHarness stands up a fresh instance the way an operator does: one enabled Discord
// provider, one circle, and a one-time owner grant minted by the CLI's own code path.
func newGrantHarness(t *testing.T) *grantHarness {
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

	identityStore, err := identitysql.New(db.Queries(), clk, invite.HashCode, invite.GrantByCodeHash)
	require.NoError(t, err)

	doer := &discordDoer{answers: happyDiscordAnswers(t)}
	client, err := newDiscordClient(doer)
	require.NoError(t, err)

	service, err := identity.New(identity.Config{
		Store: identityStore, Clients: &stubClients{discord: client}, Clock: clk, IDs: ids,
		Entropy: rand.Reader, SPAJoinURL: spaJoinURL, CallbackBaseURL: callbackBaseURL,
		Logger: log,
	})
	require.NoError(t, err)

	return &grantHarness{service: service, queries: db.Queries(), clock: clk, circle: view}
}

// ownerGrant mints the code `tod-serve init` prints, through the same function the CLI calls.
func (h *grantHarness) ownerGrant(t *testing.T) invite.Code {
	t.Helper()
	code, err := invite.MintGrant(
		t.Context(), h.queries, rand.Reader, h.circle.ID, h.clock.Now(), invite.DefaultGrantTTL)
	require.NoError(t, err)
	return code
}

// **The regression test.** The first-run owner code must survive a whole browser sign-in: the
// authorization request that starts it and the callback that finishes it.
//
// Before the fix this failed on the very first call — `createAuthorizationURL` resolved the code
// against the `invite` table alone and answered `invite_invalid`, which is what a real instance
// showed as `#error=invite_invalid` on the join page after a successful Discord sign-in.
func TestOwnerGrant_CompletesAWholeBrowserAuthorization(t *testing.T) {
	t.Parallel()

	h := newGrantHarness(t)
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

// A dead owner grant answers exactly what a dead invite answers, and an unknown code answers what
// an unknown code has always answered. Widening the fix into a distinguishable reply would make an
// owner grant findable by a guesser, which is the disclosure internal/invite deliberately closes.
func TestOwnerGrant_DeadOrUnknown_AnswersWhatAnInviteAnswers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// kill mutates the world after the grant is minted, and returns the code to present.
		kill func(t *testing.T, h *grantHarness, code invite.Code) string
		want identity.Code
	}{
		{
			name: "a redeemed grant is exhausted, as a spent invite is",
			kill: func(t *testing.T, h *grantHarness, code invite.Code) string {
				t.Helper()
				_, err := invite.ConsumeGrant(t.Context(), h.queries, code, h.clock.Now())
				require.NoError(t, err)
				return string(code)
			},
			want: identity.CodeInviteExhausted,
		},
		{
			name: "an expired grant is expired, as a lapsed invite is",
			kill: func(_ *testing.T, h *grantHarness, code invite.Code) string {
				h.clock.Advance(invite.DefaultGrantTTL * time.Microsecond)
				return string(code)
			},
			want: identity.CodeInviteExpired,
		},
		{
			name: "a grant whose circle is gone is invalid, not gone",
			kill: func(t *testing.T, h *grantHarness, code invite.Code) string {
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
			kill: func(_ *testing.T, _ *grantHarness, _ invite.Code) string {
				return "TODI-4KQ7M-9XPB2"
			},
			want: identity.CodeInviteInvalid,
		},
		{
			name: "a code that is not a code at all is the same invalid",
			kill: func(_ *testing.T, _ *grantHarness, _ invite.Code) string {
				return "not-a-code"
			},
			want: identity.CodeInviteInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newGrantHarness(t)
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
func TestOwnerGrant_RedeemedMidFlow_IsRefusedAtTheCallback(t *testing.T) {
	t.Parallel()

	h := newGrantHarness(t)
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

func ptr[T any](v T) *T { return &v }
