package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// The domain this surface reaches, as narrow interfaces.
//
// They are interfaces rather than the services themselves so a test can drive the whole dispatch
// without a projection cache, and so the set of things a Discord command can do is a list somebody
// can read. Every one is satisfied by the real service, unchanged.
type (
	// Circles reads the circle a binding resolved to. Only the server is used, and only to fill
	// in a report's — the circle's own value rather than anything the payload said. **Nothing
	// here keys on it:** a server does not identify a circle, and one guild may bind two channels
	// to two circles on the same server.
	Circles interface {
		Get(ctx context.Context, id core.CircleID) (circle.Circle, error)
	}
	// Boards renders the board `/tod board` answers with.
	Boards interface {
		Board(ctx context.Context, circleID core.CircleID, filter projection.BoardFilter) (
			[]projection.BoardEntry, bool, error)
		Get(ctx context.Context, circleID core.CircleID, targetID core.RaidTargetID,
			attribution bool) (projection.Derived, error)
	}
	// Targets runs the ONE resolve ladder. `/tod status Vulak` and `createTodReport`'s
	// `target_name` go through the same matcher, so a name that resolves in one resolves in the
	// other.
	Targets interface {
		Resolve(ctx context.Context, ref catalogue.Ref) (catalogue.Resolution, error)
	}
	// Reports appends to the log.
	Reports interface {
		Create(ctx context.Context, req tod.CreateRequest) (tod.Created, error)
	}
)

// CommanderConfig is everything the dispatcher needs.
type CommanderConfig struct {
	Bindings  *Service
	Providers Providers
	Circles   Circles
	Boards    Boards
	Targets   Targets
	Reports   Reports
	Clock     clock.Clock
	Log       *slog.Logger
}

// Commander turns a verified interaction into a reply.
//
// It is the one place the five rules of [04-identity §9] are applied together, in this order:
// derive the circle from the channel, resolve the invoking user's principal in it, check THAT
// principal's permission, do the work, and decide — last, and only then — whether anybody but the
// invoker may see the answer.
//
// [04-identity §9]: https://github.com/prokopto-dev/tod-serve/blob/main/docs/design/04-identity-and-revocation.md
type Commander struct {
	bindings  *Service
	providers Providers
	circles   Circles
	boards    Boards
	targets   Targets
	reports   Reports
	clock     clock.Clock
	log       *slog.Logger
}

// NewCommander returns a dispatcher. Every dependency is required: one wired without a board would
// answer `/tod board` with an internal error, which is a worse failure than not starting.
func NewCommander(cfg CommanderConfig) (*Commander, error) {
	switch {
	case cfg.Bindings == nil:
		return nil, errors.New("discord commander: no binding service")
	case cfg.Providers == nil:
		return nil, errors.New("discord commander: no identity providers")
	case cfg.Circles == nil:
		return nil, errors.New("discord commander: no circle service")
	case cfg.Boards == nil:
		return nil, errors.New("discord commander: no projection service")
	case cfg.Targets == nil:
		return nil, errors.New("discord commander: no catalogue service")
	case cfg.Reports == nil:
		return nil, errors.New("discord commander: no tod service")
	case cfg.Clock == nil:
		return nil, errors.New("discord commander: no clock")
	case cfg.Log == nil:
		return nil, errors.New("discord commander: no logger")
	}
	return &Commander{
		bindings: cfg.Bindings, providers: cfg.Providers, circles: cfg.Circles,
		boards: cfg.Boards, targets: cfg.Targets, reports: cfg.Reports,
		clock: cfg.Clock, log: cfg.Log,
	}, nil
}

// Dispatch answers one verified interaction.
//
// It returns a reply for everything a caller could send, including a command that does not exist
// and a principal that reaches nothing. **There is no error return with a body, on purpose:** an
// interaction that answers with an HTTP error shows the invoker "the application did not respond",
// which is indistinguishable from the instance being down. The one exception is an infrastructure
// failure, which is returned so the edge can render a problem and log it.
func (c *Commander) Dispatch(
	ctx context.Context, in Interaction, signedAt core.Micros,
) (InteractionReply, error) {
	now := c.clock.Now()
	if in.Type == TypePing {
		return Pong(now), nil
	}
	if in.Type != TypeApplicationCommand {
		return Ephemeral(now, "This application only answers slash commands."), nil
	}

	invocation, err := in.Command()
	if err != nil {
		return Ephemeral(now, "That command is not one this application registered."), nil
	}
	command, ok := LookupCommand(invocation.Name)
	if !ok {
		// A command Discord has and this binary does not: the operator registered a definition
		// from a different version. Saying so is more useful than a generic refusal, because the
		// fix is `tod-serve discord commands` and one PUT.
		return Ephemeral(now, fmt.Sprintf(
			"`/%s %s` is not a command this instance answers. An operator can re-register the "+
				"command list with `tod-serve discord commands`.", RootCommand, invocation.Name)), nil
	}

	// `circles` resolves no circle, which is exactly why it works in a channel nobody has bound.
	if command.Name == CommandCircles {
		return c.circlesCommand(ctx, in, now)
	}

	binding, err := c.bindings.Resolve(ctx, in.GuildID, in.ChannelID)
	switch {
	case errors.Is(err, ErrChannelNotBound):
		return Ephemeral(now, c.unboundAdvice(ctx, in, now)), nil
	case errors.Is(err, ErrGuildMismatch):
		// Distinguishable from unbound, and safe to distinguish: the caller already knows the
		// channel is bound somewhere, and being told the guild does not match names no circle.
		return Ephemeral(now, "That channel is bound in a different Discord server. A binding is "+
			"made for one channel in one server; ask an officer to bind it here."), nil
	case err != nil:
		return InteractionReply{}, err
	}

	principal, err := c.bindings.Principal(ctx, c.providers, binding.CircleID, in.Subject())
	switch {
	case errors.Is(err, ErrNotAMember):
		// Rule 5, in Discord's words. It does not say the circle exists, does not say who is in
		// it, and does not say which of the four reasons applied.
		return Ephemeral(now, "You are not a member of the circle this channel is bound to. "+
			"Guild membership is not circle membership; ask an officer for an invite."), nil
	case err != nil:
		return InteractionReply{}, err
	}

	if command.Permission != "" && !principal.Can(command.Permission) {
		return Ephemeral(now, fmt.Sprintf(
			"Your role in this circle does not hold `%s`.", command.Permission)), nil
	}

	content, allowVisible, err := c.run(ctx, command, invocation, binding, principal, signedAt)
	if err != nil {
		return c.refusal(err, now)
	}

	// LAST, and both halves. The binding is an officer's stored decision that a visible reply is
	// acceptable in this channel; the option is the invoker asking for one on this command. Either
	// missing means ephemeral, because Discord has no channel-membership API and this server
	// therefore cannot know who a visible message reaches.
	if allowVisible && binding.AllowVisible && command.Visible && invocation.Bool(OptionVisible) {
		return Visible(now, content), nil
	}
	if invocation.Bool(OptionVisible) && command.Visible && !binding.AllowVisible {
		// Never hide a row silently: the invoker asked for something and did not get it, and the
		// reason is a decision somebody made rather than a bug.
		content += "\n\n_Posted only to you: visible replies are not enabled for this channel._"
	}
	return Ephemeral(now, content), nil
}

// run performs the command. The second result says whether the ANSWER may ever be visible, which
// is a property of what was produced rather than of what was asked for.
//
// `signedAt` is the instant the interaction was SIGNED at, from [Verifier.Verify], and not a clock
// reading. Only `report` uses it, and [Commander.report] says why.
func (c *Commander) run(
	ctx context.Context, command Command, in Invocation, binding Binding,
	principal auth.Principal, signedAt core.Micros,
) (string, bool, error) {
	switch command.Name {
	case CommandBoard:
		return c.board(ctx, binding.CircleID)
	case CommandStatus:
		return c.status(ctx, in, binding.CircleID, principal)
	case CommandReport:
		return c.report(ctx, in, binding.CircleID, principal, signedAt)
	default:
		// Unreachable through [Dispatch], which looked the command up in the same catalogue this
		// switch covers. TestCommands_EverySubcommand_IsDispatched is what keeps it that way.
		return "", false, fmt.Errorf("no handler for command %q", command.Name)
	}
}

// boardEntries caps what one reply shows. A circle's board is a hundred-odd targets and a Discord
// message is 2000 characters; the count of what was left out is reported, because a filter that
// drops rows counts them somewhere visible.
const boardEntries = 10

func (c *Commander) board(ctx context.Context, circleID core.CircleID) (string, bool, error) {
	entries, _, err := c.boards.Board(ctx, circleID, projection.BoardFilter{})
	if err != nil {
		return "", false, err
	}
	// Only targets with something to say. A board of a hundred `unknown` rows is a reply nobody
	// reads, and `unknown` is already the answer `/tod status` gives for one of them.
	interesting := make([]projection.BoardEntry, 0, len(entries))
	for _, e := range entries {
		if e.Status == "unknown" || e.Status == "no_timer" {
			continue
		}
		interesting = append(interesting, e)
	}
	sort.SliceStable(interesting, func(i, j int) bool {
		return windowSortKey(interesting[i]) < windowSortKey(interesting[j])
	})

	if len(interesting) == 0 {
		return "Nothing on the board yet. Report a time of death with `/" + RootCommand + " " +
			CommandReport + "`.", true, nil
	}

	var b strings.Builder
	b.WriteString("**Board**\n")
	shown := interesting
	if len(shown) > boardEntries {
		shown = shown[:boardEntries]
	}
	for _, e := range shown {
		fmt.Fprintf(&b, "- **%s** - %s, %s (confidence %s)\n",
			e.Target.Name, renderStatus(e.Status), renderWindow(e.Window), e.Confidence)
		if e.Contested {
			b.WriteString("  - contested: reports disagree\n")
		}
	}
	if dropped := len(interesting) - len(shown); dropped > 0 {
		fmt.Fprintf(&b, "\n_%d more not shown._", dropped)
	}
	return b.String(), true, nil
}

// windowSortKey orders the board the way the API's board is ordered: by when the window opens,
// with entries that have no window last rather than first.
func windowSortKey(e projection.BoardEntry) int64 {
	if e.Window.OpenAt != nil {
		return int64(*e.Window.OpenAt)
	}
	if e.Window.SpawnAt != nil {
		return int64(*e.Window.SpawnAt)
	}
	return 1<<62 - 1
}

func (c *Commander) status(
	ctx context.Context, in Invocation, circleID core.CircleID, principal auth.Principal,
) (string, bool, error) {
	name, ok := in.String(OptionTarget)
	if !ok || name == "" {
		return "", false, apierr.New(apierr.CodeValidationFailed, "name a target")
	}
	resolved, err := c.targets.Resolve(ctx, catalogue.Ref{Name: name})
	if err != nil {
		return "", false, err
	}
	// Attribution is the invoker's permission, not the channel's. An officer who holds
	// `tod.read.attribution` and asks for a visible answer publishes reporter names to everybody
	// who can read the channel; that is stated in the operator runbook and it is their call.
	attribution := principal.Can(authz.PermissionTodReadAttribution)
	derived, err := c.boards.Get(ctx, circleID, resolved.Target.ID, attribution)
	if err != nil {
		return "", false, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**%s** - %s\n", derived.Target.Name, renderStatus(derived.Status))
	fmt.Fprintf(&b, "%s\n", renderWindow(derived.Window))
	if derived.DiedAt != nil {
		fmt.Fprintf(&b, "Died %s\n", renderInstant(*derived.DiedAt))
	}
	fmt.Fprintf(&b, "Confidence %s, from %d report(s) by %d reporter(s)\n",
		derived.Confidence, derived.Evidence.ReportCount, derived.Evidence.DistinctReporterCount)
	if derived.Contested && derived.ContestReason != nil {
		fmt.Fprintf(&b, "Contested: %s\n", *derived.ContestReason)
	}
	if len(derived.Reporters) > 0 {
		names := make([]string, 0, len(derived.Reporters))
		for _, r := range derived.Reporters {
			names = append(names, r.DisplayName)
		}
		fmt.Fprintf(&b, "Reported by %s\n", strings.Join(names, ", "))
	}
	return strings.TrimRight(b.String(), "\n"), true, nil
}

// maxBackdateMinutes caps `minutes_ago`. `died_at` is game truth and may be backdated, but a
// report from a week ago typed into a slash command is a typo far more often than it is a
// correction, and the API's own `died_at_too_old` refusal is the same rule with a longer arm.
const maxBackdateMinutes = 24 * 60

// report appends one kill.
//
// **`died_at` is derived from the instant the interaction was SIGNED at, never from this server's
// clock, and that is what makes a repeated interaction a replay rather than a second row.**
//
// The route requires no `Idempotency-Key` — Discord does not send one, and there is no client-side
// retry to replay — so the thing standing in for it is `ux_tod_report_natural`, which is
// `(circle, target, reporter, died_at)`. That index only collapses a repeat if `died_at` is the
// SAME on both, and a clock reading is not: the same signed bytes replayed ninety seconds later
// produced a second report ninety seconds apart, with the natural key never consulted because the
// key differed. The signed timestamp is inside what the Ed25519 signature covers, so it cannot be
// moved by whoever kept the request, and it is the better answer on its own merits — the instant
// recorded is when the person pressed enter rather than when this handler happened to run.
//
// `TestDiscordInteraction_AReplayedInteraction_AppendsOneRow` is the gate, and it ADVANCES THE
// CLOCK between the two attempts. The version that did not advance it passed against the bug.
func (c *Commander) report(
	ctx context.Context, in Invocation, circleID core.CircleID,
	principal auth.Principal, signedAt core.Micros,
) (string, bool, error) {
	name, ok := in.String(OptionTarget)
	if !ok || name == "" {
		return "", false, apierr.New(apierr.CodeValidationFailed, "name a target")
	}
	diedAt := signedAt
	if minutes, ok := in.Int(OptionMinutesAgo); ok {
		if minutes < 0 || minutes > maxBackdateMinutes {
			return "", false, apierr.Newf(apierr.CodeValidationFailed,
				"minutes_ago is between 0 and %d", maxBackdateMinutes)
		}
		// Offset from the SIGNED instant too, so a backdated report replays for the same reason
		// an immediate one does.
		diedAt = signedAt.Add(-time.Duration(minutes) * time.Minute)
	}

	// Resolved HERE rather than left to `createTodReport`'s own `target_name`, although both run
	// the same ladder: an ambiguous name has to be reported to the person who typed it, and the
	// reply has to name the target it recorded rather than echo back the string they sent.
	resolved, err := c.targets.Resolve(ctx, catalogue.Ref{Name: name})
	if err != nil {
		return "", false, err
	}

	// The server comes from the CIRCLE, never from the interaction. A circle is pinned to one
	// server immutably, and `createTodReport` takes one so a client cannot fan a kill into the
	// wrong board — here there is no client to have got it wrong, so the circle's own value is
	// the only honest thing to send.
	tenant, err := c.circles.Get(ctx, circleID)
	if err != nil {
		return "", false, err
	}

	created, err := c.reports.Create(ctx, tod.CreateRequest{
		CircleID: circleID,
		Reporter: principal.MembershipID,
		TargetID: resolved.Target.ID.String(),
		Server:   tenant.Server,
		DiedAt:   diedAt,
		// `discord` is not one of the report sources the domain knows, and inventing one here
		// would put a value in `tod_report.source` that no enum has. `manual` is what a person
		// typing a time reports, which is exactly what this is.
		Source:         "manual",
		SelfConfidence: "certain",
	})
	if err != nil {
		return "", false, err
	}

	verb := "Recorded"
	if created.Replayed {
		// The natural key `ux_tod_report_natural` refused a second row for the same reporter,
		// target and instant. That is a REPLAY and not an error — the person asked for a row to
		// exist and it does — and this is the sentence that says so rather than pretending a
		// second report was appended.
		verb = "Already recorded"
	}
	return fmt.Sprintf("%s: **%s** died %s (%s).",
		verb, resolved.Target.Name, renderAge(created.Report.DiedAt, c.clock.Now()),
		renderInstant(created.Report.DiedAt)), false, nil
}

// circlesCommand answers in a channel nobody has bound, and in a bound one.
//
// It is the whole of rule 1's second half: **in an unbound channel there is no resolve**, so the
// bot offers the invoker the circles they are actually a member of and refuses to pick one. It
// names no circle the invoker is not in, and it never picks.
func (c *Commander) circlesCommand(
	ctx context.Context, in Interaction, now core.Micros,
) (InteractionReply, error) {
	mine, err := c.bindings.Memberships(ctx, c.providers, in.Subject())
	if err != nil {
		return InteractionReply{}, err
	}
	var b strings.Builder
	binding, resolveErr := c.bindings.Resolve(ctx, in.GuildID, in.ChannelID)
	switch {
	case resolveErr == nil:
		// The channel's circle is named only if the invoker is in it. Naming it otherwise would
		// tell a guild member that a circle they have never been admitted to exists.
		// Matched on the circle ID and never on the server. A person may hold memberships in
		// SEVERAL circles on one server — `membership` carries no per-server uniqueness, and
		// `ux_circle_name_norm_server` makes a name unique only WITHIN a server — so a guild can
		// bind two channels to two circles on blue, which is the same disambiguation problem the
		// binding exists to solve and not a cross-server one.
		named, namedServer := "", ""
		for _, m := range mine {
			if m.ID == binding.CircleID {
				named, namedServer = m.Name, m.Server
			}
		}
		if named != "" {
			visible := "ephemeral only"
			if binding.AllowVisible {
				visible = "visible replies enabled"
			}
			// Name AND server, because a name identifies a circle only within one server.
			fmt.Fprintf(&b, "This channel is bound to **%s** on %s (%s).\n\n",
				named, namedServer, visible)
		} else {
			b.WriteString("This channel is bound to a circle you are not a member of.\n\n")
		}
	case errors.Is(resolveErr, ErrChannelNotBound), errors.Is(resolveErr, ErrGuildMismatch):
		b.WriteString("This channel is not bound to a circle, so `/" + RootCommand +
			"` commands here have no circle to answer for.\n\n")
	default:
		return InteractionReply{}, resolveErr
	}

	if len(mine) == 0 {
		b.WriteString("You are not a member of any circle on this instance.")
	} else {
		b.WriteString("You are a member of:\n")
		for _, m := range mine {
			fmt.Fprintf(&b, "- %s (%s)\n", m.Name, m.Server)
		}
		b.WriteString("\nAn officer binds a channel to one of them from the circle's settings.")
	}
	// Always ephemeral, and [Command.Visible] is false for this one: the list is a fact about the
	// invoker's other circles, and a channel bound to one has no business learning the rest.
	return Ephemeral(now, b.String()), nil
}

// unboundAdvice is what a command that needed a circle says when the channel has none.
func (c *Commander) unboundAdvice(ctx context.Context, in Interaction, _ core.Micros) string {
	mine, err := c.bindings.Memberships(ctx, c.providers, in.Subject())
	if err != nil {
		// The advice is a convenience; failing to compute it must not turn a refusal into a 500.
		c.log.WarnContext(ctx, "could not list a Discord user's circles",
			slog.String("interaction_id", in.ID))
		mine = nil
	}
	if len(mine) == 0 {
		return "This channel is not bound to a circle, and you are not a member of any circle " +
			"on this instance. Guild membership is not circle membership."
	}
	// Name AND server on every one of them. A name is unique only within a server, and somebody
	// can be in two circles on the SAME server as well as one on each — so a bare list of names
	// is a list a reader cannot always tell apart.
	names := make([]string, 0, len(mine))
	for _, m := range mine {
		names = append(names, m.Name+" ("+m.Server+")")
	}
	return "This channel is not bound to a circle, so there is no circle for me to answer for. " +
		"You are a member of " + strings.Join(names, ", ") +
		" - an officer of one of them can bind this channel from the circle's settings."
}

// refusal renders a domain error as a message the invoker can act on.
//
// A refusal the domain has a code for is shown; anything else is returned to the edge, which logs
// it and renders a problem. The two must not be one branch: a message reading "something went
// wrong" for a `422 unknown_target` is how a person concludes the bot is broken and stops using it.
func (c *Commander) refusal(err error, now core.Micros) (InteractionReply, error) {
	coded, ok := apierr.From(err)
	if !ok {
		return InteractionReply{}, err
	}
	if coded.GetStatus() >= 500 {
		return InteractionReply{}, err
	}
	problem := coded.Problem()
	detail := problem.Detail
	if detail == "" {
		detail = problem.Title
	}
	return Ephemeral(now, detail), nil
}
