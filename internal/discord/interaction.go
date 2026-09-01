package discord

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// InteractionType is Discord's `type` member. Only the two this instance answers are named: a
// `PING`, which is how Discord validates the endpoint URL, and an application command.
//
// The others — message components, autocomplete, modal submissions — are deliberately absent. A
// constant for a type nothing handles reads as though something does.
type InteractionType int

const (
	// TypePing is the interaction Discord sends when an operator saves the endpoint URL, and
	// periodically afterwards. It carries no guild, no channel and no user.
	TypePing InteractionType = 1
	// TypeApplicationCommand is a slash command.
	TypeApplicationCommand InteractionType = 2
)

// ResponseType is Discord's interaction-callback `type`.
const (
	// ResponsePong answers a [TypePing].
	ResponsePong = 1
	// ResponseChannelMessage answers a command with a message. Whether anybody but the invoker
	// sees it is [FlagEphemeral], never this.
	ResponseChannelMessage = 4
)

// FlagEphemeral is Discord's `EPHEMERAL` message flag: the reply is shown to the invoker alone,
// is not written to channel history and is not searchable.
//
// It is the DEFAULT here and not an option, because Discord has no channel-membership API — this
// server cannot enumerate who would read a visible message, so it must not make that disclosure
// decision. See [InteractionReply] and [04-identity §9].
//
// [04-identity §9]: https://github.com/prokopto-dev/tod-serve/blob/main/docs/design/04-identity-and-revocation.md
const FlagEphemeral = 1 << 6

// ErrMalformedInteraction is returned for a body that is not an interaction this server can read.
// It is one error for every shape of malformed input: the caller past the signature check is
// Discord, so a detailed parse failure would help nobody and would be logged on a hot path.
var ErrMalformedInteraction = errors.New("the interaction payload could not be read")

// ErrNotThisApplicationsCommand is returned for an interaction whose top-level command is not
// [RootCommand].
//
// **There is one interactions endpoint per Discord APPLICATION, not per command**, so every
// command registered on this application — a guild-scoped one, a legacy one an operator forgot to
// delete, one registered by a different tool against the same application id — arrives here with a
// valid signature. Without this check, any of them carrying a `SUB_COMMAND` named `board`,
// `status`, `report` or `circles` would dispatch, and [RootCommand] would be decorative: something
// the registration says and nothing enforces.
//
// It is a distinct error from [ErrMalformedInteraction] because the fix is different and belongs to
// a different person. A malformed payload is Discord sending something this version cannot read;
// this is a REGISTRATION that does not match the binary, which an operator repairs with
// `tod-serve discord commands`.
var ErrNotThisApplicationsCommand = errors.New(
	"the interaction names a command this instance did not register")

// Interaction is the part of Discord's payload this server reads. It is a NARROW struct on
// purpose: every member here is an assertion by a party we do not trust with tenancy, and a field
// that is never read is a field nobody has to reason about.
//
// `channel_id` and `guild_id` are the two that decide which circle this is about, and neither is
// believed on its own — [Service.Resolve] requires the binding to name both.
type Interaction struct {
	// ID is Discord's own id for this interaction. It is not a credential and it is not an
	// idempotency key; it is here so a log line can be correlated with a report from a user.
	ID   string          `json:"id"`
	Type InteractionType `json:"type"`
	// GuildID is empty in a direct message.
	GuildID string `json:"guild_id"`
	// ChannelID is the channel the command was run in. Empty in some payloads Discord has
	// shipped over the years, which is why the resolve refuses an empty one rather than treating
	// it as a channel nobody has bound.
	ChannelID string `json:"channel_id"`
	// Member is present in a guild; User is present in a direct message. Exactly one carries the
	// invoking Discord user, and [Interaction.Subject] is the only reader of either.
	Member *struct {
		User *InteractionUser `json:"user"`
	} `json:"member"`
	User *InteractionUser `json:"user"`
	Data *CommandData     `json:"data"`
}

// InteractionUser is the invoking Discord account. The snowflake is the `identity.subject` a
// `discord` provider stores, which is what makes "act as the invoking user" a lookup rather than
// a trust decision.
type InteractionUser struct {
	ID string `json:"id"`
	// Username is used for nothing but a log line. The display name on a membership is the
	// circle's, not Discord's.
	Username string `json:"username"`
}

// CommandData is the invoked command and its arguments.
type CommandData struct {
	Name    string          `json:"name"`
	Options []CommandOption `json:"options"`
}

// CommandOption is one argument, or — when [CommandOption.Type] is [OptionTypeSubCommand] — the
// subcommand carrying the arguments. Discord nests one level for `/tod board`, which is why this
// type is recursive.
type CommandOption struct {
	Name    string          `json:"name"`
	Type    int             `json:"type"`
	Value   json.RawMessage `json:"value"`
	Options []CommandOption `json:"options"`
}

// The Discord option types this surface uses. `SUB_COMMAND` is the outer one; the rest are the
// argument types [Commands] declares.
const (
	OptionTypeSubCommand = 1
	OptionTypeString     = 3
	OptionTypeInteger    = 4
	OptionTypeBoolean    = 5
)

// Subject returns the invoking Discord user's snowflake, from whichever member of the payload
// carries it.
//
// It is a method rather than two reads at the call site because getting it wrong fails OPEN in the
// worst way: a dispatcher that read only `user` would see an empty subject for every guild
// interaction, and an empty subject resolving to "no identity" is the answer a stranger gets — so
// the bug would look like a working refusal until somebody read the code.
func (i Interaction) Subject() string {
	if i.Member != nil && i.Member.User != nil && i.Member.User.ID != "" {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

// Invocation is one command as this server understands it: the subcommand name, and the arguments
// flattened out of Discord's nesting.
type Invocation struct {
	// Name is the subcommand — `board`, `status`, `report`, `circles`. The top-level command name
	// is not part of it: this instance registers exactly one, and a second would be a second
	// registration rather than a second branch here.
	Name string
	// Args are the subcommand's arguments, by option name.
	Args map[string]CommandOption
}

// String returns the argument as a string, and false when it is absent or is not one.
func (in Invocation) String(name string) (string, bool) {
	opt, ok := in.Args[name]
	if !ok || len(opt.Value) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(opt.Value, &s); err != nil {
		return "", false
	}
	return strings.TrimSpace(s), true
}

// Int returns the argument as an integer, and false when it is absent or is not one.
func (in Invocation) Int(name string) (int64, bool) {
	opt, ok := in.Args[name]
	if !ok || len(opt.Value) == 0 {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(opt.Value, &n); err != nil {
		return 0, false
	}
	return n, true
}

// Bool returns the argument as a boolean, and false when it is absent or is not one.
//
// The two falses are deliberately indistinguishable to the caller, and the one caller that matters
// is the visible-reply switch: "absent" and "false" both mean ephemeral, which is the answer that
// discloses nothing. A tri-state here would be a way for a malformed option to become a visible
// message.
func (in Invocation) Bool(name string) bool {
	opt, ok := in.Args[name]
	if !ok || len(opt.Value) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(opt.Value, &b); err != nil {
		return false
	}
	return b
}

// ParseInteraction reads a verified body.
//
// It is called only after the signature has been checked. That order is not a convenience: the
// payload is attacker-controlled until the signature says otherwise, and a parse that ran first
// would be doing work for anybody who can reach the port.
func ParseInteraction(body []byte) (Interaction, error) {
	var in Interaction
	if err := json.Unmarshal(body, &in); err != nil {
		return Interaction{}, fmt.Errorf("%w: %w", ErrMalformedInteraction, err)
	}
	if in.Type == 0 {
		return Interaction{}, fmt.Errorf("%w: no type", ErrMalformedInteraction)
	}
	return in, nil
}

// Command returns the invoked subcommand and its arguments.
//
// Discord sends `/tod board` as one command named `tod` carrying a single `SUB_COMMAND` option
// named `board`, whose own options are the arguments. Flattening that here means the dispatcher
// never walks the nesting, so a command added later cannot walk it differently.
//
// **The top-level name is CHECKED, not assumed.** One application has one interactions endpoint,
// so every command on it lands here — see [ErrNotThisApplicationsCommand]. The generated
// registration is not enforcement: it says what this instance asked Discord to offer, and says
// nothing about what Discord actually has, which an operator can add to at any time and from
// anywhere.
//
// The application id is deliberately NOT checked beside it, and that is not an oversight: a
// payload from another application is signed with another application's key, so
// [Verifier.Verify] has already refused it. A second check on a field the signature covers would
// read as though it were adding something.
func (i Interaction) Command() (Invocation, error) {
	if i.Data == nil || i.Data.Name == "" {
		return Invocation{}, fmt.Errorf("%w: no command data", ErrMalformedInteraction)
	}
	if i.Data.Name != RootCommand {
		return Invocation{}, fmt.Errorf("%w: %q", ErrNotThisApplicationsCommand, i.Data.Name)
	}
	for _, opt := range i.Data.Options {
		if opt.Type == OptionTypeSubCommand {
			return Invocation{Name: opt.Name, Args: byName(opt.Options)}, nil
		}
	}
	// A command with no subcommand is the bare `/tod`, which Discord does not send for a command
	// declared with subcommands. It is refused rather than defaulted: a default would make the
	// most privileged of the four reachable by sending the least specific payload.
	return Invocation{}, fmt.Errorf("%w: no subcommand", ErrMalformedInteraction)
}

func byName(opts []CommandOption) map[string]CommandOption {
	out := make(map[string]CommandOption, len(opts))
	for _, o := range opts {
		out[o.Name] = o
	}
	return out
}
