# Registering the Discord application

Every instance registers its **own** Discord application
([ADR-0011](../adr/0011-operator-registered-discord-application.md)). There is no shared one, and
that is not an inconvenience the design failed to remove — it is what closes cross-instance
access-token replay, because the callback checks that a token was minted for *this* instance's
`client_id`. An operator who skips this cannot enable Discord sign-in, and the instance says so.

You need this only if a circle wants Discord identity or the per-circle guild gate. `local` works
without it, at a cost this document is explicit about at the end.

## 1. Create the application

<https://discord.com/developers/applications> → **New Application**.

**OAuth2 → Redirects → Add Redirect.** The URI is exactly:

```
https://<TOD_DEPLOY_HOST>/api/v1/auth/callback/discord
```

Three things about that string, each of which has its own failure:

- **It must match what the server sends, character for character** — scheme, host, path, no trailing
  slash. Discord compares it literally, and a mismatch is `invalid_request` at the *authorization*
  step, before anybody has signed in.
- **`TOD_PUBLIC_URL` must be that same origin.** The server derives the join link from it
  (`spaJoinURL` in `cmd/tod-serve/wiring.go`) and **refuses to invent one** — if it is unset and the
  instance row has no public URL, `serve` will not start. Set it in `.env` on the droplet, where
  `deploy/compose.yaml` builds it as `https://$TOD_DEPLOY_HOST`.
- **`https`, not `http`.** The browser session cookie is `__Host-` prefixed, and the measurement in
  [the deployment runbook](deployment.md#the-console-needs-tls-and-this-was-measured) is what that
  costs over plain HTTP.

Copy the **Client ID** and generate a **Client Secret** under OAuth2. The secret is shown once.

## 2. Scopes

The authorization request asks for exactly three, and no more:

| Scope | What it buys | Why it is not optional |
|---|---|---|
| `identify` | The Discord user id — the *subject* every identity link is keyed on | Without it there is no durable identity to revoke |
| `guilds` | `GET /users/@me/guilds`, the list of servers the user is in | The guild gate's first question: are they in the guild at all |
| `guilds.members.read` | `GET /users/@me/guilds/{id}/member`, which returns their ROLES | `guilds` alone does **not** grant this, and role checks need it |

The last two are separate grants and are commonly confused. `guilds.members.read` does not imply
`guilds`, and `guilds` does not imply role visibility. Asking for only what is used is deliberate:
`TestAuthorizationURL_GuildGatedCircle_RequestsGuildsMembersRead` holds the request to the scopes
the callback then reads.

**A user can decline a scope**, and the server tells the difference. A declined scope is
`403 provider_scope_declined`, never `403 guild_role_required` — those point at completely different
fixes, and claiming the second when we were never permitted to look is exactly the confident mistake
this project is built against.

## 3. Configure the provider on the instance

Over the API, with a session holding `instance.security.manage` — see the first-deploy section of
[the deployment runbook](deployment.md#6-first-deploy) for how you get that grant.

```bash
curl -fsS -X POST "https://tod.example.com/api/v1/admin/identity-providers" \
  -H "Content-Type: application/json" -b cookies.txt \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"key":"discord","kind":"discord","display_name":"Discord",
       "client_id":"<CLIENT ID>","client_secret":"<CLIENT SECRET>",
       "redirect_uri":"https://tod.example.com/api/v1/auth/callback/discord",
       "token_endpoint":"https://discord.com/api/oauth2/token","enabled":true}'
```

The console does the same thing with a form. Either way the secret goes in and never comes back
out: `client_secret` is a `core.Secret`, rendered `***` on every path, and
`listIdentityProviders` returns `client_id` and never the secret.

`verifiable_subject` is **not** a field you send. It is a `CHECK` against `kind` in the schema,
which is what makes `revocation_strength` something the server derives rather than something a
caller asserts.

## 4. Finding the guild and role ids

Discord **User Settings → Advanced → Developer Mode**, then right-click → **Copy Server ID** on the
server, and **Copy Role ID** under Server Settings → Roles.

Set them per circle with `setCircleProviders`. The gate is evaluated on **both** `/join` and
`/sessions`, against the facts on the credential ticket rather than a cached copy — and a gate
with no role facts to evaluate **rejects** rather than skipping, because reading an absent role
list as an empty one would disable the gate for every user while appearing to enforce it.

## What this does not do, and you should tell your officers

**Removing somebody's Discord role does not revoke a personal access token they already hold.**

The guild gate is checked when they join and when they re-authenticate. It is not checked on every
request, and there is no bot polling guild membership — continuous enforcement needs a background
job, a second set of Discord rate limits, a new failure mode when Discord is unreachable, and a
policy decision about what to do when it is. That is a project, and the roadmap names it as deferred
rather than implying it is there.

**`revokeMember` is the mechanism that works, and it works immediately, on every request.** Use it.
Removing the Discord role afterwards stops them getting back in; it does not evict them.

The honest summary of what Discord identity buys is therefore: a **durable** subject, so a revoked
member cannot rejoin by making a new account, and a circle whose `revocation_strength` is `durable`
rather than `weak`. It does not buy live role enforcement.

By contrast, `local`'s subject is not verifiable: revoking a member there stops their credential and
does not stop them rejoining under a new one. `tod-serve doctor` says so on every run, and every
circle accepting a `local` provider reports `revocation_strength: weak` to its members before they
commit anything to it.
