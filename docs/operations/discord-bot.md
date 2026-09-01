# The Discord bot: installing it, and what a bound channel discloses

**Status: shipped.** The design is [ADR-0017](../adr/0017-discord-interactions-in-the-binary.md),
the rules the endpoint is held to are
[04-identity §9](../design/04-identity-and-revocation.md#9-discord-interactions-what-is-disclosed-and-where),
and the API is [02-api-design](../design/02-api-design.md#discord). Work through §1 to §5 in order:
each step is checkable, and §7 says how to check the whole of it.

The part worth reading regardless of any of that is **[§6 — what a bound channel
discloses](#6-what-a-bound-channel-discloses)**. That is the decision you are actually making, and
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

### The bot token goes nowhere on the instance, and that is the answer

**There is no `TOD_DISCORD_BOT_TOKEN`, no field, and no row.** Keep the token in your password
manager; you will use it exactly once, from your own machine, in [§4](#4-register-the-slash-commands).

That is not an oversight, and it is better than what ADR-0017 accepted. The ADR listed "a second
high-value secret at rest" as a cost of this decision. It did not materialise, because the only
thing the token is for — telling Discord which slash commands exist — is an **outbound HTTPS
request**, and [law 6](../../AGENTS.md) confines outbound HTTP to `internal/identity` through one
guarded client. Rather than carve a `NET001` exception for a request made once per deployment, the
binary prints the body and you send it. A credential this instance never holds is a credential its
database cannot leak.

What the instance does hold is the application's **public key**, which verifies signatures and
signs nothing. It goes in `TOD_DISCORD_PUBLIC_KEY` — [§3](#3-give-the-instance-the-public-key).

## 2. Install it in a guild

Per guild, and by somebody who can **Manage Server** there. Build the install URL yourself; the
portal's URL Generator produces the same thing with more places to slip:

```
https://discord.com/oauth2/authorize?client_id=<YOUR_DISCORD_CLIENT_ID>&scope=applications.commands&permissions=0
```

Two values, and both of them are exact:

| Field | Value | Why not more |
|---|---|---|
| `scope` | `applications.commands` | The whole of it. It is what lets the application register slash commands in the guild, and it grants nothing else |
| `permissions` | `0` | **Zero, literally.** No Send Messages, no Read Message History, no Embed Links |

**The bot asks for no Discord permissions, and that is structural rather than modest:** every reply
is written into the body of the HTTP response to Discord's own POST, so the common path makes no
outbound request at all and needs no standing right to speak in your channel. A bot holding Send
Messages could post unprompted; this one has nothing to post with, which is a stronger statement
than a promise not to.

Adding the `bot` scope makes the application appear in the member list. Nothing here needs it, and
adding it is what makes Discord start asking you for a permissions integer.

Install it in every guild that will use it. There is no instance-wide install. **`Manage Server` in
that guild is what you need to install it** — the person doing this is a Discord administrator,
which is a different person from the circle officer who binds a channel in §5.

### The interactions endpoint URL

Discord → your application → **General Information** → **Interactions Endpoint URL**. Ask the binary
rather than typing it:

```console
$ tod-serve discord endpoint
https://tod.example.com/api/v1/integrations/discord/interactions
```

It is `$TOD_PUBLIC_URL` — the same one
[discord-app.md](discord-app.md#the-one-string-that-has-to-match) is about — plus this path:

```text
/api/v1/integrations/discord/interactions
```

**That command and `openapi/openapi.json` are the authority; the block above is a copy**, and
`TestDiscordBotRunbook_ThePathItPublishes_IsTheRouteRegistrys` compares the two so this page cannot
go stale the way a hand-written path does. `tod-serve doctor` prints the same URL beside the
callback URI it already checks.

**Save the public key first ([§3](#3-give-the-instance-the-public-key)), then save this URL.** In
that order, because:

**Discord verifies this URL when you save it.** It POSTs a signed `PING`, and then a deliberately
*invalid* one, and refuses to save unless it gets a well-signed `PONG` for the first and a `401` for
the second. That is the opposite of the redirect-URI failure in discord-app.md — you cannot save a
wrong interactions URL and find out at 2am. If it will not save, in decreasing order of likelihood:

1. `TOD_DISCORD_PUBLIC_KEY` is unset, or is not this application's key. An instance with no key
   refuses every interaction, including Discord's own `PING`.
2. The instance is not reachable at `$TOD_PUBLIC_URL` from the public internet.
3. You pasted the console URL, or the callback URL, instead of the one above.
4. The instance's clock is out. Signatures carry a timestamp and this server refuses one outside
   its window, because Ed25519 says *who* signed and never *when* — without a window a single
   captured interaction is replayable for ever. The window is **not symmetric**: five minutes into
   the past, and only **two minutes** into the future, which is the clock-skew tolerance the report
   log itself accepts. A clock running fast is therefore the tighter constraint of the two.

## 3. Give the instance the public key

Developer portal → your application → **General Information** → **Public Key**. Copy it — it is 64
hex characters — and set it in your `.env` beside the other `TOD_*` values:

```dotenv
TOD_DISCORD_PUBLIC_KEY=abc123...   # 64 hex characters, from General Information
```

Then restart: `docker compose up -d`. It is read at startup and nowhere else.

It is **not a secret**. It verifies signatures and cannot produce one, so it is safe in a file
people copy about — which is why it is a plain string in the configuration rather than a
`core.Secret`. Do not confuse it with the client secret or the bot token, both of which are.

An instance with it unset serves the endpoint and refuses every interaction with `401`,
indistinguishably from a forged one. That is the correct state for an instance not running the bot,
and it is why an unset key and a wrong one look identical from outside: telling a stranger which
one they found tells them what to try next.

A key that is present and **malformed** is a different case and is refused loudly — `serve` will not
start. An operator who pasted half a key finds out at boot rather than from the first person who
runs a command.

## 4. Register the slash commands

Discord does not discover your commands; you tell it what they are, once per deployment, and again
whenever you upgrade to a version that changes them.

**This server never makes that request.** It is outbound HTTPS, and law 6 confines outbound HTTP to
the identity providers through one guarded client. So the binary prints the exact body and you send
it:

```console
$ tod-serve discord commands > commands.json
$ curl -X PUT \
    -H "Authorization: Bot $DISCORD_BOT_TOKEN" \
    -H "Content-Type: application/json" \
    -d @commands.json \
    "https://discord.com/api/v10/applications/<YOUR_DISCORD_CLIENT_ID>/commands"
```

`<YOUR_DISCORD_CLIENT_ID>` is the application id — the same **Client ID** from the table at the top
of this page. `$DISCORD_BOT_TOKEN` is the token from §1, prefixed with the literal word `Bot` and a
space, which is Discord's scheme and not a typo for `Bearer`.

The body is generated from the same catalogue the server dispatches on, so a command you register
and this binary does not answer is not something you can produce by editing one of two lists. It
registers **one** application command, `/tod`, with a subcommand each:

| Command | Needs | Visible reply possible |
|---|---|---|
| `/tod board` | `tod.read` | Yes, in a channel where you enabled it |
| `/tod status <target>` | `tod.read` | Yes, in a channel where you enabled it |
| `/tod report <target> [minutes_ago]` | `tod.report` | **Never** |
| `/tod circles` | nothing | **Never** |

Those are the permissions of **the person who typed the command**, in the circle the channel is
bound to. The bot holds none of its own.

Global commands can take up to an hour to appear in every guild. If you are impatient, the same PUT
against `/applications/<id>/guilds/<guild id>/commands` registers them in one guild immediately —
useful for testing, and remember to remove the guild copy afterwards or members see each command
twice.

## 5. Bind a channel to a circle

A guild raiding Blue and Green is **two circles** on this instance, deliberately
([ADR-0009](../adr/0009-circle-pinned-to-one-server.md)) — there is no combined view anywhere. So a
command arriving from a channel has to be told which circle it is about, and a **channel binding**
is how you tell it.

**And it is not only the two-server case.** A circle is pinned to one server, but nothing limits a
person or a guild to one circle *per* server: your guild's own Blue roster and an alliance's Blue
roster are two circles, both on blue, and a member can be in both. Circle names are unique only
within a server, so neither the name nor the server identifies a circle on its own. The bot never
disambiguates by server — it resolves the **channel** to a circle id, which is the only thing that
does identify one — and it prints the name and the server together wherever it names a circle to
you.

What a binding is, in one line each:

- **One channel, one circle.** A channel cannot be bound to two. Binding a channel that already
  belongs to a live circle is refused rather than silently redirected — unbind it first, from the
  circle that holds it. Two circles on one server therefore need **two channels**, not one channel
  and a flag.
- **It takes `circle.security.manage`** — owner or officer — and it is written to the audit log,
  because it is a disclosure decision and not a preference.
- **It does not, by itself, make anything visible.** Visible replies are a second, separate switch
  on the binding, and it is **off** when the binding is created. See §6.
- **In an unbound channel the bot still works**, ephemerally, and asks you which of *your* circles
  you meant. Binding removes that question; it is not what makes the bot answer.
- **Deleting a circle leaves its bindings behind.** A deleted circle is a tombstone, not a delete —
  the report log outlives it — so the binding stops resolving rather than disappearing, and the
  channel can be bound to a new circle straight away.

### How to bind one

The API operation is `bindCircleDiscordChannel`, and it needs a browser session that has
re-authenticated recently — a personal access token reaches it at no scope, because a binding is a
disclosure decision and lives in the capability floor with the rest of them.

```http
PUT /api/v1/circles/{circle_id}/discord-channels/{discord_channel_id}
If-Match: *
Content-Type: application/json

{"discord_guild_id": "<the guild the channel is in>", "allow_visible": false}
```

- **`If-Match: *`** means "and it must not exist yet". Changing an existing binding takes the
  `ETag` a read returned instead, so you cannot reverse another officer's disclosure decision having
  read nothing.
- **`discord_channel_id` and `discord_guild_id`** are Discord snowflakes. Turn on **User Settings →
  Advanced → Developer Mode** in Discord, then right-click the channel → **Copy Channel ID**, and
  right-click the server icon → **Copy Server ID**.
- **The guild is stored and compared.** An interaction arriving from a different guild carrying that
  channel id resolves to nothing: the signature proves who *sent* the payload, not that the ids in
  it mean what your binding says.
- `GET /api/v1/circles/{circle_id}/discord-channels` lists what this circle discloses into.
  `DELETE` on the same path as the PUT unbinds.

**There is no console screen for this yet**, and this page says so rather than describing one. The
operations are in `openapi/openapi.json` and reachable from any client that can hold a session.

## 6. What a bound channel discloses

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

## 7. Check it works

Five checks, in order. Each one fails differently, which is what makes doing them in order worth
the time.

**1. The instance agrees with you about all of it.**

```console
$ tod-serve doctor
  ok       Discord interactions endpoint https://tod.example.com/api/v1/integrations/discord/interactions
  ok       TOD_DISCORD_PUBLIC_KEY is set
```

A `warn` on the second line means the key is unset and every interaction is refused. A `warn` on the
first means `$TOD_PUBLIC_URL` is not an origin, which breaks sign-in too.

**2. The endpoint refuses a request that is not Discord's.** From anywhere:

```console
$ curl -si -X POST -H 'Content-Type: application/json' -d '{"type":1}' \
    https://tod.example.com/api/v1/integrations/discord/interactions | head -1
HTTP/2 401
```

`401` is the answer you want. **A `200` here is a serious bug** — it means an unsigned body was
accepted, which is an unauthenticated write — and a `404` means you have the wrong path.

**3. Discord accepts the endpoint URL.** Save it in the portal (§2). It saves, or it tells you it
could not reach you; there is no third outcome, because Discord POSTs a signed `PING` and a
deliberately invalid one before it will accept the field.

**4. The commands appear.** Type `/tod` in a channel of a guild you installed into. Four
subcommands should offer themselves. If nothing appears, §4 did not run, or you registered against
the wrong application id, or a global registration has not propagated yet — give it an hour, or
register against the guild.

**5. A command answers.** Run `/tod circles` in an unbound channel. It should reply, **only to
you**, with the circles you are a member of and the sentence that this channel is bound to none of
them. That single command exercises the whole path: signature, parse, provider lookup, your Discord
account to an identity, your identity to memberships — and it discloses nothing, because it names
only circles you are already in.

Then bind a channel (§5) and run `/tod board` in it.

### When a command answers something you did not expect

| What it says | What it means |
|---|---|
| "This channel is not bound to a circle" | §5 has not been done for this channel, or was done for a different one |
| "bound in a different Discord server" | The binding's `discord_guild_id` is not this guild. Re-bind it with the right one |
| "You are not a member of the circle this channel is bound to" | Your Discord account reaches no live membership there. Sign in to the console with Discord at least once, and check you have not been revoked. Guild membership is not circle membership |
| "Your role in this circle does not hold `tod.report`" | Your role is the one being checked, not the bot's. Ask an officer |
| "Posted only to you: visible replies are not enabled for this channel" | The binding's `allow_visible` is false, which is its default. §6 is what you are deciding if you change it |
| "The application did not respond" (Discord's own message) | The instance did not answer within three seconds, or answered a `5xx`. Check the container is up and read the logs. **Three seconds is a hard budget here**: replying late would mean a follow-up webhook, which is an outbound request law 6 forbids, so there is no deferred reply to fall back on |

## 8. What this still does not fix

**Removing somebody's Discord role does not revoke a personal access token they already hold**, and
a bot built on interactions does not change that: Discord sends us commands, not events, so nothing
here notices a role coming off. ADR-0017 keeps that cost knowingly and names the option that would
have closed it. [discord-app.md's last
section](discord-app.md#what-this-does-not-do-and-you-should-tell-your-officers) is the full
version, including the sentence to say to officers. `revokeMember` is the tool that works,
immediately, on the next request.
