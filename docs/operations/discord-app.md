# Registering the Discord application

Every instance registers its **own** Discord application
([ADR-0011](../adr/0011-operator-registered-discord-application.md)). There is no shared one, and
that is not an inconvenience the design failed to remove — it is what closes cross-instance
access-token replay, because verification checks that a token was minted for *this* instance's
`client_id`. An operator who skips this cannot enable Discord sign-in, and the instance says so.

You need this only if a circle wants Discord identity or the per-circle guild gate. `local` works
without it, at a cost [the last section](#what-this-does-not-do-and-you-should-tell-your-officers)
is explicit about.

Throughout, replace:

| Placeholder | With |
|---|---|
| `<YOUR_DOMAIN>` | the host this instance is reachable at, e.g. `tod.example.com` |
| `<YOUR_DISCORD_CLIENT_ID>` | the **Client ID** from the Discord developer portal |
| `<YOUR_GUILD_ID>`, `<YOUR_ROLE_ID>` | the ids from [§5](#5-finding-the-guild-and-role-ids) |

**Never paste a client secret into a shell you did not open, a chat window, or an issue.** The
secret is shown once, goes into the instance, and never comes back out.

## Start here: which of these are you?

| You have | Go to |
|---|---|
| No Discord application at all | [§1 — register from zero](#1-no-application-yet-register-one) |
| An application you started and did not finish | [§2 — find what is missing](#2-an-application-that-was-never-finished) |
| An application whose redirect URI is wrong, or points at a domain you no longer use | [§3 — correct the redirect URI](#3-a-wrong-or-dead-redirect-uri) |

Everyone then does [§4](#4-configure-the-provider-on-the-instance), and
[§5](#5-finding-the-guild-and-role-ids) if a circle gates on a guild.

## The one string that has to match

This is the whole of what usually goes wrong, so it comes before the walkthrough.

```
https://<YOUR_DOMAIN>/api/v1/auth/callback/discord
```

That exact string has to appear in **three** places, identical in all three:

1. **Discord → your application → OAuth2 → Redirects.**
2. **`identity_provider.redirect_uri`** on this instance — what you send in
   [§4](#4-configure-the-provider-on-the-instance).
3. **`$TOD_PUBLIC_URL`**, which is the `https://<YOUR_DOMAIN>` part of it. The server builds the
   other two thirds — `/api/v1/auth/callback/` comes from the route registry and `discord` is the
   provider key — so setting `$TOD_PUBLIC_URL` correctly is the whole of your side of it.

Notes, each of which is a real failure somebody has had:

- **`https`, not `http`.** The browser session cookie is `__Host-` prefixed and will not be set
  over plain HTTP.
- **No trailing slash.** `…/discord/` is a different URI to Discord, which compares literally.
- **The last path segment is the provider `key`**, not the word "discord" by coincidence. If you
  register the provider under the key `discord` — and you should — the segment is `discord`.
- **No port unless you are actually serving on one.** `:443` is https's default and may be omitted;
  `:8443` may not.
- Scheme and host are case-insensitive; **the path is not**.

If you are unsure what this instance thinks its own callback is, ask it:

```bash
tod-serve doctor
```

It prints the configured value and the required one side by side for every provider, and exits
non-zero if they differ.

## 1. No application yet: register one

<https://discord.com/developers/applications> → **New Application**. Name it whatever your members
should see on the consent screen.

Then, in order:

1. **OAuth2 → Redirects → Add Redirect.** Paste
   `https://<YOUR_DOMAIN>/api/v1/auth/callback/discord` and **Save Changes**. Discord does not save
   this until you press the button, and a redirect list that looks right on screen but was never
   saved is [state (b)](#2-an-application-that-was-never-finished).
2. **OAuth2 → Client information → Copy Client ID.** This is public; it travels in every
   authorization URL.
3. **OAuth2 → Client information → Reset Secret**, and copy it. **Shown once.** If you lose it,
   reset it again — resetting invalidates the previous one, so do it before you configure the
   instance rather than after.
4. Go to [§4](#4-configure-the-provider-on-the-instance).

**You do not need a bot.** Everything this flow reads — including guild membership and roles —
comes from *user* OAuth scopes on the person signing in. There is no bot token, no gateway
intent, no "Add to Server", and no privileged intent to request. If a half-finished application
has a bot on it, that bot is unused and harmless; you do not have to remove it, and adding one
will not fix a sign-in problem.

## 2. An application that was never finished

This is the common state: an application exists, some of it is configured, and there is no single
screen that tells you which parts are missing. Work down this list in the developer portal, in this
order, and do not skip a line because it "should" be set.

Open <https://discord.com/developers/applications> and select the application.

### 2.1 OAuth2 → Redirects

| What you see | What it means | Do this |
|---|---|---|
| The list is **empty** | The most common half-finished state. Authorization will fail before anybody signs in | Add `https://<YOUR_DOMAIN>/api/v1/auth/callback/discord` and **Save Changes** |
| A URI that is *nearly* right — wrong host, `http://`, trailing slash, `/callback` without the rest | Discord compares literally, so this is the same as empty | [§3](#3-a-wrong-or-dead-redirect-uri) |
| The right URI is there, alongside others | Fine. Extra redirects are not a problem; this flow only ever sends the one | Nothing |
| It looks right but **Save Changes** is still showing | It is not saved. This is the state that wastes the most time, because the screen shows the value you meant | Press **Save Changes** |

### 2.2 OAuth2 → Client information

| What you see | Do this |
|---|---|
| **Client ID** | Copy it. It is public and never changes |
| **Client Secret** with a **Reset Secret** button and no visible value | You do not have the secret. Discord will not show it again. **Reset Secret**, copy the new one, and use it in [§4](#4-configure-the-provider-on-the-instance) — resetting invalidates any older one, so anything already using it stops working |
| You have a secret written down from earlier | Use it. If sign-in later fails with `credential_invalid` at the *token exchange* step, the secret is wrong: reset it and reconfigure |

**A `discord` provider with no client secret is refused**, `422`, at configuration time. It is not
a configuration that half-works: the instance performs the token exchange itself, so without a
secret every sign-in fails — and it fails at the moment somebody clicks the button rather than at
the moment somebody saved the form. If you cannot produce a secret yet, leave the provider
unconfigured rather than saving it half-filled.

### 2.3 Scopes — nothing to configure, and this is the part people over-configure

**There is no scope setting to save on the application.** Scopes are requested per authorization,
and this server decides them. You will see a **URL Generator** on the OAuth2 page; you do not need
it and any URL it produces is not the one this server sends people to.

What this server requests is in [§6](#6-scopes-and-why-there-are-only-two). If an earlier attempt
had you ticking boxes in the URL generator, nothing you ticked there is saved and nothing was
broken by it.

### 2.4 Bot, installation and gateway intents

If a previous attempt created a bot, added an installation context, or asked for privileged
intents: **none of that is used**, and none of it needs to be undone. See the note at the end of
[§1](#1-no-application-yet-register-one).

The one thing worth checking is that you did not put the **bot's** token somewhere expecting it to
be the client secret. They are different values from different sections; a bot token in
`client_secret` fails the token exchange with `credential_invalid`.

### 2.5 Then

Go to [§4](#4-configure-the-provider-on-the-instance). If the provider already exists on the
instance, PATCH it rather than POSTing a second one — there is at most one `discord` provider per
instance, and a second is `409`.

## 3. A wrong or dead redirect URI

Symptom: sign-in either stops at Discord with an error, or appears to work and then goes nowhere.
[The diagnosis table](#what-a-mismatch-looks-like) tells the two apart.

Fix both ends, in this order — the instance first, so that the moment Discord is right, everything
is:

1. **Set `$TOD_PUBLIC_URL` to `https://<YOUR_DOMAIN>`** and restart the service. This is the value
   everything else is derived from. (Where that variable lives is the deployment runbook's; this
   guide only says what it has to equal.)
2. **Update the provider row** to the new redirect URI —
   [§4](#4-configure-the-provider-on-the-instance) shows the PATCH. The server refuses a
   `redirect_uri` that is not its own callback URL, with `422` and the string it wants, so you
   cannot save a wrong one.
3. **In Discord, OAuth2 → Redirects**, remove the old URI, add
   `https://<YOUR_DOMAIN>/api/v1/auth/callback/discord`, **Save Changes**.
4. **`tod-serve doctor`** — it compares the instance row, `$TOD_PUBLIC_URL` and every provider's
   redirect URI, and reports a disagreement as a problem. It cannot see the Discord end; that one
   is only ever confirmed by a real sign-in.

Old sign-ins that were already in flight will fail with `auth_flow_expired`. Starting again is the
whole remedy; nothing is left in a bad state.

## What a mismatch looks like

Two failures, two very different symptoms, and telling them apart is the entire diagnosis.

| Symptom | Which end is wrong | Fix |
|---|---|---|
| You never reach the Discord consent screen. Discord shows its own error page saying the `redirect_uri` is invalid | The instance sends a redirect URI that is **not registered** with the application | Add that exact URI in Discord → OAuth2 → Redirects, and **Save Changes** |
| The console reports a server error as soon as you click "Sign in with Discord", and you never leave the instance | The instance's own `redirect_uri` is **not this instance's callback**. It refuses to start a flow it knows cannot come back | `tod-serve doctor`, then [§3](#3-a-wrong-or-dead-redirect-uri) |
| You sign in and consent at Discord, and then land on a page that is not this instance — or nothing happens at all | The application's registered redirect URI points at a **different or dead host**. This is the one that used to leave no trace, because the callback never reached this server | [§3](#3-a-wrong-or-dead-redirect-uri) |
| You come back to the join page and it says the sign-in took too long | `auth_flow_expired`: a flow is valid for ten minutes | Start again |

For the second row, the server log carries the whole answer:

```
level=ERROR msg="identity provider redirect_uri does not match this instance"
  provider=discord
  configured_redirect_uri=https://old.example.com/api/v1/auth/callback/discord
  expected_redirect_uri=https://<YOUR_DOMAIN>/api/v1/auth/callback/discord
```

Everything else the callback can fail with arrives in the browser as `#error=<code>` on the join
page, and the console renders each one in words. The codes worth recognising:

| Code | Means |
|---|---|
| `credential_invalid` at the token exchange | The `client_secret` is wrong, or was reset since you configured it |
| `credential_audience_mismatch` | The token was minted for a **different instance's** Discord application. This is the replay check doing its job |
| `provider_scope_declined` | The user unticked a permission on the consent screen. **Not** a role failure — see [§6](#6-scopes-and-why-there-are-only-two) |
| `guild_membership_required` | They are not in the guild the circle gates on |
| `guild_role_required` | They are in the guild and hold none of the required roles — **or** we hold no fact either way and will not guess |
| `provider_disabled` | The provider is off on this instance |
| `identity_blocked` | The instance operator has blocked that identity |

## 4. Configure the provider on the instance

Over the API, with a session holding `instance.security.manage`. The console does the same thing
with a form, and prefills the redirect URI from the address you are browsing.

```bash
curl -fsS -X POST "https://<YOUR_DOMAIN>/api/v1/admin/identity-providers" \
  -H "Content-Type: application/json" -b cookies.txt \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"key":"discord","kind":"discord","display_name":"Discord",
       "client_id":"<YOUR_DISCORD_CLIENT_ID>","client_secret":"<PASTE THE SECRET>",
       "redirect_uri":"https://<YOUR_DOMAIN>/api/v1/auth/callback/discord",
       "enabled":true}'
```

To **correct** an existing provider rather than create one, PATCH it by id — there is at most one
`discord` provider per instance, so a second POST is `409`:

```bash
curl -fsS -X PATCH "https://<YOUR_DOMAIN>/api/v1/admin/identity-providers/<PROVIDER ID>" \
  -H "Content-Type: application/json" -b cookies.txt -H "If-Match: *" \
  -d '{"redirect_uri":"https://<YOUR_DOMAIN>/api/v1/auth/callback/discord"}'
```

Notes:

- `token_endpoint` is optional and defaults to Discord's. Setting it to any other host is refused.
- **A `redirect_uri` that is not this instance's callback URL is refused**, `422
  validation_failed` on `body.redirect_uri`, with the exact string it wants in the message. That
  refusal is deliberate: the alternative is a row that saves cleanly and produces a sign-in that
  lands nowhere.
- The secret goes in and never comes back out: `client_secret` is a `core.Secret`, rendered `***`
  on every path, and `listIdentityProviders` returns `client_id` and never the secret.
- `verifiable_subject` is **not** a field you send. It is a `CHECK` against `kind` in the schema,
  which is what makes `revocation_strength` something the server derives rather than something a
  caller asserts.
- A provider is created **disabled** unless you send `"enabled":true`, so a half-configured
  application is not briefly live.

## 5. Finding the guild and role ids

Only needed if a circle gates on a Discord server.

Discord **User Settings → Advanced → Developer Mode**, then right-click the server →
**Copy Server ID**, and Server Settings → Roles → right-click a role → **Copy Role ID**.

Set them per circle with `setCircleProviders`. The gate is evaluated on **both** `/join` and
`/sessions`, against the facts on the credential ticket rather than a cached copy — and a gate
with no role facts to evaluate **rejects** rather than skipping, because reading an absent role
list as an empty one would disable the gate for every user while appearing to enforce it.

An empty required-role list means "anyone in the guild". A non-empty one admits anybody holding
**any** listed role, so the list widens as it grows.

## 6. Scopes, and why there are only two

The authorization request asks for exactly these, and only when they are needed:

| Scope | Requested | What it buys |
|---|---|---|
| `identify` | Always | The Discord user id — the *subject* every identity is keyed on — and `global_name ?? username` for a display name |
| `guilds.members.read` | Only when the circle being joined gates on a guild | `GET /users/@me/guilds/{id}/member`, which answers membership **and** returns the user's roles in that one guild |

**The broader `guilds` scope is deliberately not requested, and this flow never calls
`GET /users/@me/guilds`.** That endpoint returns the user's entire list of Discord servers to
answer a much narrower question, and it does not return roles anyway — so it would be more data,
another consent line, and a second round trip, for less information. One call to the member
endpoint answers both halves for the one guild that matters: `404` is "not in the guild", and a
`200` carries the roles. Members of your other Discord servers are never learned.

`TestAuthorizationURL_GuildGatedCircle_RequestsGuildsMembersRead` holds the request to the scopes
the callback then reads, in both directions.

**A user can decline a scope**, and the server tells the difference. A declined scope is
`403 provider_scope_declined`, never `403 guild_role_required` — those point at completely
different fixes ("grant the permission" versus "go ask an officer for a role you may already
have"), and claiming the second when we were never permitted to look is exactly the confident
mistake this project is built against.

## What this does not do, and you should tell your officers

**Removing somebody's Discord role does not revoke a personal access token they already hold.**

The guild gate is checked when they join and when they re-authenticate. It is not checked on every
request, and there is no bot polling guild membership — continuous enforcement needs a background
job, a second set of Discord rate limits, a new failure mode when Discord is unreachable, and a
policy decision about what to do when it is. That is a project, and the roadmap names it as
deferred rather than implying it is there.

This is the gap most likely to be assumed closed, so it is worth saying to officers in the words
they will act on:

> Taking someone's raider role away stops them getting back in. It does not get them out.

**`revokeMember` is the mechanism that works, and it works immediately, on every request.** Use it.
Membership state is checked on every call, so revocation takes effect without waiting for anything.
Removing the Discord role afterwards is the belt to that braces.

For the harder case — somebody who must not land in *any* circle on this instance, including one
whose officers have never heard of them — the instance operator can block the identity outright.
`identity.blocked_at` is refused at join and at ticket redemption, so a blocked identity cannot use
even a ticket it already holds.

The honest summary of what Discord identity buys is therefore: a **durable** subject, so a revoked
member cannot rejoin by making a new account, and a circle whose `revocation_strength` is `durable`
rather than `weak`. **It does not buy live role enforcement.**

By contrast, `local`'s subject is not verifiable: revoking a member there stops their credential
and does not stop them rejoining under a new one. `tod-serve doctor` says so on every run, and
every circle accepting a `local` provider reports `revocation_strength: weak` to its members before
they commit anything to it.
