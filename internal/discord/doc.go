// Package discord is the inbound half of the Discord integration: the Ed25519 signature over an
// interaction, the channel-to-circle binding that says which tenant an interaction is about, and
// the command surface a member reaches from a slash command.
//
// **Nothing here makes an outbound request, and that is the whole shape of the decision.** Law 6
// confines outbound HTTP to `internal/identity` through one guarded client, so a gateway bot — a
// persistent outbound WebSocket — was refused in [ADR-0017]. Interactions run the other way:
// Discord POSTs, this package verifies the signature, and the reply is the body of the HTTP
// response to that POST. There is no second request to make and therefore no client to construct,
// which is why `NET001` needs no exception for this package.
//
// The one thing that WOULD need an outbound call is registering the command definitions with
// Discord, and this package does not do it either. [Commands] renders them as the exact JSON
// Discord's `PUT /applications/{id}/commands` takes, `tod-serve discord commands` prints it, and
// an operator sends it once with the bot token. That is why no bot token is configured on this
// instance at all: the only thing it would have been for is a request this binary may not make.
//
// Three rules from [04-identity §9] are held here rather than in a handler, because a handler is
// where they were most likely to be forgotten:
//
//   - The circle is DERIVED, by [Service.Resolve], from the channel the interaction arrived in.
//     No command takes a circle id, and [Command] has no option that could carry one.
//   - The principal is the INVOKING USER's, resolved by [Service.Principal] from the Discord
//     subject through `identity` to a `membership` in the resolved circle. This package holds no
//     credential of its own and there is nothing here for a confused deputy to spend.
//   - A reply is ephemeral unless the binding says visible replies are allowed AND the invoker
//     asked for one. [InteractionReply.Flags] is what carries it, and [Command.Visible] is what says a
//     command may ever offer the choice.
//
// [ADR-0017]: https://github.com/prokopto-dev/tod-serve/blob/main/docs/adr/0017-discord-interactions-in-the-binary.md
// [04-identity §9]: https://github.com/prokopto-dev/tod-serve/blob/main/docs/design/04-identity-and-revocation.md
package discord
