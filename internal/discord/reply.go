package discord

import (
	"fmt"
	"strings"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// InteractionReply is Discord's interaction-callback body.
//
// It carries `as_of` alongside Discord's own two members, which canonical §1 requires of every
// response this API sends and Discord ignores as an unknown field. Exempting the one route whose
// body somebody else designed would be a second answer to "does every response carry `as_of`", and
// a second answer is what this repository gates against everywhere else. It is also not decorative
// here: the reply below renders instants and this is the clock they were rendered against.
type InteractionReply struct {
	Type int                   `json:"type"`
	Data *InteractionReplyData `json:"data,omitempty"`
	AsOf core.Micros           `json:"as_of"`
}

// InteractionReplyData is the message.
type InteractionReplyData struct {
	Content string `json:"content"`
	// Flags carries [FlagEphemeral] on every reply that has not been explicitly, doubly permitted
	// to be visible. Zero means the channel sees it.
	Flags int `json:"flags"`
}

// Pong answers Discord's endpoint-validation `PING`.
//
// It is the whole of what a `PING` gets: no message, no database read, and nothing derived from
// the payload. Discord POSTs one when an operator saves the interactions URL and refuses to save
// unless a well-signed `PONG` comes back, so this is also the operator's proof that the public key
// on this instance is this application's.
func Pong(now core.Micros) InteractionReply { return InteractionReply{Type: ResponsePong, AsOf: now} }

// Ephemeral returns a reply only the invoker sees.
//
// It is the constructor with no argument for visibility, which is the point: making a reply
// visible takes a different function and a permission check, so "forgot to set the flag" is not a
// way to disclose a circle's board to a channel.
func Ephemeral(now core.Micros, content string) InteractionReply {
	return InteractionReply{
		Type: ResponseChannelMessage,
		Data: &InteractionReplyData{Content: truncate(content), Flags: FlagEphemeral},
		AsOf: now,
	}
}

// Visible returns a reply everybody who can read the channel sees, now and in scrollback, for ever.
//
// Its only caller is [Commander.Dispatch], past both halves of the test rule 3 states: the binding
// says visible replies are allowed here, AND the invoker asked for one. See
// TestDispatch_AVisibleRequest_InAnUnpermittedChannel_StaysEphemeral.
func Visible(now core.Micros, content string) InteractionReply {
	return InteractionReply{
		Type: ResponseChannelMessage,
		Data: &InteractionReplyData{Content: truncate(content)},
		AsOf: now,
	}
}

// MaxContent is Discord's message length limit. A reply over it is refused by Discord with a 400
// that the invoker sees as "the application did not respond", so it is truncated here instead,
// visibly.
const MaxContent = 2000

const truncationNotice = "\n... (truncated)"

func truncate(s string) string {
	if len(s) <= MaxContent {
		return s
	}
	// Never hide a row silently: the reader is told the list was cut rather than left believing
	// they saw all of it.
	return s[:MaxContent-len(truncationNotice)] + truncationNotice
}

// renderInstant writes an absolute time Discord renders in the reader's own locale, and a relative
// one beside it.
//
// **This is the one place a client clock is allowed to matter, and it is safe for the reason
// `WEB002` exists.** `WEB002` bans the browser's clock because a machine four minutes fast renders
// a countdown that is wrong on screen and right in the database. Discord's `<t:...>` is not that:
// the payload is an ABSOLUTE epoch second computed here from this response's `as_of`, and Discord
// renders it. Baking "in 4 minutes" into the text would be the actual hazard — a visible reply
// lives in scrollback for ever, and a string that was true when it was posted is a confident
// mistake five minutes later.
func renderInstant(at core.Micros) string {
	seconds := at.Time().Unix()
	return fmt.Sprintf("<t:%d:t> (<t:%d:R>)", seconds, seconds)
}

// renderWindow writes a target's respawn window the way the board does: the kind first, because
// `unknown` and `no_timer` are answers rather than missing data.
func renderWindow(w consensus.Window) string {
	switch {
	case w.SpawnAt != nil:
		return "spawns " + renderInstant(*w.SpawnAt)
	case w.OpenAt != nil && w.CloseAt != nil:
		return "window " + renderInstant(*w.OpenAt) + " to " + renderInstant(*w.CloseAt)
	case w.OpenAt != nil:
		return "window opens " + renderInstant(*w.OpenAt)
	default:
		return "no window"
	}
}

// renderStatus is the board's status, spelled for a person rather than for a client.
func renderStatus(status string) string {
	switch status {
	case "no_timer":
		return "no timer"
	case "pre_window":
		return "not yet in window"
	case "in_window":
		return "IN WINDOW"
	default:
		return strings.ReplaceAll(status, "_", " ")
	}
}

// renderAge writes how long ago an instant was, for a report the bot has just recorded. It is a
// duration rather than an instant because it is the thing the reporter is checking they got right.
func renderAge(diedAt, now core.Micros) string {
	d := now.Sub(diedAt).Round(time.Second)
	if d <= 0 {
		return "just now"
	}
	return d.String() + " ago"
}
