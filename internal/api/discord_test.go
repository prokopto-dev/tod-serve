package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/discord"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// The Discord ids these tests use. Constants so a failure names the same thing every time.
const (
	blueChannel  = "111111111111111111"
	greenChannel = "222222222222222222"
	sharedGuild  = "999999999999999999"
	blueSubject  = "555555555555555555"
	greenSubject = "444444444444444444"
)

// The two circles the cross-circle test uses are BOTH ON ONE SERVER, which is the harder half of
// "a guild names N circles" and the one a reading of ADR-0017 as a Blue/Green story misses.
//
// `membership` carries no per-server uniqueness and `ux_circle_name_norm_server` makes a name
// unique only WITHIN a server, so a person can hold memberships in two circles on blue and a guild
// can bind a channel to each. `harness.seedCircle` pins every circle it makes to blue, so this is
// what these tests already drive — named here so it is a stated property rather than an accident
// of the fixture.
const oneServerTwoCircles = "both circles are on blue"

const interactionsPath = api.BasePath + "/integrations/discord/interactions"

// **This is the test the whole endpoint rests on.** An unverified interaction is an
// unauthenticated write, and a suite that only drove valid signatures would pass with the check
// removed — which is exactly how this repository's gate defects have shipped.
//
// It walks [api.DiscordRoutes] rather than a list, so a second signed route cannot be added
// without being covered, and it asserts `401` specifically: Discord's own endpoint validation
// POSTs a deliberately invalid signature when an operator saves the URL and refuses an endpoint
// that answers anything else.
func TestDiscordRoutes_EveryRoute_RefusesAnUnsignedBody(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	routes := api.DiscordRoutes()
	require.NotEmpty(t, routes, "no route is authenticated by an interaction signature")

	_, body := signedInteraction(t, h.clock.Now(), map[string]any{"type": 1})
	valid, _ := signedInteraction(t, h.clock.Now(), map[string]any{"type": 1})

	forged := map[string]map[string]string{
		"no headers at all": {},
		"no signature": {
			api.DiscordTimestampHeader: valid[api.DiscordTimestampHeader],
		},
		"no timestamp": {
			api.DiscordSignatureHeader: valid[api.DiscordSignatureHeader],
		},
		"the signature is not hex": {
			api.DiscordSignatureHeader: "nonsense",
			api.DiscordTimestampHeader: valid[api.DiscordTimestampHeader],
		},
		"a well-formed signature by nobody": {
			api.DiscordSignatureHeader: "00" + valid[api.DiscordSignatureHeader][2:],
			api.DiscordTimestampHeader: valid[api.DiscordTimestampHeader],
		},
	}

	for _, route := range routes {
		for name, headers := range forged {
			t.Run(string(route.ID)+"/"+name, func(t *testing.T) {
				t.Parallel()
				got := h.do(request{
					Method: route.Method, Path: route.FullPath(),
					Body: string(body), Headers: headers,
				})
				require.Equal(t, http.StatusUnauthorized, got.Status,
					"%s accepted an interaction it could not verify (%s). Body: %s",
					route.ID, name, got.Body)
				require.Equal(t, apierr.CodeUnauthenticated, got.Problem.Code)
			})
		}
	}
}

// The other half, without which the test above passes against an endpoint that refuses everything.
// A genuine `PING` is answered with a `PONG`, which is what Discord looks for when an operator
// saves the interactions URL.
func TestDiscordInteraction_AGenuinePing_IsAnsweredWithAPong(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	headers, body := signedInteraction(t, h.clock.Now(), map[string]any{"type": 1})

	got := h.do(request{
		Method: http.MethodPost, Path: interactionsPath, Body: string(body), Headers: headers,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var reply discord.InteractionReply
	require.NoError(t, json.Unmarshal([]byte(got.Body), &reply))
	require.Equal(t, discord.ResponsePong, reply.Type)
	require.Equal(t, h.clock.Now(), reply.AsOf, "canonical §1: every response carries as_of")
}

// **Law 5, in Discord's words, over the case ADR-0017 exists for: one guild, two circles.**
//
// Blue and Green are two circles in the same Discord guild with a bound channel each. Green has
// reported a kill; an interaction in BLUE's channel must be answered as though that report does
// not exist — no evidence, no time of death, and no hint that anybody else knows one.
//
// It is not covered by TestTenancy_CrossCircle_EveryOperationDenies: that walks routes naming a
// circle in their path, and this route deliberately has none — the circle is derived from the
// channel binding, because a circle id in an interaction body is the class #29 closed.
func TestDiscordInteraction_ACrossCircleTarget_IsAnsweredAsAbsent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	target := h.seedTarget("Vulak`Aerr", "Temple of Veeshan")

	// Two circles, one guild, a bound channel each — and `oneServerTwoCircles`: they are BOTH on
	// blue. Distinguishing them by server would be impossible, which is the point.
	alliance := h.seedCircle("Alliance")
	allianceOwner := h.seedDiscordMember(alliance, authz.RoleOwner, greenSubject)
	h.seedBinding(alliance, greenChannel, sharedGuild, allianceOwner, false)
	h.seedReport(alliance, allianceOwner, target)

	guildCircle := h.seedCircle("Guild")
	guildOwner := h.seedDiscordMember(guildCircle, authz.RoleOwner, blueSubject)
	h.seedBinding(guildCircle, blueChannel, sharedGuild, guildOwner, false)
	require.Equal(t, h.serverOf(alliance), h.serverOf(guildCircle), oneServerTwoCircles)

	// The Guild circle's own channel, its own member, the same guild and the same target.
	content := h.interact(t, blueChannel, sharedGuild, blueSubject, discord.CommandStatus,
		map[string]any{discord.OptionTarget: "Vulak`Aerr"})
	require.Contains(t, content, "unknown",
		"this channel must see no state for a target only the other circle has reported")
	require.Contains(t, content, "0 report(s)")

	// And the control: the Alliance channel sees the Alliance report, so the assertion above is
	// about tenancy rather than about the command being broken.
	other := h.interact(t, greenChannel, sharedGuild, greenSubject, discord.CommandStatus,
		map[string]any{discord.OptionTarget: "Vulak`Aerr"})
	require.Contains(t, other, "1 report(s)")
}

// **A REPLAYED interaction appends one row, and the clock moves in between.**
//
// The route declares `CreatesState: false` and requires no `Idempotency-Key` — Discord sends none,
// and there is no client-side retry to replay — so `ux_tod_report_natural` stands in its place. It
// is `(circle, target, reporter, died_at)`, which collapses a repeat only if `died_at` is the same
// on both attempts. **It was not**: `died_at` came from this server's clock, so the same captured
// bytes replayed ninety seconds later wrote a second report ninety seconds apart and the natural
// key was never consulted. `died_at` now comes from the instant the interaction was SIGNED at,
// which is inside what the signature covers and therefore cannot be moved by whoever kept the
// request.
//
// **The clock advance is the whole test.** The version of this without it passed against the bug —
// a frozen clock makes "the two instants are equal" true for free, which is the assertion that
// needed proving. Ninety seconds is inside [discord.ReplayWindow], so the replay is one this
// endpoint still accepts as well-signed; it is the write that has to refuse to duplicate.
func TestDiscordInteraction_AReplayedInteraction_AppendsOneRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	blue := h.seedCircle("Blue")
	owner := h.seedDiscordMember(blue, authz.RoleOwner, blueSubject)
	h.seedBinding(blue, blueChannel, sharedGuild, owner, false)

	// Signed ONCE. Both attempts send byte-for-byte the same request, which is what a replay is.
	headers, body := signedInteraction(t, h.clock.Now(),
		reportPayload("interaction-replay", blueChannel, sharedGuild, blueSubject, nil))

	first := h.replay(t, headers, body)
	require.Contains(t, first, "Recorded")

	h.advance(90 * time.Second)

	second := h.replay(t, headers, body)
	require.Contains(t, second, "Already recorded",
		"a replayed interaction claimed to append a second row")

	reports, _, err := h.tods.List(t.Context(), tod.ListRequest{CircleID: blue, Limit: 50})
	require.NoError(t, err)
	require.Len(t, reports, 1, "the report log grew twice for one captured request")
	require.Equal(t, fixtureNow, reports[0].DiedAt,
		"died_at must be the SIGNED instant; a clock reading is what let the replay through")
}

// A backdated report replays for the same reason an immediate one does: `minutes_ago` offsets from
// the signed instant rather than from the clock, so the two attempts compute one `died_at`.
func TestDiscordInteraction_AReplayedBackdatedReport_AppendsOneRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	blue := h.seedCircle("Blue")
	owner := h.seedDiscordMember(blue, authz.RoleOwner, blueSubject)
	h.seedBinding(blue, blueChannel, sharedGuild, owner, false)

	headers, body := signedInteraction(t, h.clock.Now(), reportPayload(
		"interaction-backdated", blueChannel, sharedGuild, blueSubject,
		map[string]any{discord.OptionMinutesAgo: 20}))

	require.Contains(t, h.replay(t, headers, body), "Recorded")
	h.advance(2 * time.Minute)
	require.Contains(t, h.replay(t, headers, body), "Already recorded")

	reports, _, err := h.tods.List(t.Context(), tod.ListRequest{CircleID: blue, Limit: 50})
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, fixtureNow.Add(-20*time.Minute), reports[0].DiedAt)
}

// Two DISTINCT interactions are two observations and are not collapsed. They carry different
// signed instants, so the natural key does not match — which is correct: somebody running the
// command twice a minute apart has reported two times of death, and the derivation clusters them
// rather than this layer deciding one of them did not happen.
//
// It is the other direction of the test above, and without it that one passes against a handler
// that refused every second report outright.
func TestDiscordInteraction_TwoDistinctReports_AreBothRecorded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	blue := h.seedCircle("Blue")
	owner := h.seedDiscordMember(blue, authz.RoleOwner, blueSubject)
	h.seedBinding(blue, blueChannel, sharedGuild, owner, false)

	first, firstBody := signedInteraction(t, h.clock.Now(),
		reportPayload("interaction-one", blueChannel, sharedGuild, blueSubject, nil))
	require.Contains(t, h.replay(t, first, firstBody), "Recorded")

	h.advance(time.Minute)
	second, secondBody := signedInteraction(t, h.clock.Now(),
		reportPayload("interaction-two", blueChannel, sharedGuild, blueSubject, nil))
	require.Contains(t, h.replay(t, second, secondBody), "Recorded")

	reports, _, err := h.tods.List(t.Context(), tod.ListRequest{CircleID: blue, Limit: 50})
	require.NoError(t, err)
	require.Len(t, reports, 2, "two separate observations were collapsed into one")
}

// An interaction signed further ahead than the report log will accept a `died_at` is refused at
// the SIGNATURE rather than accepted and then rejected by the write.
//
// A member whose clock is three minutes fast would otherwise get a well-formed `401`-free
// signature check followed by a `422 died_at_in_future` from the database — a verified request the
// instance then refuses, which is the worst pair of answers available.
func TestDiscordInteraction_SignedFurtherAheadThanTheReportLogAccepts_IsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTarget("Vulak`Aerr", "Temple of Veeshan")
	blue := h.seedCircle("Blue")
	owner := h.seedDiscordMember(blue, authz.RoleOwner, blueSubject)
	h.seedBinding(blue, blueChannel, sharedGuild, owner, false)

	ahead := h.clock.Now().Add(discord.FutureSkewTolerance + time.Second)
	headers, body := signedInteraction(t, ahead,
		reportPayload("interaction-ahead", blueChannel, sharedGuild, blueSubject, nil))

	got := h.do(request{
		Method: http.MethodPost, Path: interactionsPath, Body: string(body), Headers: headers,
	})
	require.Equal(t, http.StatusUnauthorized, got.Status, got.Body)

	reports, _, err := h.tods.List(t.Context(), tod.ListRequest{CircleID: blue, Limit: 50})
	require.NoError(t, err)
	require.Empty(t, reports)
}

// A bind naming a channel that already belongs to a live circle is refused, and the refusal names
// no circle: telling an officer of one circle which other circle holds the channel confirms that
// the other circle exists.
func TestBindCircleDiscordChannel_AChannelBoundElsewhere_IsRefusedAndNamesNoCircle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	green := h.seedCircle("Green Sun")
	greenOwner := h.seedMember(green, authz.RoleOwner)
	h.seedBinding(green, blueChannel, sharedGuild, greenOwner, false)

	blue := h.seedCircle("Ancient Blood")
	blueOwner := h.seedMember(blue, authz.RoleOwner)

	got := h.do(request{
		Method:  http.MethodPut,
		Path:    bindingPath(blue, blueChannel),
		Session: h.session(blueOwner, true),
		Body:    `{"discord_guild_id":"` + sharedGuild + `"}`,
		Headers: map[string]string{api.IfMatchHeader: "*"},
	})
	require.Equal(t, http.StatusConflict, got.Status, got.Body)
	require.Equal(t, apierr.CodeConflict, got.Problem.Code)
	require.NotContains(t, got.Body, "Green Sun", "the refusal named the other circle")
}

// Another circle's binding is a `404`, not a `403`: law 5 through the DELETE's own `WHERE`. A
// `403` would confirm the binding — and therefore the circle behind it — exists.
func TestUnbindCircleDiscordChannel_AnotherCirclesBinding_Is404(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	green := h.seedCircle("Green")
	greenOwner := h.seedMember(green, authz.RoleOwner)
	h.seedBinding(green, greenChannel, sharedGuild, greenOwner, false)

	blue := h.seedCircle("Blue")
	blueOwner := h.seedMember(blue, authz.RoleOwner)

	got := h.do(request{
		Method:  http.MethodDelete,
		Path:    bindingPath(blue, greenChannel),
		Session: h.session(blueOwner, true),
	})
	require.Equal(t, http.StatusNotFound, got.Status, got.Body)
	require.Equal(t, apierr.CodeNotFound, got.Problem.Code)

	// And Green still has it, so the refusal refused rather than deleting and reporting one.
	list := h.do(request{
		Method: http.MethodGet, Path: bindingsPath(green), Session: h.session(greenOwner, true),
	})
	require.Equal(t, http.StatusOK, list.Status, list.Body)
	require.Contains(t, list.Body, greenChannel)
}

// Binding is in the capability floor: it is a disclosure decision, so no personal access token
// reaches it at any scope and the session has to be recently re-authenticated.
func TestBindCircleDiscordChannel_ReachesNoTokenAndNeedsAFreshSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	blue := h.seedCircle("Blue")
	owner := h.seedMember(blue, authz.RoleOwner)
	body := `{"discord_guild_id":"` + sharedGuild + `"}`
	headers := map[string]string{api.IfMatchHeader: "*"}

	withToken := h.do(request{
		Method: http.MethodPut, Path: bindingPath(blue, blueChannel),
		Token: h.seedToken(owner, allScopes()...), Body: body, Headers: headers,
	})
	h.requireProblem(withToken, apierr.CodeSessionRequired)

	stale := h.do(request{
		Method: http.MethodPut, Path: bindingPath(blue, blueChannel),
		Session: h.session(owner, false), Body: body, Headers: headers,
	})
	h.requireProblem(stale, apierr.CodeStepUpRequired)
}

// `If-Match: *` creates a binding and is REFUSED on one that exists, so an officer cannot reverse
// another officer's disclosure decision having read nothing.
func TestBindCircleDiscordChannel_TheWildcard_CreatesAndDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	blue := h.seedCircle("Blue")
	owner := h.seedMember(blue, authz.RoleOwner)
	body := `{"discord_guild_id":"` + sharedGuild + `","allow_visible":true}`

	created := h.do(request{
		Method: http.MethodPut, Path: bindingPath(blue, blueChannel),
		Session: h.session(owner, true), Body: body,
		Headers: map[string]string{api.IfMatchHeader: "*"},
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)
	etag := created.Header.Get(api.ETagHeader)
	require.NotEmpty(t, etag)

	again := h.do(request{
		Method: http.MethodPut, Path: bindingPath(blue, blueChannel),
		Session: h.session(owner, true), Body: body,
		Headers: map[string]string{api.IfMatchHeader: "*"},
	})
	h.requireProblem(again, apierr.CodePreconditionFailed)

	withTag := h.do(request{
		Method: http.MethodPut, Path: bindingPath(blue, blueChannel),
		Session: h.session(owner, true),
		Body:    `{"discord_guild_id":"` + sharedGuild + `","allow_visible":false}`,
		Headers: map[string]string{api.IfMatchHeader: etag},
	})
	require.Equal(t, http.StatusOK, withTag.Status, withTag.Body)
	require.Contains(t, withTag.Body, `"allow_visible":false`)
}

// TestGetCircleDiscordChannel_TheTagItReturns_IsWhatAReplaceRequires drives the read INTO the
// write, in both directions.
//
// The two sides are one line apart in `discord.go` and they still drift: the tag is computed over
// `bindingView`, and the response body is that view PLUS `as_of`. A tag taken over the body would
// be different on every read, no caller could ever satisfy the precondition, and the failure looks
// exactly like somebody else editing concurrently — which is the one explanation an officer will
// believe. So neither half is asserted in isolation: the tag this GET returned is sent as the
// `If-Match` of a real PUT, and the write has to succeed.
//
// The second half is the mutation proof. A test that only drove the accepted tag would pass with
// the precondition removed altogether, so the tag is re-sent AFTER the binding moved and has to be
// refused — otherwise this route would hand out a key that opens the lock whatever the state is,
// which is worse than no route at all.
func TestGetCircleDiscordChannel_TheTagItReturns_IsWhatAReplaceRequires(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	blue := h.seedCircle("Blue")
	owner := h.seedMember(blue, authz.RoleOwner)
	h.seedBinding(blue, blueChannel, sharedGuild, owner, false)

	read := h.do(request{
		Method: http.MethodGet, Path: bindingPath(blue, blueChannel),
		Session: h.session(owner, true),
	})
	require.Equal(t, http.StatusOK, read.Status, read.Body)
	etag := read.Header.Get(api.ETagHeader)
	require.NotEmpty(t, etag, "the read returned no ETag, so a replace is unreachable")
	require.Contains(t, read.Body, `"allow_visible":false`)
	require.Contains(t, read.Body, `"as_of"`,
		"the body carries as_of; the tag must NOT be taken over it")

	// The tag that read returned, on the write it exists for.
	replaced := h.do(request{
		Method: http.MethodPut, Path: bindingPath(blue, blueChannel),
		Session: h.session(owner, true),
		Body:    `{"discord_guild_id":"` + sharedGuild + `","allow_visible":true}`,
		Headers: map[string]string{api.IfMatchHeader: etag},
	})
	require.Equal(t, http.StatusOK, replaced.Status, replaced.Body)
	require.Contains(t, replaced.Body, `"allow_visible":true`)

	// And the same tag once the binding has moved: refused, carrying what it moved to. This is the
	// concurrency rule the console's flip now runs on — an officer who read `false`, and whose
	// colleague made it visible in between, cannot write their stale decision back.
	stale := h.do(request{
		Method: http.MethodPut, Path: bindingPath(blue, blueChannel),
		Session: h.session(owner, true),
		Body:    `{"discord_guild_id":"` + sharedGuild + `","allow_visible":false}`,
		Headers: map[string]string{api.IfMatchHeader: etag},
	})
	h.requireProblem(stale, apierr.CodePreconditionFailed)
	require.Contains(t, stale.Body, `"allow_visible":true`,
		"the 412 must carry the CURRENT representation, so the retry costs no extra read")
}

// Another circle's binding is a `404` on the read too, for the reason the DELETE is: a `403` would
// confirm the binding exists, and therefore that the circle holding it does.
func TestGetCircleDiscordChannel_AnotherCirclesBinding_Is404(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	green := h.seedCircle("Green")
	greenOwner := h.seedMember(green, authz.RoleOwner)
	h.seedBinding(green, greenChannel, sharedGuild, greenOwner, true)

	blue := h.seedCircle("Blue")
	blueOwner := h.seedMember(blue, authz.RoleOwner)

	got := h.do(request{
		Method: http.MethodGet, Path: bindingPath(blue, greenChannel),
		Session: h.session(blueOwner, true),
	})
	require.Equal(t, http.StatusNotFound, got.Status, got.Body)
	require.Equal(t, apierr.CodeNotFound, got.Problem.Code)
	require.NotContains(t, got.Body, "allow_visible",
		"the refusal leaked the other circle's disclosure setting")
}

// A binding with no `If-Match` at all is `428`, not a silent create: this PUT overwrites a
// disclosure decision, and a request that raced one must be told rather than applied.
func TestBindCircleDiscordChannel_WithoutIfMatch_Is428(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	blue := h.seedCircle("Blue")
	owner := h.seedMember(blue, authz.RoleOwner)

	got := h.do(request{
		Method: http.MethodPut, Path: bindingPath(blue, blueChannel),
		Session: h.session(owner, true),
		Body:    `{"discord_guild_id":"` + sharedGuild + `"}`,
	})
	h.requireProblem(got, apierr.CodePreconditionRequired)
}

// serverOf reads a circle's server straight out of the row, so a test asserting that two circles
// share one can say so from the database rather than from what the fixture was asked for.
func (h *harness) serverOf(circleID core.CircleID) string {
	h.t.Helper()
	row, err := h.store.Queries().GetCircle(h.t.Context(), circleID.String())
	require.NoError(h.t, err)
	return row.Server
}

// reportPayload is one `/tod report` interaction, as Discord sends it.
//
// It takes the interaction ID so a test can send the SAME one twice — a replay — or two different
// ones, which are two observations.
func reportPayload(
	id, channelID, guildID, subject string, extra map[string]any,
) map[string]any {
	options := []map[string]any{{"name": discord.OptionTarget, "value": "Vulak`Aerr"}}
	for k, v := range extra {
		options = append(options, map[string]any{"name": k, "value": v})
	}
	return map[string]any{
		"type": 2, "id": id, "guild_id": guildID, "channel_id": channelID,
		"member": map[string]any{"user": map[string]any{"id": subject, "username": "someone"}},
		"data": map[string]any{
			"name": discord.RootCommand,
			"options": []map[string]any{{
				"name": discord.CommandReport, "type": discord.OptionTypeSubCommand,
				"options": options,
			}},
		},
	}
}

// replay sends bytes that were signed earlier, verbatim. It is the difference between this and
// [harness.interact], which signs afresh at the current instant: a replay is the same request
// arriving twice, and signing again would be a second interaction.
func (h *harness) replay(t *testing.T, headers map[string]string, body []byte) string {
	t.Helper()
	got := h.do(request{
		Method: http.MethodPost, Path: interactionsPath, Body: string(body), Headers: headers,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var reply discord.InteractionReply
	require.NoError(t, json.Unmarshal([]byte(got.Body), &reply))
	require.NotNil(t, reply.Data)
	return reply.Data.Content
}

func bindingsPath(circleID core.CircleID) string {
	return api.BasePath + "/circles/" + circleID.String() + "/discord-channels"
}

func bindingPath(circleID core.CircleID, channelID string) string {
	return bindingsPath(circleID) + "/" + channelID
}

// seedDiscordMember writes an identity on the instance's `discord` provider carrying the given
// snowflake, and a membership for it.
//
// It is a second seeder beside [harness.seedMember] rather than a flag on it, because the two
// answer different questions: `seedMember` gives a principal a credential can resolve to, and this
// gives one an INTERACTION can — through `identity.subject`, which for a `discord` provider is the
// Discord snowflake and nothing else.
func (h *harness) seedDiscordMember(
	circle core.CircleID, role authz.Role, subject string,
) core.MembershipID {
	h.t.Helper()
	provider := h.seedDiscordProvider()
	identityID := newID[core.Identity](h)
	_, err := h.store.Queries().CreateIdentity(h.t.Context(), sqlitegen.CreateIdentityParams{
		ID: identityID.String(), ProviderID: provider.String(), Subject: subject,
		DisplayName: string(role) + " " + subject[:6],
		CreatedAt:   int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(h.t, err)

	membership := newID[core.Membership](h)
	owner := identityID.String()
	_, err = h.store.Queries().CreateMembership(h.t.Context(), sqlitegen.CreateMembershipParams{
		ID: membership.String(), CircleID: circle.String(), IdentityID: &owner,
		Kind: schemaenum.MembershipKindHuman, DisplayName: string(role) + subject[:4],
		DisplayNameNorm: string(role) + subject[:4], Role: string(role),
		JoinedAt:  int64(fixtureNow),
		CreatedAt: int64(fixtureNow), UpdatedAt: int64(fixtureNow),
	})
	require.NoError(h.t, err)
	return membership
}

// seedDiscordProvider writes the instance's one `discord` provider, once per harness.
func (h *harness) seedDiscordProvider() core.IdentityProviderID {
	h.t.Helper()
	if !h.discordProvider.IsZero() {
		return h.discordProvider
	}
	id := newID[core.IdentityProvider](h)
	clientID := "1234567890"
	_, err := h.store.Queries().CreateIdentityProvider(h.t.Context(),
		sqlitegen.CreateIdentityProviderParams{
			ID: id.String(), Key: "discord", Kind: schemaenum.IdentityProviderKindDiscord,
			DisplayName: "Discord", Enabled: 1,
			// A CHECK against `kind`, never a toggle: a Discord subject is verifiable.
			VerifiableSubject: 1,
			ClientID:          &clientID,
			CreatedAt:         int64(fixtureNow), UpdatedAt: int64(fixtureNow),
		})
	require.NoError(h.t, err)
	h.discordProvider = id
	return id
}

// seedBinding binds a channel through the real service, so the audit row and the refusals are the
// ones a real bind produces.
func (h *harness) seedBinding(
	circleID core.CircleID, channelID, guildID string, by core.MembershipID, allowVisible bool,
) {
	h.t.Helper()
	_, err := h.bindings.Bind(h.t.Context(), discord.BindRequest{
		CircleID: circleID, ChannelID: channelID, GuildID: guildID,
		AllowVisible: allowVisible, By: by,
	})
	require.NoError(h.t, err)
}

// seedReport appends a kill through the real report service, which is the only thing that writes
// one.
func (h *harness) seedReport(
	circleID core.CircleID, reporter core.MembershipID, target catalogue.Target,
) {
	h.t.Helper()
	_, err := h.tods.Create(h.t.Context(), tod.CreateRequest{
		CircleID: circleID, Reporter: reporter, TargetID: target.ID.String(),
		Server: schemaenum.ServerBlue, DiedAt: h.clock.Now(),
		Source: "manual", SelfConfidence: "certain",
	})
	require.NoError(h.t, err)
}

// interact drives one slash command all the way through the HTTP edge, signed the way Discord
// signs one, and returns the reply's content.
//
// It goes through the real route rather than calling the dispatcher, because half of what is under
// test here lives in the middleware: the signature check, the buffered raw body, and the fact that
// no principal is resolved at the edge at all.
func (h *harness) interact(
	t *testing.T, channelID, guildID, subject, command string, args map[string]any,
) string {
	t.Helper()
	options := make([]map[string]any, 0, len(args))
	for k, v := range args {
		options = append(options, map[string]any{"name": k, "value": v})
	}
	payload := map[string]any{
		"type": 2, "id": "interaction-" + command,
		"guild_id": guildID, "channel_id": channelID,
		"member": map[string]any{"user": map[string]any{"id": subject, "username": "someone"}},
		"data": map[string]any{
			"name": discord.RootCommand,
			"options": []map[string]any{{
				"name": command, "type": discord.OptionTypeSubCommand, "options": options,
			}},
		},
	}
	headers, body := signedInteraction(t, h.clock.Now(), payload)
	got := h.do(request{
		Method: http.MethodPost, Path: interactionsPath, Body: string(body), Headers: headers,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var reply discord.InteractionReply
	require.NoError(t, json.Unmarshal([]byte(got.Body), &reply))
	require.NotNil(t, reply.Data, "a command answered with no message")
	return reply.Data.Content
}
