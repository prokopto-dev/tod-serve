package discord_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/discord"
)

// A channel resolves to at most one circle, and the second circle is refused rather than
// redirected. The refusal NAMES NO CIRCLE: saying which one holds the channel would confirm to an
// officer of circle A that circle B exists.
func TestBind_AChannelBoundToALiveCircle_IsRefusedAndNamesNoCircle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	blue := f.seedCircle("Ancient Blood", "blue")
	blueOwner := f.seedMember(blue, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(blue, boundChannel, guild, false, blueOwner)

	green := f.seedCircle("Green Sun", "green")
	greenOwner := f.seedMember(green, bobSubject, "Bob", string(authz.RoleOwner))

	_, err := f.bindings.Bind(t.Context(), discord.BindRequest{
		CircleID: green, ChannelID: boundChannel, GuildID: guild, By: greenOwner,
	})
	require.ErrorIs(t, err, discord.ErrChannelBoundElsewhere)
	coded, ok := apierr.From(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeConflict, coded.Code())
	require.NotContains(t, coded.Problem().Detail, "Ancient Blood",
		"the refusal named the other circle")

	// And the original binding is untouched: a refused bind must not half-apply.
	still, err := f.bindings.Resolve(t.Context(), guild, boundChannel)
	require.NoError(t, err)
	require.Equal(t, blue, still.CircleID)
}

// A binding whose circle is tombstoned may be replaced. Nothing deletes a binding when a circle is
// deleted — the report log outlives the circle — so the channel would otherwise be unusable for
// ever.
func TestBind_AChannelBoundToATombstonedCircle_IsReplaced(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dead := f.seedCircle("Gone", "blue")
	deadOwner := f.seedMember(dead, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(dead, boundChannel, guild, false, deadOwner)
	f.tombstone(dead)

	live := f.seedCircle("Here", "blue")
	liveOwner := f.seedMember(live, bobSubject, "Bob", string(authz.RoleOwner))
	bound, err := f.bindings.Bind(t.Context(), discord.BindRequest{
		CircleID: live, ChannelID: boundChannel, GuildID: guild, By: liveOwner,
	})
	require.NoError(t, err)
	require.Equal(t, live, bound.CircleID)
}

// `allow_visible` defaults to FALSE, and the default is in the DDL rather than only in a handler
// so a binding written by any path is silent until somebody says otherwise.
func TestBind_ANewBinding_IsEphemeralOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))

	bound, err := f.bindings.Bind(t.Context(), discord.BindRequest{
		CircleID: mine, ChannelID: boundChannel, GuildID: guild, By: owner,
	})
	require.NoError(t, err)
	require.False(t, bound.AllowVisible)
}

// Binding is a disclosure decision and not a preference, so it is audited — and so is unbinding,
// which is the decision being taken back.
func TestBindAndUnbind_AreAudited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))

	f.bind(mine, boundChannel, guild, true, owner)
	require.NoError(t, f.bindings.Unbind(t.Context(), mine, boundChannel, owner))

	entries := f.auditActions(mine)
	require.Contains(t, entries, string(discord.ActionChannelBound))
	require.Contains(t, entries, string(discord.ActionChannelUnbound))
}

// Unbinding a channel this circle does not hold is a `404`, from the query's own `WHERE`. That is
// law 5 for the binding table: an officer of one circle cannot discover, or remove, another's.
func TestUnbind_AChannelThisCircleDoesNotHold_IsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	theirs := f.seedCircle("Theirs", "blue")
	theirOwner := f.seedMember(theirs, aliceSubject, "Alice", string(authz.RoleOwner))
	f.bind(theirs, boundChannel, guild, false, theirOwner)

	mine := f.seedCircle("Mine", "green")
	myOwner := f.seedMember(mine, bobSubject, "Bob", string(authz.RoleOwner))

	err := f.bindings.Unbind(t.Context(), mine, boundChannel, myOwner)
	coded, ok := apierr.From(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeNotFound, coded.Code())

	// And the other circle's binding still resolves, so the refusal was a refusal rather than a
	// deletion that reported one.
	_, resolveErr := f.bindings.Resolve(t.Context(), guild, boundChannel)
	require.NoError(t, resolveErr)
}

// A channel or guild id that is not a Discord snowflake is refused at the edge of this package.
// It cannot ask Discord whether a channel exists — and would not believe the answer about who can
// read it if it could — so what this buys is that a channel id cannot be a path traversal, a ULID
// from another table, or a four-kilobyte string in a primary key.
func TestBind_AnIdThatIsNotASnowflake_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	owner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))

	for _, tc := range []struct{ name, channel, guildID string }{
		{"empty channel", "", guild},
		{"channel is not digits", "../../etc/passwd", guild},
		{"channel is far too long", "1234567890123456789012345", guild},
		{"empty guild", boundChannel, ""},
		{"guild is not digits", boundChannel, "not-a-guild"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := f.bindings.Bind(t.Context(), discord.BindRequest{
				CircleID: mine, ChannelID: tc.channel, GuildID: tc.guildID, By: owner,
			})
			require.ErrorIs(t, err, discord.ErrNotASnowflake)
		})
	}
}

// The resolve refuses a channel nobody has bound rather than answering for some circle. There is
// no default circle and there never will be: a default is the confident mistake this whole table
// exists to prevent.
func TestResolve_AnUnboundChannel_ResolvesToNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.bindings.Resolve(t.Context(), guild, otherChannel)
	require.ErrorIs(t, err, discord.ErrChannelNotBound)
}

// List answers with this circle's bindings and no other circle's, which is the read an officer
// makes before a raid and an auditor makes afterwards.
func TestList_ReturnsThisCirclesBindingsOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.seedCircle("Mine", "blue")
	myOwner := f.seedMember(mine, aliceSubject, "Alice", string(authz.RoleOwner))
	theirs := f.seedCircle("Theirs", "green")
	theirOwner := f.seedMember(theirs, bobSubject, "Bob", string(authz.RoleOwner))

	f.bind(mine, boundChannel, guild, false, myOwner)
	f.bind(theirs, otherChannel, guild, false, theirOwner)

	got, err := f.bindings.List(t.Context(), mine)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, boundChannel, got[0].ChannelID)
}
