package discord_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
)

func TestEvaluateGate_EveryOutcome(t *testing.T) {
	t.Parallel()

	member := discord.GuildFacts{guildID: {Member: true, RoleIDs: []string{"raider"}}}
	nonMember := discord.GuildFacts{guildID: {Member: false, RoleIDs: []string{}}}

	tests := []struct {
		name          string
		gate          discord.Gate
		facts         discord.GuildFacts
		scopeDeclined bool
		want          error
	}{
		{
			name:  "no guild gate admits everyone",
			gate:  discord.Gate{},
			facts: nil,
		},
		{
			name:  "in the guild, no roles required",
			gate:  discord.Gate{GuildID: guildID},
			facts: member,
		},
		{
			name:  "in the guild, holds one of the required roles",
			gate:  discord.Gate{GuildID: guildID, RequiredRoleIDs: []string{"officer", "raider"}},
			facts: member,
		},
		{
			name:  "in the guild, holds none of the required roles",
			gate:  discord.Gate{GuildID: guildID, RequiredRoleIDs: []string{"officer"}},
			facts: member,
			want:  discord.ErrGuildRoleRequired,
		},
		{
			name:  "not in the guild",
			gate:  discord.Gate{GuildID: guildID},
			facts: nonMember,
			want:  discord.ErrGuildMembershipRequired,
		},
		{
			name:  "no fact at all rejects rather than skipping",
			gate:  discord.Gate{GuildID: guildID},
			facts: discord.GuildFacts{},
			want:  discord.ErrGuildRoleRequired,
		},
		{
			name:  "a fact about another guild is not a fact about this one",
			gate:  discord.Gate{GuildID: guildID},
			facts: discord.GuildFacts{"other-guild": {Member: true}},
			want:  discord.ErrGuildRoleRequired,
		},
		{
			name:          "a declined scope is a declined scope, not a role failure",
			gate:          discord.Gate{GuildID: guildID, RequiredRoleIDs: []string{"officer"}},
			facts:         discord.GuildFacts{},
			scopeDeclined: true,
			want:          discord.ErrScopeDeclined,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := discord.EvaluateGate(tt.gate, tt.facts, tt.scopeDeclined)
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.want)
		})
	}
}

// The tempting shortcut, pinned so it cannot be reintroduced: reading an absent role list as an
// empty one disables the gate for every user while appearing to enforce it.
func TestEvaluateGate_MissingRoleFacts_Refused(t *testing.T) {
	t.Parallel()

	gate := discord.Gate{GuildID: guildID, RequiredRoleIDs: []string{"officer"}}

	require.ErrorIs(t, discord.EvaluateGate(gate, nil, false), discord.ErrGuildRoleRequired)
	require.ErrorIs(t, discord.EvaluateGate(gate, discord.GuildFacts{}, false), discord.ErrGuildRoleRequired)
}

func TestParseRoleIDs_EmptyColumn_MeansAnyoneInTheGuild(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "[]"} {
		got, err := discord.ParseRoleIDs(raw)
		require.NoError(t, err)
		require.Empty(t, got)
	}

	got, err := discord.ParseRoleIDs(`["a","b"]`)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, got)

	_, err = discord.ParseRoleIDs(`{"a":1}`)
	require.Error(t, err, "an unparseable list must not read as no roles required")
}

func TestFacts_RoundTripThroughTheTicketColumn(t *testing.T) {
	t.Parallel()

	facts := discord.GuildFacts{
		guildID:       {Member: true, RoleIDs: []string{"raider"}},
		"other-guild": {Member: false, RoleIDs: []string{}},
	}

	raw, err := discord.MarshalFacts(facts)
	require.NoError(t, err)

	got, err := discord.ParseFacts(raw)
	require.NoError(t, err)
	require.Equal(t, facts, got)

	// The three-state distinction has to survive the column, or the gate cannot tell "not a
	// member" from "we never asked" after a round trip.
	require.False(t, got["other-guild"].Member)
	_, known := got["never-asked"]
	require.False(t, known)
}

func TestMarshalFacts_NoFacts_IsAnEmptyObject(t *testing.T) {
	t.Parallel()

	raw, err := discord.MarshalFacts(nil)
	require.NoError(t, err)
	require.Equal(t, "{}", raw)
}

// The authorization request asks for every scope the callback then uses, and no more.
func TestAuthorizationURL_GuildGatedCircle_RequestsGuildsMembersRead(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{discord.ScopeIdentify}, discord.Scopes(false))
	require.Equal(t,
		[]string{discord.ScopeIdentify, discord.ScopeGuildsMembersRead},
		discord.Scopes(true))

	c := newClient(t, &stubDoer{})

	gated := c.AuthorizationURL("state-1", "verifier-1", discord.Scopes(true))
	require.Contains(t, gated, "scope=identify+guilds.members.read")
	require.Contains(t, gated, "code_challenge_method=S256")
	require.NotContains(t, gated, "verifier-1", "the verifier stays server-side; only its hash travels")
	require.NotContains(t, gated, "scope=guilds&", "the broader guilds scope is never requested")

	ungated := c.AuthorizationURL("state-1", "verifier-1", discord.Scopes(false))
	require.NotContains(t, ungated, "guilds.members.read")
}

// RFC 7636 appendix B. A hand-rolled S256 challenge that is subtly wrong fails only against a
// real provider, which is the worst place to find out.
func TestPKCEChallenge_MatchesTheRFC7636Vector(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		discord.PKCEChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"))
	require.False(t, strings.Contains(discord.PKCEChallenge("x"), "="), "the challenge is unpadded base64url")
}
