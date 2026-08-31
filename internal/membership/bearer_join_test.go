package membership_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity/oidc"
	"github.com/prokopto-dev/tod-serve/internal/identity/outbound"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
)

// The `bearer_token` join is the NON-BROWSER sign-in: a client with no browser presents a Discord
// access token straight to `/join`, and this instance goes and checks it.
//
// It had no end-to-end test, and the reason is visible in the fixture: it is the one credential
// that needs a transport, so every other join shape could be driven without one and this one was
// left to `internal/identity`'s unit tests of the audience check. Those cover whether a token is
// accepted; nothing covered whether a person holding one ends up with a membership.
//
// It matters here because it resolves its code through internal/invite rather than through
// identitysql — `Service.Verify` dispatches it to `verifyDiscordBearer`, which never enters
// `completeAuthorization` or `guildsToAsk`. That is a claim about the call graph, and this is what
// turns it into a claim about the outcome.

// bearerAppID is what the stub reports as the token's audience. It is the client id the fixture's
// `discord` provider row carries — `key + "-client-id"` — because the audience check compares the
// two, and this file must not be the source of both sides of that comparison.
const bearerAppID = "discord-client-id"

// bearerDoer answers Discord's API from a table keyed on a URL suffix. The subject is settable so
// two people can join one circle.
type bearerDoer struct {
	t       *testing.T
	subject string
}

func (d *bearerDoer) Do(
	_ context.Context, _, rawURL string, _ http.Header, _ []byte,
) (*outbound.Response, error) {
	d.t.Helper()
	var body map[string]any
	switch {
	case strings.HasSuffix(rawURL, "/oauth2/@me"):
		body = map[string]any{
			"application": map[string]any{"id": bearerAppID},
			"scopes":      []string{discord.ScopeIdentify, discord.ScopeGuildsMembersRead},
		}
	case strings.HasSuffix(rawURL, "/users/@me"):
		body = map[string]any{"id": d.subject, "username": "bearer-" + d.subject}
	case strings.HasSuffix(rawURL, "/member"):
		body = map[string]any{"roles": []string{"raider"}}
	default:
		return &outbound.Response{
			Status: http.StatusNotFound, Header: http.Header{}, Body: []byte("{}"),
		}, nil
	}
	raw, err := json.Marshal(body)
	require.NoError(d.t, err)
	return &outbound.Response{Status: http.StatusOK, Header: http.Header{}, Body: raw}, nil
}

// bearerClients hands out a real discord.Client over the stub transport, so what runs is the real
// dispatch and the real audience check.
//
// It builds the client from the PROVIDER ROW, exactly as [identity.GuardedClients] does, rather
// than from a constant this file also feeds the stub. A stub that did the latter would compare the
// audience against itself and pass for any application id at all — which it did, until the
// mutation run said so.
type bearerClients struct{ doer *bearerDoer }

func (b *bearerClients) Discord(p identity.Provider) (*discord.Client, error) {
	return discord.New(b.doer, discord.Config{
		ClientID: p.ClientID, ClientSecret: p.ClientSecret, RedirectURI: p.RedirectURI,
	})
}

func (b *bearerClients) OIDC(identity.Provider) (*oidc.Verifier, error) {
	return nil, errors.New("this fixture has no OIDC verifier")
}

// newBearerFixture wires the join path with the Discord transport stubbed.
func newBearerFixture(t *testing.T, subject string) (*fixture, *bearerDoer) {
	t.Helper()
	doer := &bearerDoer{t: t, subject: subject}
	return newFixtureWithClients(t, &bearerClients{doer: doer}), doer
}

// The non-browser path, end to end, on both code shapes: the first-run owner grant and an ordinary
// invite. Both must produce a membership and a PAT.
func TestJoin_ABearerToken_AdmitsAnOwnerGrantAndAnOrdinaryInvite(t *testing.T) {
	t.Parallel()

	f, doer := newBearerFixture(t, "444444444444444444")
	view, _ := f.discordCircle("Riot Blue", "", nil)

	owner, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(f.ownerGrant(view)), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind: identity.CredentialBearerToken, Token: core.Secret("an-access-token"),
		},
		DisplayName: "Tankguy", ClientName: "tod-cli", IdempotencyKey: "owner",
	})
	require.NoError(t, err, "the non-browser path could not redeem a first-run owner grant")
	require.True(t, owner.Created)
	require.Equal(t, string(authz.RoleOwner), owner.Membership.Role)
	require.NotEmpty(t, owner.Token.Secret)

	minted, err := f.invites.Create(t.Context(), invite.CreateRequest{
		CircleID: view.ID, Actor: owner.Membership.ID, Role: authz.RoleMember,
	})
	require.NoError(t, err)

	// A second person, so the membership is a new row rather than the owner's own.
	doer.subject = "555555555555555555"
	member, err := f.members.Join(t.Context(), membership.JoinRequest{
		Code: string(minted.Code), ProviderKey: "discord",
		Credential: identity.Credential{
			Kind: identity.CredentialBearerToken, Token: core.Secret("another-access-token"),
		},
		DisplayName: "Sneakco", ClientName: "tod-cli", IdempotencyKey: "member",
	})
	require.NoError(t, err, "the non-browser path could not redeem an ordinary invite")
	require.True(t, member.Created)
	require.Equal(t, string(authz.RoleMember), member.Membership.Role)
	require.NotEqual(t, owner.Membership.ID, member.Membership.ID)
}
