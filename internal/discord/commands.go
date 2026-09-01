package discord

import (
	"encoding/json"
	"fmt"

	"github.com/prokopto-dev/tod-serve/internal/authz"
)

// RootCommand is the one application command this instance registers. Every capability is a
// subcommand of it, so a guild's command list gains one entry rather than four.
const RootCommand = "tod"

// The subcommand names. They are constants because three things spell them: the catalogue below,
// the dispatcher's switch, and the JSON an operator registers with Discord.
const (
	// CommandBoard is the circle's board — every target with a window, soonest first.
	CommandBoard = "board"
	// CommandStatus is one target's state.
	CommandStatus = "status"
	// CommandReport appends a time-of-death report.
	CommandReport = "report"
	// CommandCircles is the unbound-channel answer: which circles the INVOKER is in, and what
	// this channel is bound to. It is the one command that resolves no circle, which is why it
	// is also the one that works in a channel nobody has bound.
	CommandCircles = "circles"
)

// The option names, for the same reason.
const (
	// OptionTarget is a raid target's name, run through the resolve ladder. There is no target-id
	// option, and that is not an omission: an id is the shape a cross-circle probe takes, and a
	// name is what somebody types in Discord anyway.
	OptionTarget = "target"
	// OptionMinutesAgo backdates a report. `died_at` is game truth and may be backdated;
	// `reported_at` is system truth and never is.
	OptionMinutesAgo = "minutes_ago"
	// OptionVisible asks for a reply the channel can see. It is a REQUEST and not a decision:
	// the binding has to allow it as well.
	OptionVisible = "visible"
)

// Command is one subcommand, as data.
//
// It is a registry for the same reason `internal/api` has one: the permission a command needs, and
// whether it may ever answer where a channel can read it, are facts a test can walk. A command
// whose permission lived in its handler is a command whose permission is checked by whoever
// remembers to.
type Command struct {
	// Name is the subcommand.
	Name string
	// Description is what Discord shows in the command picker.
	Description string
	// Permission is the catalogue key the INVOKER must hold in the resolved circle. Empty means
	// the command reaches no circle data at all, which is true of exactly one of them.
	Permission authz.Permission
	// Visible says the command may offer a visible reply. It is still refused unless the binding
	// allows it and the invoker asked — this only says the option exists.
	//
	// `circles` carries false, and that is the interesting one: it lists the invoker's OTHER
	// circles, which is a fact about them rather than about this channel, and a channel bound to
	// one circle has no business learning the names of the rest.
	Visible bool
	// Writes says the command appends to the domain. It exists so the route's missing
	// `Idempotency-Key` is a stated fact with a test over it rather than an oversight — see
	// TestDiscordInteraction_AReplayedInteraction_AppendsOneRow.
	Writes bool
	// Options are the arguments, in the order Discord shows them. Required options come first;
	// Discord refuses a registration that orders them otherwise.
	Options []Option
}

// Option is one argument of a subcommand.
type Option struct {
	Name        string
	Description string
	// Type is a Discord option type: [OptionTypeString], [OptionTypeInteger] or
	// [OptionTypeBoolean].
	Type     int
	Required bool
}

// Commands returns the command surface, in the order it is registered.
//
// It is a function rather than a package-level slice for the reason the permission catalogue and
// the route registry are: the surface a bot exposes is the last thing that should be editable from
// a distance.
func Commands() []Command {
	return []Command{
		{
			Name:        CommandBoard,
			Description: "The circle's board for this channel: every target with a window, soonest first",
			Permission:  authz.PermissionTodRead,
			Visible:     true,
			Options: []Option{
				{
					Name:        OptionVisible,
					Description: "Post where the channel can read it. Only in a channel an officer has enabled",
					Type:        OptionTypeBoolean,
				},
			},
		},
		{
			Name:        CommandStatus,
			Description: "One target's state: window, confidence, and what the answer rests on",
			// `tod.read`, although the handler also runs the catalogue resolve ladder, which is
			// `catalogue.read`. Every role holds that — it is in the observer set — so `tod.read`
			// is the narrowest key that actually gates this command, and declaring both would
			// suggest a distinction there is not. If the role matrix ever moved `catalogue.read`
			// up, `TestCommands_TheWeakestRoleHoldingThePermission_CanRunTheCommand` is what goes
			// red rather than the weakest member of somebody's circle.
			Permission: authz.PermissionTodRead,
			Visible:    true,
			Options: []Option{
				{
					Name:        OptionTarget,
					Description: "The target's name. Aliases and partial names resolve",
					Type:        OptionTypeString,
					Required:    true,
				},
				{
					Name:        OptionVisible,
					Description: "Post where the channel can read it. Only in a channel an officer has enabled",
					Type:        OptionTypeBoolean,
				},
			},
		},
		{
			Name:        CommandReport,
			Description: "Report a time of death for this channel's circle",
			Permission:  authz.PermissionTodReport,
			// A report is never visible. The reply names the target and the time it recorded,
			// which is the circle's competitive intelligence at its freshest, and the person
			// running the command is in a raid rather than in a position to think about who is
			// reading the channel.
			Visible: false,
			Writes:  true,
			Options: []Option{
				{
					Name:        OptionTarget,
					Description: "The target's name. Aliases and partial names resolve",
					Type:        OptionTypeString,
					Required:    true,
				},
				{
					Name:        OptionMinutesAgo,
					Description: "How long ago it died. Omit for just now",
					Type:        OptionTypeInteger,
				},
			},
		},
		{
			Name:        CommandCircles,
			Description: "Which of your circles this channel is bound to, and which you are in",
			// No permission: it reads the invoker's own memberships and nothing else. That is the
			// same shape a `self` route has in the route registry.
			Permission: "",
			Visible:    false,
		},
	}
}

// LookupCommand returns the command with the given subcommand name.
func LookupCommand(name string) (Command, bool) {
	for _, c := range Commands() {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// registration is the JSON shape Discord's `PUT /applications/{id}/commands` takes.
type registration struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Type        int                  `json:"type"`
	Options     []registrationOption `json:"options"`
}

type registrationOption struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Type        int                  `json:"type"`
	Required    bool                 `json:"required,omitempty"`
	Options     []registrationOption `json:"options,omitempty"`
}

// CommandRegistrationJSON renders [Commands] as the body an operator PUTs to Discord.
//
// **This server never sends it, and that is law 6 rather than laziness.** Registering commands is
// an outbound HTTPS request to Discord, and outbound HTTP is confined to `internal/identity`
// through one guarded client — a request from here would need a `NET001` exception for a call made
// once per deployment. So the binary prints the body and the operator sends it with the bot token,
// which is also why no bot token is stored on this instance at all.
//
// It is generated from the same [Commands] the dispatcher switches on, so a command an operator
// registered and this server does not answer is not a thing that can happen by editing one of two
// lists.
func CommandRegistrationJSON() ([]byte, error) {
	subcommands := make([]registrationOption, 0, len(Commands()))
	for _, c := range Commands() {
		opts := make([]registrationOption, 0, len(c.Options))
		for _, o := range c.Options {
			opts = append(opts, registrationOption{
				Name: o.Name, Description: o.Description, Type: o.Type, Required: o.Required,
			})
		}
		subcommands = append(subcommands, registrationOption{
			Name: c.Name, Description: c.Description, Type: OptionTypeSubCommand, Options: opts,
		})
	}
	body := []registration{{
		Name:        RootCommand,
		Description: "Times of death for this channel's circle",
		// CHAT_INPUT. A user or message command would appear in a context menu, where there is no
		// argument to carry the target and no way to ask for an ephemeral reply.
		Type:    1,
		Options: subcommands,
	}}
	out, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the Discord command registration: %w", err)
	}
	return out, nil
}
