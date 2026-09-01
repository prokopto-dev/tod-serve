package discord_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/discord"
)

// The channel, guild and Discord account every case below uses. Constants so a failure names the
// same thing every time.
const (
	boundChannel = "111111111111111111"
	otherChannel = "222222222222222222"
	guild        = "999999999999999999"
	otherGuild   = "888888888888888888"
	aliceSubject = "555555555555555555"
	bobSubject   = "444444444444444444"
)

// command builds the interaction Discord sends for `/tod <name> ...`.
func command(name, channelID, guildID, subject string, args map[string]any) discord.Interaction {
	options := make([]discord.CommandOption, 0, len(args))
	for k, v := range args {
		raw, _ := json.Marshal(v)
		options = append(options, discord.CommandOption{Name: k, Value: raw})
	}
	in := discord.Interaction{
		ID: "interaction-1", Type: discord.TypeApplicationCommand,
		GuildID: guildID, ChannelID: channelID,
		Data: &discord.CommandData{
			Name: discord.RootCommand,
			Options: []discord.CommandOption{{
				Name: name, Type: discord.OptionTypeSubCommand, Options: options,
			}},
		},
	}
	in.Member = &struct {
		User *discord.InteractionUser `json:"user"`
	}{User: &discord.InteractionUser{ID: subject, Username: "someone"}}
	return in
}

func ephemeral(t *testing.T, r discord.InteractionReply) string {
	t.Helper()
	require.Equal(t, discord.ResponseChannelMessage, r.Type)
	require.NotNil(t, r.Data)
	require.Equal(t, discord.FlagEphemeral, r.Data.Flags,
		"a reply that is not explicitly, doubly permitted to be visible must be ephemeral")
	return r.Data.Content
}

// Every subcommand in the catalogue reaches a handler.
//
// The dispatcher's `run` has a `default` branch that returns an error, and this is what stops that
// branch from being reachable: a command added to [discord.Commands] and not to the switch would
// be advertised to every guild, registered by every operator, and answer with an internal error.
func TestCommands_EverySubcommand_IsDispatched(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, false, owner)
	f.seedTarget("Vulak`Aerr")

	require.NotEmpty(t, discord.Commands(), "the command catalogue is empty; the filter is wrong")
	for _, c := range discord.Commands() {
		t.Run(c.Name, func(t *testing.T) {
			args := map[string]any{}
			for _, o := range c.Options {
				if o.Required {
					args[o.Name] = "Vulak`Aerr"
				}
			}
			reply, err := f.commander.Dispatch(
				t.Context(), command(c.Name, boundChannel, guild, aliceSubject, args), fixtureNow)
			require.NoError(t, err, "%s reached no handler", c.Name)
			content := ephemeral(t, reply)
			require.NotContains(t, content, "is not a command this instance answers",
				"%s is in the catalogue and the dispatcher does not know it", c.Name)
		})
	}
}

// A `PING` is answered with a `PONG` and nothing else. It is how Discord validates the endpoint
// URL, so it must succeed before any binding, any provider and any membership exists.
func TestDispatch_APing_IsAnsweredWithAPong(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	reply, err := f.commander.Dispatch(t.Context(), discord.Interaction{Type: discord.TypePing}, fixtureNow)
	require.NoError(t, err)
	require.Equal(t, discord.ResponsePong, reply.Type)
	require.Nil(t, reply.Data, "a PONG carries no message")
	require.Equal(t, fixtureNow, reply.AsOf)
}

// **Rule 3 and rule 4, and the reason the switch is two halves.** An invoker asking for a visible
// reply in a channel whose officer has not enabled one gets an ephemeral answer — and is TOLD, so
// a filter that dropped their request counts it somewhere visible.
func TestDispatch_AVisibleRequest_InAnUnpermittedChannel_StaysEphemeral(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, false, owner)

	reply, err := f.commander.Dispatch(t.Context(), command(
		discord.CommandBoard, boundChannel, guild, aliceSubject,
		map[string]any{discord.OptionVisible: true}), fixtureNow)
	require.NoError(t, err)
	content := ephemeral(t, reply)
	require.Contains(t, content, "visible replies are not enabled for this channel")
}

// The other half of the same switch: with the binding's opt-in AND the invoker's request, the
// reply is visible. Without this the test above would pass against a bot that could never post
// anything at all, which is a green tick over a feature that does not work.
func TestDispatch_AVisibleRequest_InAPermittedChannel_IsVisible(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, true, owner)

	reply, err := f.commander.Dispatch(t.Context(), command(
		discord.CommandBoard, boundChannel, guild, aliceSubject,
		map[string]any{discord.OptionVisible: true}), fixtureNow)
	require.NoError(t, err)
	require.NotNil(t, reply.Data)
	require.Zero(t, reply.Data.Flags, "the binding allows it and the invoker asked for it")
}

// A permitted channel with no request is still ephemeral. Ephemeral is the DEFAULT and not a
// fallback: enabling visible replies is permission to ask, never an instruction to publish.
func TestDispatch_APermittedChannel_WithoutAsking_IsStillEphemeral(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, true, owner)

	reply, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandBoard, boundChannel, guild, aliceSubject, nil), fixtureNow)
	require.NoError(t, err)
	ephemeral(t, reply)
}

// A command whose catalogue entry says it may never be visible is never visible, however the
// channel is configured and however the invoker asks. `/tod report` is the one: the reply names
// the target and the time at its freshest, and the person running it is in a raid rather than in a
// position to think about who is reading.
func TestDispatch_ACommandThatIsNeverVisible_IsNeverVisible(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, true, owner)
	f.seedTarget("Vulak`Aerr")

	for _, c := range discord.Commands() {
		if c.Visible {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			args := map[string]any{discord.OptionVisible: true}
			for _, o := range c.Options {
				if o.Required {
					args[o.Name] = "Vulak`Aerr"
				}
			}
			reply, err := f.commander.Dispatch(
				t.Context(), command(c.Name, boundChannel, guild, aliceSubject, args), fixtureNow)
			require.NoError(t, err)
			ephemeral(t, reply)
		})
	}
}

// **Rule 2 and rule 5 together.** A Discord account in the guild but not in the circle is answered
// as a stranger, and the answer names no circle: guild membership is not circle membership, and a
// refusal that admitted the circle existed would confirm it to somebody who has never been
// admitted.
func TestDispatch_AGuildMemberWhoIsNotInTheCircle_IsAnsweredAsAStranger(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Ancient Blood", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, true, owner)

	reply, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandBoard, boundChannel, guild, bobSubject, nil), fixtureNow)
	require.NoError(t, err)
	content := ephemeral(t, reply)
	require.Contains(t, content, "not a member of the circle")
	require.NotContains(t, content, "Ancient Blood",
		"the refusal named the circle, which confirms to a stranger that it exists")
}

// A revoked membership is answered the same way, on the very NEXT command: the membership is read
// live rather than carried on a credential, so revocation takes effect immediately.
func TestDispatch_ARevokedMember_IsAnsweredAsAStranger(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	member := f.seedMember(mine, bobSubject, "Bob", string(authz.RoleMember))
	f.bind(mine, boundChannel, guild, false, owner)

	before, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandBoard, boundChannel, guild, bobSubject, nil), fixtureNow)
	require.NoError(t, err)
	require.NotContains(t, ephemeral(t, before), "not a member of the circle")

	f.revoke(mine, member, owner)

	after, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandBoard, boundChannel, guild, bobSubject, nil), fixtureNow)
	require.NoError(t, err)
	require.Contains(t, ephemeral(t, after), "not a member of the circle")
}

// **The whole of rule 2.** The permission checked is the INVOKER's, read from their membership
// role, and not something the bot holds. An observer cannot report; the same person as an officer
// can.
func TestDispatch_ThePermission_IsTheInvokersAndNotTheBots(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, false, owner)
	f.seedTarget("Vulak`Aerr")
	f.seedMember(mine, bobSubject, "Bob", string(authz.RoleObserver))

	reply, err := f.commander.Dispatch(t.Context(), command(
		discord.CommandReport, boundChannel, guild, bobSubject,
		map[string]any{discord.OptionTarget: "Vulak`Aerr"}), fixtureNow)
	require.NoError(t, err)
	require.Contains(t, ephemeral(t, reply), string(authz.PermissionTodReport),
		"an observer reported a kill; the bot's own access was spent instead of theirs")

	// And the owner, whose role does hold it, gets through — so the refusal above is about the
	// role rather than about the command being broken.
	reply, err = f.commander.Dispatch(t.Context(), command(
		discord.CommandReport, boundChannel, guild, aliceSubject,
		map[string]any{discord.OptionTarget: "Vulak`Aerr"}), fixtureNow)
	require.NoError(t, err)
	require.Contains(t, ephemeral(t, reply), "Recorded")
}

// **Rule 1's second half.** In a channel nobody has bound there is no resolve: the bot offers the
// invoker the circles they are actually a member of and refuses to pick one.
func TestDispatch_AnUnboundChannel_OffersTheInvokersOwnCirclesAndPicksNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Ancient Blood", "blue")
	f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))

	reply, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandBoard, otherChannel, guild, aliceSubject, nil), fixtureNow)
	require.NoError(t, err)
	content := ephemeral(t, reply)
	require.Contains(t, content, "not bound to a circle")
	require.Contains(t, content, "Ancient Blood", "the invoker is offered their own circles")
	require.NotContains(t, content, "Board", "an unbound channel resolved a circle anyway")
}

// The guild is a second fact the binding has to agree with. A channel id lifted out of one guild
// and posted from another does not resolve — the signature proves who SENT the payload, not that
// the channel id in it means what the binding says.
func TestDispatch_AChannelIdFromAnotherGuild_DoesNotResolve(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, true, owner)

	reply, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandBoard, boundChannel, otherGuild, aliceSubject, nil), fixtureNow)
	require.NoError(t, err)
	require.Contains(t, ephemeral(t, reply), "bound in a different Discord server")
}

// A tombstoned circle keeps its bindings — nothing deletes them — so the resolve has to see that
// the circle is gone rather than answer for it.
func TestDispatch_ATombstonedCircle_ResolvesToNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, true, owner)
	f.tombstone(mine)

	reply, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandBoard, boundChannel, guild, aliceSubject, nil), fixtureNow)
	require.NoError(t, err)
	require.Contains(t, ephemeral(t, reply), "not bound to a circle")
}

// `/tod circles` names the channel's circle only when the invoker is IN it. Naming it otherwise
// would tell a guild member that a circle they have never been admitted to exists.
func TestDispatch_Circles_NamesTheBoundCircleOnlyToItsMembers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Ancient Blood", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, false, owner)

	// A stranger to the circle, who is a member of a different one.
	other := f.seedCircle("Green Sun", "green")
	f.seedMember(other, bobSubject, "Bob", string(authz.RoleMember))

	reply, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandCircles, boundChannel, guild, bobSubject, nil), fixtureNow)
	require.NoError(t, err)
	content := ephemeral(t, reply)
	require.NotContains(t, content, "Ancient Blood")
	require.Contains(t, content, "bound to a circle you are not a member of")
	require.Contains(t, content, "Green Sun", "their own circles are theirs to be told about")
}

// A subcommand this binary does not know is refused rather than defaulted. Defaulting would make
// the most privileged of the four reachable by sending the least specific payload.
func TestDispatch_AnUnknownSubcommand_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, true, owner)

	reply, err := f.commander.Dispatch(
		t.Context(), command("nuke", boundChannel, guild, aliceSubject, nil), fixtureNow)
	require.NoError(t, err)
	require.Contains(t, ephemeral(t, reply), "not a command this instance answers")
}

// An interaction with no subcommand at all — the bare `/tod` — resolves nothing.
func TestDispatch_ACommandWithNoSubcommand_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	in := discord.Interaction{
		ID: "x", Type: discord.TypeApplicationCommand, GuildID: guild, ChannelID: boundChannel,
		Data: &discord.CommandData{Name: discord.RootCommand},
	}
	reply, err := f.commander.Dispatch(t.Context(), in, fixtureNow)
	require.NoError(t, err)
	require.Contains(t, ephemeral(t, reply), "not one this application registered")
}

// The command registration is generated from the same catalogue the dispatcher switches on, so an
// operator cannot register a command this binary does not answer by editing one of two lists.
func TestCommandRegistrationJSON_NamesEverySubcommand(t *testing.T) {
	t.Parallel()
	raw, err := discord.CommandRegistrationJSON()
	require.NoError(t, err)

	var registered []struct {
		Name    string `json:"name"`
		Options []struct {
			Name    string `json:"name"`
			Type    int    `json:"type"`
			Options []struct {
				Name     string `json:"name"`
				Required bool   `json:"required"`
			} `json:"options"`
		} `json:"options"`
	}
	require.NoError(t, json.Unmarshal(raw, &registered))
	require.Len(t, registered, 1, "this instance registers exactly one application command")
	require.Equal(t, discord.RootCommand, registered[0].Name)

	got := map[string]bool{}
	for _, sub := range registered[0].Options {
		require.Equal(t, discord.OptionTypeSubCommand, sub.Type)
		got[sub.Name] = true
	}
	for _, c := range discord.Commands() {
		require.True(t, got[c.Name], "%s is in the catalogue and is not registered", c.Name)
	}
	require.Len(t, got, len(discord.Commands()),
		"the registration names a subcommand the catalogue does not")
}

// A reply longer than Discord accepts is truncated VISIBLY rather than refused. Discord answers a
// too-long body with a 400 the invoker sees as "the application did not respond", which reads as
// the instance being down.
func TestReply_AnOverlongContent_IsTruncatedAndSaysSo(t *testing.T) {
	t.Parallel()
	reply := discord.Ephemeral(fixtureNow, strings.Repeat("x", discord.MaxContent*2))
	require.NotNil(t, reply.Data)
	require.LessOrEqual(t, len(reply.Data.Content), discord.MaxContent)
	require.Contains(t, reply.Data.Content, "truncated")
}

// **A person can be in two circles ON ONE SERVER, and a guild can bind a channel to each.**
//
// `membership` carries no per-server uniqueness and `ux_circle_name_norm_server` makes a name
// unique only WITHIN a server, so "a guild raiding Blue and Green makes two circles" is the easy
// half of the case ADR-0017 exists for and not the whole of it. Nothing in this package may key on
// the server: the resolve is channel → circle id, and the reply prints name AND server because
// neither identifies a circle alone.
func TestDispatch_TwoCirclesOnOneServer_AreDistinguishedByTheChannelBinding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	guildCircle := f.seedCircle("Guild Nights", "blue")
	alliance := f.seedCircle("Alliance Nights", "blue")

	// One person, two memberships, both on blue. The schema permits it and nothing here may not.
	guildOwner := f.seedMember(guildCircle, aliceSubject, "Alice", string(authz.RoleOwner))
	f.seedMember(alliance, bobSubject, "Bob", string(authz.RoleOwner))
	f.bind(guildCircle, boundChannel, guild, false, guildOwner)

	reply, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandCircles, boundChannel, guild, aliceSubject, nil), fixtureNow)
	require.NoError(t, err)
	content := ephemeral(t, reply)

	require.Contains(t, content, "Guild Nights",
		"the binding names one circle and the reply must name that one")
	require.NotContains(t, content, "Alliance Nights",
		"a circle the invoker is not in was named")
	// The BOUND-CIRCLE line specifically, not merely the word "blue" somewhere in the reply: the
	// membership list below it prints the server too, so a looser assertion would pass with the
	// binding's own line saying nothing about which server it means.
	require.Contains(t, content, "**Guild Nights** on blue",
		"the bound-circle line prints the server, because a name identifies a circle only within one")
}

// The unbound-channel answer lists name AND server for every circle, so somebody in two circles on
// one server can tell the rows apart. A bare list of names is a list a reader cannot always read.
func TestDispatch_AnUnboundChannel_NamesTheServerOfEveryCircleItOffers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.seedCircle("Guild Nights", "blue")
	second := f.seedCircle("Alliance Nights", "blue")
	f.seedMember(first, aliceSubject, "Alice", string(authz.RoleMember))
	f.seedMemberFor(second, aliceSubject, "Alice", string(authz.RoleMember))

	reply, err := f.commander.Dispatch(
		t.Context(), command(discord.CommandBoard, otherChannel, guild, aliceSubject, nil), fixtureNow)
	require.NoError(t, err)
	content := ephemeral(t, reply)

	require.Contains(t, content, "Guild Nights (blue)")
	require.Contains(t, content, "Alliance Nights (blue)")
}

// **The permission a command declares is enough to RUN it.**
//
// A command's catalogue entry names one key, and the dispatcher checks that one — but the handler
// behind it may read more than that key covers. `/tod status` runs the catalogue resolve ladder,
// which is `catalogue.read`: today every role holds it, so `tod.read` is the narrowest honest gate,
// and if the role matrix ever changed that would stop being true silently.
//
// So each command is driven by the WEAKEST role that holds its declared permission, and required
// not to answer with a permission refusal. A command whose handler grew a second requirement is
// then red here rather than discovered by whichever member has the weakest role.
func TestCommands_TheWeakestRoleHoldingThePermission_CanRunTheCommand(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(mine, boundChannel, guild, false, owner)
	f.seedTarget("Vulak`Aerr")

	for i, c := range discord.Commands() {
		t.Run(c.Name, func(t *testing.T) {
			role := weakestRoleHolding(t, c.Permission)
			// A fresh Discord account per subtest, so the roles cannot interfere. Keyed on the
			// INDEX rather than on the name: `status` and `report` are both six characters, and
			// deriving the subject from the length collided them onto one identity — which
			// `identity`'s unique `(provider, subject)` refused, as it should.
			subject := fmt.Sprintf("7000000000000000%02d", i)
			f.seedMember(mine, subject, "Weakest "+c.Name, string(role))

			args := map[string]any{}
			for _, o := range c.Options {
				if o.Required {
					args[o.Name] = "Vulak`Aerr"
				}
			}
			reply, err := f.commander.Dispatch(
				t.Context(), command(c.Name, boundChannel, guild, subject, args), fixtureNow)
			require.NoError(t, err)
			content := ephemeral(t, reply)
			require.NotContains(t, content, "does not hold",
				"%s declares %q, %s is the weakest role holding it, and the command still "+
					"refused — the handler needs more than the catalogue says",
				c.Name, c.Permission, role)
		})
	}
}

// weakestRoleHolding returns the lowest role in the matrix that holds the permission, or the
// weakest role of all when the command declares none.
func weakestRoleHolding(t *testing.T, permission authz.Permission) authz.Role {
	t.Helper()
	roles := authz.Roles()
	require.NotEmpty(t, roles)
	if permission == "" {
		return roles[0]
	}
	for _, r := range roles {
		if authz.RolePermissions(r).Has(permission) {
			return r
		}
	}
	t.Fatalf("no role holds %q; a command declares a permission no circle role grants", permission)
	return ""
}
