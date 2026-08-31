# The Discord bot: installing it, and what a bound channel discloses

**Status: the design is [ADR-0017](../adr/0017-discord-interactions-in-the-binary.md); the route and
the commands land in a follow-up.** §1 and §2 are Discord-side work you can do today. §3 onward
describes decisions the instance does not yet offer you, and says so where it matters — a runbook
that reads as though the feature shipped is worse than no runbook, because you cannot tell which
step you got wrong.

The part worth reading now regardless is **[§4 — what a bound channel
discloses](#4-what-a-bound-channel-discloses)**. That is the decision you are actually making, and
it is not reversible for anything already posted.

## This is the same application you already registered

The bot is a **second credential on the application from [discord-app.md](discord-app.md)**, not a
second registration ([ADR-0011](../adr/0011-operator-registered-discord-application.md)). Do not
create another one: a second application has a different `client_id`, and the replay check refuses a
token that was not minted for the one this instance is configured with.

Three values, three places, and mixing two of them up is the most common way to lose an hour:

| Value | Where Discord shows it | Where it goes |
|---|---|---|
| **Client ID** | OAuth2 → Client information | `identity_provider.client_id`. Public |
| **Client secret** | OAuth2 → Client information → Reset Secret | `identity_provider.client_secret`. Sign-in only |
| **Bot token** | Bot → Reset Token | The bot credential. **Never** `client_secret` |

[discord-app.md §2.4](discord-app.md#24-bot-installation-and-gateway-intents) already names the
failure in one direction: a bot token pasted into `client_secret` fails the token exchange with
`credential_invalid`. It fails at the *token exchange*, which is after the consent screen, so it
looks like a Discord problem and is not.

## 1. Add a bot to the existing application

Developer portal → your application → **Bot** → **Reset Token**, and copy it. **Shown once**, like
the client secret, and resetting it invalidates the previous one — so do it when you are ready to
paste it somewhere, not before.

Two settings on that page, both of which people turn on and neither of which is used:

- **Privileged Gateway Intents: leave them off.** This bot never connects to the gateway. ADR-0017
  rejected a gateway bot on law 6 — outbound connections are confined to `internal/identity` through
  one guarded client, and a persistent WebSocket is not that. Message Content Intent in particular
  buys nothing: the bot reads slash-command arguments, never channel messages.
- **Public Bot: turn it off** unless you want other people installing your instance's bot in their
  guilds. It has no bearing on your own guilds.

Where the token goes on the instance is settled by the implementation PR. It is stored the way
`client_secret` is — a `core.Secret`, rendered `***` on every path and never logged — and this page
will name the exact field when there is one.

## 2. Install it in a guild

Per guild, and by somebody who can **Manage Server** there. Build the install URL yourself; the
portal's URL Generator produces the same thing with more places to slip:

```
https://discord.com/oauth2/authorize?client_id=<YOUR_DISCORD_CLIENT_ID>&scope=applications.commands&permissions=0
```

`applications.commands` is the whole of it. **The bot asks for no Discord permissions, and that is
structural rather than modest:** an interaction is answered in the body of the HTTP response to
Discord's own POST, so the common path makes no outbound request and needs no standing right to
speak in your channel. Adding the `bot` scope makes the application appear in the member list;
nothing on this page needs it.

Install it in every guild that will use it. There is no instance-wide install.

### The interactions endpoint URL

Discord → your application → **General Information** → **Interactions Endpoint URL**. It is
`https://<YOUR_DOMAIN>` — the same `$TOD_PUBLIC_URL` that
[discord-app.md](discord-app.md#the-one-string-that-has-to-match) is about — plus the route's path.

ADR-0017's follow-up registers that route, so the path arrives with it. It will be published in
`openapi/openapi.json` and printed by `tod-serve doctor` beside the callback URL it already checks;
**those are the authority and this page is a copy**, and the implementation PR should gate the two
against each other the way `ENV001` gates `deploy/env.example` against the `TOD_*` constants. Until
the route exists there is nothing to paste, and Discord will not let you pretend otherwise:

**Discord verifies this URL when you save it.** It POSTs a signed `PING` and refuses to save unless
it gets a well-signed `PONG` back. That is the opposite of the redirect-URI failure in
discord-app.md — you cannot save a wrong interactions URL and find out at 2am. If it will not save,
the URL is wrong, the instance is unreachable, or the public key on the instance is not this
application's.

## 3. Bind a channel to a circle

A guild raiding Blue and Green is **two circles** on this instance, deliberately
([ADR-0009](../adr/0009-circle-pinned-to-one-server.md)) — there is no combined view anywhere. So a
command arriving from a channel has to be told which circle it is about, and a **channel binding**
is how you tell it.

What a binding is, in one line each:

- **One channel, one circle.** A channel cannot be bound to two. Binding a channel that already
  belongs to a live circle is refused rather than silently redirected — unbind it first, from the
  circle that holds it.
- **It takes `circle.security.manage`** — owner or officer — and it is written to the audit log,
  because it is a disclosure decision and not a preference.
- **It does not, by itself, make anything visible.** Visible replies are a second, separate switch
  on the binding, and it is **off** when the binding is created. See §4.
- **In an unbound channel the bot still works**, ephemerally, and asks you which of *your* circles
  you meant. Binding removes that question; it is not what makes the bot answer.
- **Deleting a circle leaves its bindings behind.** A deleted circle is a tombstone, not a delete —
  the report log outlives it — so the binding stops resolving rather than disappearing, and the
  channel can be bound to a new circle straight away.

The console screen and the API call for this land with the route.

## 4. What a bound channel discloses

Read this to your officers in these words. Discord has **no channel-membership API** — there is no
call that answers "who can read this channel". Visibility comes from guild membership plus role
permissions, so **this server cannot tell you who will see a visible message, and does not pretend
to**. You can. That is the whole reason the switch is yours.

| | Ephemeral reply — the default | Visible reply — only if you turned it on *and* asked for it |
|---|---|---|
| Who sees it | The one person who ran the command | Everyone who can read the channel |
| For how long | Until they dismiss it. Not in channel history, not searchable | Forever, in scrollback, to people who join the guild next year |
| Who that includes | Nobody else | Guild members who are **not in your circle**; members you have **revoked** from the circle but not from Discord; anyone a role change lets in later |

Three more, which are the ones officers get wrong:

- **A visible reply is composed with the *invoker's* permissions.** An officer who can see reporter
  attribution and asks for a visible answer publishes attribution to the channel. The reply carries
  what *that person* may see, not what the channel may see.
- **The bot never posts unprompted.** Every message is a reply to a command somebody ran. There is
  no ToD feed, no announcement, no "the window just opened" — that would be a continuous disclosure
  decision made by a server that cannot see who is reading, which is exactly what it must not do.
- **Unbinding does not unsay anything.** Messages already posted are Discord's, and Discord keeps
  them. Unbinding stops the next one.

What a bound channel does **not** do, in any configuration:

- **It does not grant membership.** A guild member who is not in the circle is answered as a
  stranger. Binding a channel is not an invite, and it is not a back door around one.
- **It does not disclose another circle** — including the other circle in the same guild. A command
  naming another circle's target or report is answered as though that thing does not exist, never as
  "you are not allowed": a refusal that admitted the row existed would confirm the circle does too.
- **It does not check Discord's channel permissions**, because it cannot. If you point a binding at
  a channel your whole server can read, the bot will do exactly what you told it to.

## 5. What this still does not fix

**Removing somebody's Discord role does not revoke a personal access token they already hold**, and
a bot built on interactions does not change that: Discord sends us commands, not events, so nothing
here notices a role coming off. ADR-0017 keeps that cost knowingly and names the option that would
have closed it. [discord-app.md's last
section](discord-app.md#what-this-does-not-do-and-you-should-tell-your-officers) is the full
version, including the sentence to say to officers. `revokeMember` is the tool that works,
immediately, on the next request.
