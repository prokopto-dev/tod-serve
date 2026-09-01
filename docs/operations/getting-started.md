# Getting started

From nothing to **signed into the console as the owner of your first circle**. Every command here is
meant to be pasted in the order it appears.

There are two paths and you want exactly one of them:

| | Use it when | Reach it at |
|---|---|---|
| **[A — at home](#path-a--at-home)** | A laptop, a home server, a NAS. No public DNS, no proxy | `https://localhost:8443` |
| **[B — behind Traefik](#path-b--behind-traefik)** | A server on the internet with Traefik already running and owning `:80`/`:443` | `https://<YOUR_DOMAIN>` |

They differ in the compose file and in what has to exist first. The middle — three secrets, a
`migrate` step, one form, one code — is identical, and if you have done one the other is twenty
minutes.

> **Already have a half-configured instance you want to abandon?** Go to
> [Starting over](#starting-over) first. It takes a snapshot before it destroys anything, and you
> want that step even if you think you do not.

## The shape of it

Six things happen, in this order, and every one of them has a reason it is not folded into the one
before it:

1. **Three secrets**, generated once. `serve` refuses to start on the placeholders `env.example`
   ships, deliberately — that file is published in a public repository.
2. **`migrate`, as its own command.** A server that upgraded its schema whenever Docker restarted it
   would apply a forward-only migration to the only copy of your report log with nobody watching.
   `serve` does not migrate, ever. That is load-bearing, not an oversight.
3. **`up`** — against a database holding nothing but the schema.
4. **One form**, at `/setup`, authorised by `TOD_SETUP_TOKEN`. It creates the instance, the identity
   provider, the first circle and the raid-target catalogue, and hands back a one-time owner code.
5. **Redeem that code.** It makes you the circle's owner and — because nobody administers this
   instance yet — this instance's first administrator, in the same transaction.
6. **Delete `TOD_SETUP_TOKEN` and restart**, then check `/setup` answers `404`. It is a takeover
   credential and you are done with it — this step is part of the install, not an optional tidy-up.

There is no `tod-serve init` and no `tod-serve seed targets` in that list. They still exist and are
still the way back when nobody can sign in ([the recovery path](#if-nobody-can-sign-in)), but since
[ADR-0016](../adr/0016-first-run-setup-is-an-env-token-and-a-derived-window.md) a first deploy is
`.env`, `migrate`, `up`, and one form.

### What was actually run to write this

`deploy/smoke.sh` is this walkthrough, executed: CI runs it on every build against the image that
would ship, wizard and owner code included. Beyond that, **Path A below was run start to finish on
2026-08-29** against a clean volume, and the output shown under each step is the output it produced.

**Path B was not run against a live droplet, and blocks that could not be are marked
`unverified on a droplet`.** What was checked locally is that `deploy/compose.yaml` resolves, that a
missing `backups` directory refuses `up` with the error quoted, and that the parts it shares with
Path A behave identically. Traefik, ACME and public DNS are exercised by nothing in CI — the
[deployment runbook](deployment.md#what-is-deliberately-not-here) says so at more length, and the
[troubleshooting table](#troubleshooting) below carries the specific hazard that follows from it.

## Before you start

You need Docker with the Compose v2 plugin, and `openssl` and `curl` (both ship with macOS and every
Linux distribution worth the name):

```bash
docker compose version
openssl version
```

```
Docker Compose version 5.5.0
OpenSSL 3.5.0 1 Apr 2025
```

Any Compose v2 is fine; the version above is just what this was written against.

You do **not** need Go, Node, or a build toolchain. The image is prebuilt and public.

---

## Path A — at home

### A1. Get the compose file

```bash
git clone https://github.com/prokopto-dev/tod-serve
cd tod-serve
```

You are not building anything — `deploy/compose.local.yaml` pulls a published image. The clone is
the easiest way to get that file, `deploy/env.example`, and `deploy/Caddyfile`, which the TLS
profile bind-mounts and which must exist on disk.

### A2. Create `.env` **next to the compose file**

This is the step that most often goes wrong, so it is its own step:

```bash
install -m 600 /dev/null deploy/.env
cp deploy/env.example deploy/.env
chmod 600 deploy/.env
ls -l deploy/.env
```

```
-rw-------  1 you  staff  7602 Aug 29 15:11 deploy/.env
```

**`deploy/.env`, not `.env`.** Docker Compose resolves `.env` from the directory of the first `-f`
file, so a `.env` at the repository root is read by nothing at all. The failure is not subtle, but
it names the variable rather than the location, which sends people looking in the wrong place:

```
error while interpolating services.tod-serve.environment.TOD_TOKEN_PEPPER: required variable
TOD_TOKEN_PEPPER is missing a value: set TOD_TOKEN_PEPPER in .env — see deploy/env.example
```

The `install -m 600` before the `cp` is not decoration: it creates the file `0600` **before** any
secret goes into it, rather than tightening the permissions afterwards on a file that was
world-readable for a moment.

`deploy/.env` is ignored by git, and a test fails the build if any `.env` is ever tracked or if a
generated secret shows up in a committed file. Do not defeat it with `git add -f`.

### A3. Generate the three secrets

Three values, generated once, pasted into `deploy/.env` in place of the `CHANGE_ME_…` placeholders:

```bash
openssl rand -base64 48   # -> TOD_TOKEN_PEPPER
openssl rand -base64 48   # -> TOD_SESSION_KEY
openssl rand -base64 48   # -> TOD_SETUP_TOKEN
```

Open `deploy/.env` in an editor and replace the three placeholder lines so they read:

```
TOD_TOKEN_PEPPER=<PASTE_THE_FIRST_OPENSSL_OUTPUT>
TOD_SESSION_KEY=<PASTE_THE_SECOND_OPENSSL_OUTPUT>
TOD_SETUP_TOKEN=<PASTE_THE_THIRD_OPENSSL_OUTPUT>
```

| | What it does | What rotating it costs |
|---|---|---|
| `TOD_TOKEN_PEPPER` | Keys every credential hash in the database. A stolen database file is useless without it | **Every personal access token stops working and everybody is signed out.** Effectively permanent — back it up somewhere you will still have in a year |
| `TOD_SESSION_KEY` | Signs browser session cookies | Everybody is signed out of the console. No token is affected |
| `TOD_SETUP_TOKEN` | Authorises the first-run wizard at `/setup`, and nothing else | Nothing. **You delete this one at step [A10](#a10-delete-tod_setup_token--do-not-skip-this)** |

**Check you got all three.** `serve` refuses any secret still beginning with `CHANGE_ME_`, and it
refuses at start-up, one variable at a time:

```bash
grep -c '^TOD_.*=CHANGE_ME' deploy/.env
```

```
0
```

If that prints anything other than `0`, you missed one, and the container will exit with:

```
tod-serve: TOD_SESSION_KEY still holds the CHANGE_ME_ placeholder from deploy/env.example:
generate a real one with `openssl rand -base64 48`
```

### A4. Decide the origin now, not later

**Set this before you run the wizard.** The public URL is written into the instance row at setup and
into the join links the server builds; changing it afterwards means two copies that disagree, and
`tod-serve doctor` will tell you so on every run until you fix it.

The default in `env.example` is a real hostname belonging to somebody else's deployment. At home,
point both variables at yourself:

```bash
sed -i.bak 's|^TOD_DEPLOY_HOST=.*|TOD_DEPLOY_HOST=localhost|' deploy/.env && rm -f deploy/.env.bak
printf 'TOD_PUBLIC_URL=https://localhost:8443\n' >> deploy/.env
grep -E '^TOD_(DEPLOY_HOST|PUBLIC_URL)=' deploy/.env
```

```
TOD_DEPLOY_HOST=localhost
TOD_PUBLIC_URL=https://localhost:8443
```

> **Why `https` and port 8443, at home, on a laptop.** The console's session cookie is
> `__Host-tod_session` — `__Host-` prefixed and unconditionally `Secure`. **Two of three browser
> engines refuse to store that over plain HTTP**, and none of the three stores it when the origin is
> a LAN address. Over `http://` the console loads, the sign-in completes, and the browser is still
> signed out; the login button appears to do nothing. The measurement, with dates and build strings,
> is in [the deployment runbook](deployment.md#the-console-needs-tls-and-this-was-measured).
>
> So this path uses the `tls` profile, which puts Caddy in front with a certificate from its own CA.
> `TOD_DEPLOY_HOST` is the name on that certificate — leave it as `tod.prokopto.dev` and the front
> answers for a name you are not typing, and `https://localhost:8443` fails to connect with no
> useful error at all.
>
> **The API does not need any of this.** A personal access token is an `Authorization` header. The
> nParse+ plugin, `curl` and every script work fine against plain `http://localhost:8080` — if you
> only want the API, skip the `--profile tls` below and set
> `TOD_PUBLIC_URL=http://localhost:8080` instead.

Now check the whole file resolves before anything starts:

```bash
docker compose -f deploy/compose.local.yaml config -q
```

Silence and exit 0 is success. This resolves every interpolation, so it fails on a malformed file
*and* on any required variable your `.env` is missing.

### A5. Migrate — its own step, on purpose

```bash
docker compose -f deploy/compose.local.yaml run --rm tod-serve migrate
```

```
time=2026-08-29T19:12:10.753Z level=INFO msg="migration applied" source=000008_instance_grant_unique_hash.sql version=8 took=357.035µs
/data/tod.db is at schema version 8
```

The last line is what you are looking for. Migrations are forward-only and there is no way back from
one that ran unattended, which is why this is a command you type rather than something `serve` does
on boot. Do not add a one-shot migrate service to the compose file: that runs on every `up`,
restarts included, which is the exact failure the rule exists for.

### A6. Bring it up

```bash
docker compose -f deploy/compose.local.yaml --profile tls up -d
```

```
 Container tod-serve-local-tod-serve-1  Started
 Container tod-serve-local-caddy-1      Started
```

Caddy takes a few seconds to mint its certificate on the first run. Until it does,
`https://localhost:8443` refuses the connection rather than answering an error.

### A7. Check it before you open a browser

Three checks, in this order, because each failing means something different:

```bash
curl -k -fsS https://localhost:8443/healthz;      echo
curl -k -fsS https://localhost:8443/readyz;       echo
curl -k -fsS https://localhost:8443/api/v1/meta;  echo
```

```
{"status":"ok","version":"0.0.0-edge+09d1e9f","as_of":"2026-08-29T19:12:16.043250Z"}
{"status":"ready","schema_version":8,"as_of":"2026-08-29T19:12:16.057399Z"}
{"name":"","version":"0.0.0-edge+09d1e9f","api_versions":["/api/v1"],"configured":false,"self_service_circle_creation":false,"setup_available":true,"as_of":"2026-08-29T19:12:16.068663Z"}
```

`-k` because the certificate is Caddy's own and nothing trusts it yet. `"setup_available":true` on
the third line is the instance telling you it has no administrator and the wizard is open.

`/healthz` is the process; `/readyz` is the database and whether the migrations are current;
`/api/v1/meta` is the API serving. If `/readyz` says the database is behind the migrations
the binary embeds, step A5 did not run.

### A8. Run the wizard

Open **`https://localhost:8443/setup`**.

Your browser will warn about the certificate. Click through it — the cookie prefix cares that the
scheme is `https`, not that the certificate is trusted. To make the warning go away for good,
install Caddy's root:

```bash
docker compose -f deploy/compose.local.yaml cp \
  caddy:/data/caddy/pki/authorities/local/root.crt ./caddy-root.crt
```

Then trust `caddy-root.crt` in your OS keychain. **Do not delete the `caddy-data` volume
afterwards** — a regenerated root is a new certificate authority, and every browser that trusted
the old one then shows a warning that reads exactly like an interception.

Paste your `TOD_SETUP_TOKEN` into the form. It asks for:

| Field | At home |
|---|---|
| **Name** | Anything. It is what the join page and console call this instance |
| **Public URL** | `https://localhost:8443` — the same string as `TOD_PUBLIC_URL` |
| **Provider** | `local` unless you have already registered a Discord or OIDC app — see [Identity providers](#identity-providers) |
| **Circle name / server** | Your guild, and `blue`, `green` or `red`. **The server is immutable** |

Choosing `local` makes you tick an acknowledgement that revocation is advisory: nobody can tell the
server an account is gone, so a revoked member holding any live invite comes back under a new name.
That is true, it is why the circle reports `revocation_strength: weak` to its own members, and it is
fixable later by adding a Discord or OIDC provider.

Submitting creates everything and hands back a **one-time owner code**:

```
owner_code   : TODI-XXXXX-XXXXX
join_path    : /join#TODI-XXXXX-XXXXX
circle       : My Guild  (revocation_strength: weak)
raid targets : 54
```

The code is shown **once** and stored nowhere — `tod_meta` holds only its hash. It expires 24 hours
after it is minted. Losing it is not fatal: re-run the wizard and it issues a fresh one, because the
window is still open until somebody redeems one.

**54 raid targets** is target *identity* only. Timers are not bundled, deliberately — see
[A11](#a11-optional-load-timer-data).

### A9. Redeem the owner code

The wizard redirects you straight to `/join#TODI-…`, so there is nothing to copy. Fill in a display
name and submit.

```
role   : owner
circle : My Guild
```

**This is the end of the bootstrap.** Redeeming that code makes you the circle's owner, creates
an identity, and — because nobody administered this instance yet — grants that identity
`instance.owner` in the same transaction. There is no further console step: registering an identity
provider, curating the catalogue and creating another circle all work from that browser session
immediately.

The grant is on an **identity**, never on a membership, so **no personal access token reaches the
admin surface at any scope**. Administering the instance is something you do signed in to the
console.

Confirm it:

```bash
docker compose -f deploy/compose.local.yaml run --rm tod-serve doctor
```

```
tod-serve 0.0.0-edge+09d1e9f — /data/tod.db

  ok       schema version 8
  ok       migrations are up to date
  ok       integrity check passed
  ok       foreign key check passed
  ok       instance "My Instance"
  ok       public URL https://localhost:8443
  warn     provider "local" is local: revocation there is ADVISORY, and every circle accepting it reports revocation_strength=weak
  ok       1 identity can administer this instance (instance.security.manage)
  ok       2 copies of the public URL, all on https://localhost:8443

no problems found
```

The line that matters is **`1 identity can administer this instance`**. The `warn` about `local` is
correct and expected; it is not a failure.

`2 copies of the public URL, all on …` is the check worth reading twice. If those disagree, the
sign-in flow completes and lands nowhere — see the [troubleshooting table](#troubleshooting).

### A10. Delete `TOD_SETUP_TOKEN` — do not skip this

**`TOD_SETUP_TOKEN` is a takeover credential.** Whoever holds it while this instance has no
administrator can make themselves one. You are finished with it; remove it.

```bash
# Capture it BEFORE you delete it — the check below needs the exact value.
SETUP_TOKEN="$(grep '^TOD_SETUP_TOKEN=' deploy/.env | cut -d= -f2-)"

sed -i.bak '/^TOD_SETUP_TOKEN=/d' deploy/.env && rm -f deploy/.env.bak
docker compose -f deploy/compose.local.yaml --profile tls up -d
```

**Now confirm the door is shut rather than assuming it**, presenting the token you just removed:

```bash
grep -c '^TOD_SETUP_TOKEN=' deploy/.env
curl -k -s -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $SETUP_TOKEN" https://localhost:8443/api/v1/setup
```

```
0
404
```

> **Check it WITH the token, never without.** An unauthenticated request to `/api/v1/setup` answers
> `404` on every instance in every state — including one that is wide open with a live token and no
> administrator. That is deliberate: a missing token and a wrong one are one refusal, so a stranger
> cannot tell which instances are worth guessing at. It also means **a bare `curl` proves nothing
> here**, and would tell you the door is shut at the exact moment it is widest open. Measured on
> 2026-08-29 against a fresh instance: unauthenticated `404`, same request with the real token
> `200`.

**The safe end state is all three of these at once:**

1. **No `TOD_SETUP_TOKEN` line in `deploy/.env`** — the `grep -c` above prints `0`.
2. **The stack has been restarted since you deleted it.** Editing `.env` changes nothing until the
   container is recreated; the running process still holds the value it started with.
3. **`/api/v1/setup` answers `404` to the removed token.**

| With the token presented | What it means |
|---|---|
| **`404`** | Finished. The binary no longer holds that token |
| **`409`** | The file is edited but **the container was not recreated** — the token is still live in the running process. Re-run the `up -d` above |
| **`200`** | **Stop.** The token is live *and* nobody administers this instance. Anyone who reaches it can take it over |

> **Why this is not optional, even though something else also guards it.** The setup routes refuse
> everybody once any identity administers the instance, and that condition is *derived from the
> grant ledger* on every request rather than stored as a flag anybody can reset. That is a real
> second defence and it is why a `409` is not an emergency.
>
> It is not a reason to leave the line. **The window this protects is the one before an
> administrator exists** — and it re-opens: revoking every administrator re-arms both doors, so a
> `TOD_SETUP_TOKEN` still sitting in `.env` months from now becomes a live takeover credential again
> the moment the ledger empties. That is the recovery path working as designed
> ([ADR-0016](../adr/0016-first-run-setup-is-an-env-token-and-a-derived-window.md)), and it is also
> how an instance gets handed away by accident. Delete the line.

### A11. Optional: load timer data

**Timers are not bundled and never will be.** Respawn and variance numbers are community-derived and
genuinely disputed. An instance without them records every ToD correctly and reports `no_timer`
everywhere, which is the honest degradation rather than a guessed window.

```bash
docker compose -f deploy/compose.local.yaml run --rm \
  --volume "$PWD/seed:/seed" tod-serve seed timers --file /seed/timers.json
```

`unverified` — there is no published seed file to run it against yet. The verb exists and the flag
is correct; the walkthrough stops where the data does.

### A12. Back it up

`compose.local.yaml` declares no `backups` mount, so pass one:

```bash
mkdir -p backups
docker compose -f deploy/compose.local.yaml run --rm --volume "$PWD/backups:/backups" \
  tod-serve backup --to "/backups/$(date -u +%Y%m%d).db"
```

```
/data/tod.db backed up to /backups/20260829.db
```

Then **open it**, because "it exited 0" is not the same as "there is an undo":

```bash
docker compose -f deploy/compose.local.yaml run --rm --volume "$PWD/backups:/backups" \
  tod-serve doctor --db "/backups/$(date -u +%Y%m%d).db"
```

```
  ok       schema version 8
  ok       migrations are up to date
  ok       integrity check passed
  ok       foreign key check passed
```

> On Docker Desktop, a bind source outside the shared-paths list is **not an error** — the daemon
> creates an empty directory inside its VM and mounts that, so the backup "succeeds" and the file is
> nowhere on your disk. `$PWD` under your home directory is shared; `/tmp` on macOS is not. This was
> hit while writing this page.

Full detail, including restoring: [the backup runbook](backup.md).

---

## Path B — behind Traefik

Everything in this section is run **on the server, by you, over SSH**. No automation reaches it.

> **`unverified on a droplet`.** These blocks were not executed against a live host. What *was*
> run locally on 2026-08-29: `deploy/compose.yaml` resolving, the `backups` mount refusing a
> missing directory with the error quoted below, and every step Path B shares with Path A. DNS,
> ACME and Traefik's routers were not, and nothing in CI exercises them either.
>
> So read the output blocks below in two kinds. Where one is quoted from the local run it says so;
> the rest are **illustrative** — the shape to look for, not a transcript. Check each step's real
> output before moving to the next, and do not treat a mismatch in spacing as a failure.

### B0. What must already be true

- Traefik v3 running on this host, owning `:80`/`:443`, with the Docker provider enabled.
- An external Docker network Traefik watches. `docker network ls` tells you its name.
- A DNS **A record** for `<YOUR_DOMAIN>` pointing at this host's public IP, already propagated.
- You know Traefik's **entrypoint names and certresolver name**. They are not standard, they come
  from Traefik's own static configuration, and [naming one that does not exist fails
  silently](#troubleshooting).

The fastest way to learn those three names is to read the labels on a service already working on
this host. On the reference droplet they are entrypoints `http` and `https`, certresolver `http`,
network `proxy` — which are the defaults `deploy/compose.yaml` falls back to.

### B1. The deploy user and the directories

```bash
# As root.
adduser --disabled-password --gecos "" deploy
usermod -aG docker deploy

install -d -o deploy -g deploy -m 750 /opt/tod-serve

# Written by the CONTAINER, which runs as uid 65532 and matches nothing on this host.
install -d -o 65532 -g deploy -m 770 /opt/tod-serve/backups
```

```bash
ls -ld /opt/tod-serve /opt/tod-serve/backups
```

Illustrative — what matters is the **owner column**: `deploy` on the first, `65532` on the second.

```
drwxr-x--- 2 deploy deploy 4096 … /opt/tod-serve
drwxrwx--- 2  65532 deploy 4096 … /opt/tod-serve/backups
```

**That `65532` is load-bearing and has its own failure mode.** `compose.yaml` bind-mounts
`./backups` with `create_host_path: false`, so a *missing* directory refuses `up` outright rather
than letting Docker create a root-owned one that fails on the day you need it. Verified locally,
this is the error:

```
Error response from daemon: invalid mount config for type "bind":
bind source path does not exist: /opt/tod-serve/backups
```

A root-owned one starts fine and fails later, at snapshot time, with
`unable to open database: /backups/pre-….db`.

### B2. Put the two files in place

From a checkout **on your workstation**:

```bash
scp deploy/compose.yaml deploy@<YOUR_DOMAIN>:/opt/tod-serve/compose.yaml
scp deploy/env.example  deploy@<YOUR_DOMAIN>:/opt/tod-serve/.env
```

This is the only time you copy `compose.yaml` by hand. Once the GitHub Actions pipeline is running,
every deploy ships the copy that came with the release and overwrites this one — so a hand-edit here
survives until the next deploy and no longer. `.env` is the opposite: it holds secrets, it stays on
the host, and no pipeline ever writes it except the one line naming the image.

### B3. Fill in `.env`

```bash
# On the server, as the deploy user.
cd /opt/tod-serve
chmod 600 .env
openssl rand -base64 48   # -> TOD_TOKEN_PEPPER
openssl rand -base64 48   # -> TOD_SESSION_KEY
openssl rand -base64 48   # -> TOD_SETUP_TOKEN
```

Edit `.env` and set, at minimum:

```
TOD_DEPLOY_HOST=<YOUR_DOMAIN>
TOD_DEPLOY_IMAGE=ghcr.io/prokopto-dev/tod-serve:edge
TRAEFIK_NETWORK=<THE_NETWORK_TRAEFIK_WATCHES>
TRAEFIK_ENTRYPOINT_HTTP=<TRAEFIKS_PLAIN_ENTRYPOINT_NAME>
TRAEFIK_ENTRYPOINT_HTTPS=<TRAEFIKS_TLS_ENTRYPOINT_NAME>
TRAEFIK_CERTRESOLVER=<TRAEFIKS_CERTRESOLVER_NAME>
TOD_TOKEN_PEPPER=<PASTE_THE_FIRST_OPENSSL_OUTPUT>
TOD_SESSION_KEY=<PASTE_THE_SECOND_OPENSSL_OUTPUT>
TOD_SETUP_TOKEN=<PASTE_THE_THIRD_OPENSSL_OUTPUT>
```

There is **no `TOD_PUBLIC_URL` line here.** `compose.yaml` derives it as `https://$TOD_DEPLOY_HOST`,
which is why setting `TOD_DEPLOY_HOST` correctly is the step everything downstream hangs off.

Check it, and check the placeholders are gone:

```bash
grep -c '^TOD_.*=CHANGE_ME' .env
docker compose config -q && echo "resolves"
```

```
0
resolves
```

### B4. Register the Discord application

Skip this only if you are starting with `local` identity and adding Discord later.

The redirect URI you register must equal this instance's public URL **character for character**,
and that URL is now fixed by `TOD_DEPLOY_HOST`:

```
https://<YOUR_DOMAIN>/api/v1/auth/callback/discord
```

That is the reason this step sits here and not earlier: before B3 you did not know the string, and
after the wizard you would be reconfiguring rather than configuring.

**Go and do it now: [Registering the Discord application](discord-app.md).** Come back with a
`<YOUR_DISCORD_CLIENT_ID>` and a `<YOUR_DISCORD_CLIENT_SECRET>`. If you have a Discord application
that was half set up and abandoned, that document starts from zero and covers recovering one — do
not assume the redirect URI or the scopes on an existing app are right.

The same ordering applies to an OIDC issuer; you additionally need its issuer, authorization
endpoint and JWKS URI, all of which the wizard asks for.

### B5. Prove the name reaches Traefik, before the container exists

Two different things can be wrong here and they look identical from a browser, so check them
separately. First, routing — without trusting your own resolver, which may hold a stale negative
cache long after the record exists:

```bash
curl -sk -o /dev/null -w '%{http_code}\n' \
  --resolve <YOUR_DOMAIN>:443:<YOUR_SERVER_IP> https://<YOUR_DOMAIN>/healthz
```

```
404
```

**`404` is the correct answer here** and it is the point of running this before you deploy anything:
Traefik is up on the name and has no router for it yet. `000` or a timeout means DNS or the
firewall, not tod-serve.

### B6. Pull, migrate, up

```bash
cd /opt/tod-serve
docker compose pull
docker compose run --rm tod-serve migrate
```

```
/data/tod.db is at schema version 8
```

```bash
docker compose up -d
docker compose ps --format 'table {{.Service}}\t{{.Status}}'
```

```
SERVICE     STATUS
tod-serve   Up 4 seconds (health: starting)
```

`health: starting` becomes `healthy` within about thirty seconds — the image probes its own
listener over loopback, because it is `FROM scratch` and has no shell and no `curl` to do it with.

`migrate` is a separate command here for the same reason it is at home, and it matters more: this
host holds the only copy of a circle's report log. Once the GitHub Actions pipeline is running it
runs `migrate` as its own step, after a human approved the deploy.

### B7. Prove the **certificate** — this is the part that fails silently

A `200` through `-k` or `--resolve` proves routing and proves nothing at all about TLS. Ask who
issued the certificate:

```bash
echo | openssl s_client -connect <YOUR_DOMAIN>:443 -servername <YOUR_DOMAIN> 2>/dev/null \
  | openssl x509 -noout -issuer
```

```
issuer=C=US, O=Let's Encrypt, CN=…
```

The intermediate's name changes over time; **`O=Let's Encrypt` is the part to read.**

| What it says | What it means |
|---|---|
| A Let's Encrypt issuer | Correct. The router came up with its labels and the resolver did its job |
| `TRAEFIK DEFAULT CERT` | **`TRAEFIK_CERTRESOLVER` names a resolver that does not exist.** Nothing else looks broken — the router works, the service answers — and every client sees a TLS error pointing nowhere near the cause. Check the name against Traefik's static configuration |

Then the three checks:

```bash
curl -fsS https://<YOUR_DOMAIN>/healthz;      echo
curl -fsS https://<YOUR_DOMAIN>/readyz;       echo
curl -fsS https://<YOUR_DOMAIN>/api/v1/meta;  echo
```

```
{"status":"ok","version":"0.0.0-edge+…","as_of":"…"}
{"status":"ready","schema_version":8,"as_of":"…"}
{"name":"","version":"0.0.0-edge+…","api_versions":["/api/v1"],"configured":false,"self_service_circle_creation":false,"setup_available":true,"as_of":"…"}
```

### B8. Run the wizard, redeem the code

Open **`https://<YOUR_DOMAIN>/setup`** and paste `TOD_SETUP_TOKEN`.

Identical to [A8](#a8-run-the-wizard) and [A9](#a9-redeem-the-owner-code), with two differences:

- **Public URL** is `https://<YOUR_DOMAIN>`, and it must match what you registered with Discord.
- **Provider** is `discord` (or `oidc`) if you did [B4](#b4-register-the-discord-application).
  The form asks for the client id, the client secret, the redirect URI and the token endpoint.
  Discord's token endpoint is `https://discord.com/api/oauth2/token`. The secret is write-only —
  no operation ever returns it.

Then confirm, on the server:

```bash
docker compose run --rm tod-serve doctor
```

```
  ok       1 identity can administer this instance (instance.security.manage)

no problems found
```

**Grant a second administrator now, while you are here.** The count `doctor` prints exists so that
"one administrator" and "one administrator on holiday" are not the same line:

```bash
docker compose run --rm tod-serve instance identities
docker compose run --rm tod-serve instance grant --identity <THE_SECOND_PERSONS_IDENTITY_ID> \
  --permission instance.owner
```

### B9. Delete `TOD_SETUP_TOKEN` — do not skip this

**This is the step most likely to be lost to "I will do it later".** You have a working instance in
front of you and a browser full of things to look at; the takeover credential is still in `.env`.

```bash
cd /opt/tod-serve
SETUP_TOKEN="$(grep '^TOD_SETUP_TOKEN=' .env | cut -d= -f2-)"   # capture BEFORE deleting

sed -i '/^TOD_SETUP_TOKEN=/d' .env
grep -c '^TOD_SETUP_TOKEN=' .env       # must print 0
docker compose up -d                   # the running process holds the old value until this

curl -s -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $SETUP_TOKEN" https://<YOUR_DOMAIN>/api/v1/setup
```

```
0
404
```

**The `Authorization` header is not optional in that check.** An unauthenticated request answers
`404` on every instance in every state, including a wide-open one — so a bare `curl` would report
success at the exact moment the instance is most exposed.

`404` is the finished state; `409` means the container was not recreated; `200` means stop and read
[A10](#a10-delete-tod_setup_token--do-not-skip-this).

The full reasoning, including why this stays mandatory when a second defence exists and how
revoking every administrator re-arms it, is at [A10](#a10-delete-tod_setup_token--do-not-skip-this).

### B10. Set up daily backups before you walk away

The deploy pipeline snapshots on every deploy, which covers "the deploy broke it" and nothing else.
Add a daily one:

```bash
# /etc/cron.daily/tod-serve-backup   (root, chmod 755)
#!/bin/sh
set -eu
cd /opt/tod-serve
stamp="$(date -u +%Y%m%d)"
docker compose exec -T tod-serve /usr/local/bin/tod-serve backup --to "/backups/daily-${stamp}.db"
find /opt/tod-serve/backups -name 'daily-*.db' -mtime +30 -delete
```

The absolute path to the binary is not optional: `docker exec` does not apply the image's
`ENTRYPOINT`, and this image is `FROM scratch` with no shell and no `PATH` to fall back on.

**And copy them off the machine.** A backup on the same host as the database survives a bad
migration and nothing else. [The backup runbook](backup.md) has the `rsync` line and the restore
procedure.

Back up `.env` too, separately. A restored database is **unreadable without the pepper that keyed
it**: every credential is `HMAC-SHA256(pepper, secret)`, so a database restored under a different
pepper has a valid schema, a complete report log, and not one working token in it.

### B11. Then automate it

Everything above is the by-hand first stand-up. The GitHub Actions pipeline — approved deploys,
snapshots, migrate as its own reviewed step, rollbacks — is a separate setup, and
[the deployment runbook](deployment.md) is that document. Do it once this instance is up and you
have signed into it.

---

## Identity providers

You pick one during the wizard. You are not stuck with it — the console's admin surface adds more
afterwards, and a circle chooses which of them it accepts.

| Provider | How somebody joins | Revocation | Needs before the wizard |
|---|---|---|---|
| `local` | An invite code and a name they type | **Advisory.** A revoked member with another invite returns as a new member | Nothing |
| `discord` | Browser OAuth against **your own** Discord application | **Durable** — banned by Discord id | A registered app: [discord-app.md](discord-app.md) |
| `oidc` | Any issuer you configure — Authentik, Keycloak, Google | **Durable**, and its `aud` makes cross-instance replay structurally impossible | A registered client at that issuer |

**Starting with `local` is a legitimate choice and the circle says so.** Every circle accepting a
`local` provider publishes `revocation_strength: weak` to its own members, and `doctor` warns on
every run. What that costs is not the re-entry — it is officers believing revocation worked. Add
Discord before the circle holds anything competitive.

**What Discord does not buy:** removing somebody's Discord role does **not** revoke a personal
access token they already hold. The guild gate is checked when they join and when they
re-authenticate, not on every request. `revokeMember` is the thing that takes effect on the very
next request. Tell your officers that once, out loud.

---

## Changing instance policy afterwards

The wizard's answers are not final. **Console → Instance admin → Instance policy** reads and
writes them, behind `instance.security.manage` and a re-authenticated session — the same door the
identity providers sit behind, because whoever can add a `local` provider can already let anybody
in.

| Setting | Changeable | What it decides |
|---|---|---|
| **Self-service circle creation** | Yes | This instance's stated answer to "who may create a circle here". It is what `/meta` publishes and what a client reads to decide whether to offer the option. **It is published, not yet enforced:** `createCircle` still requires `instance.circle.create`, whichever way this is set |
| **Name** | Yes | What `/meta`, the join page and the console call this instance |
| **Timezone** | Yes | Display only. Every countdown anywhere is a signed offset from a response's `as_of`, never a local clock |
| **Public URL** | **No** | It must match the redirect URI registered with each provider *exactly*, and it is read at startup from `$TOD_PUBLIC_URL`. Changing it means changing that variable, re-registering the URI with the provider, and restarting — [A4](#a4-decide-the-origin-now-not-later). The API refuses the field rather than accepting a change that would break sign-in at some later restart |

Every change is written to `instance_setting_change`: append-only, hash-chained, and rendered under
the form, so "who turned self-service on, and when" is a question the console answers. Nothing
prunes it.

---

## A second circle

A guild raiding Blue and Green keeps **at least two circles**. There is no combined view of them
anywhere and there never will be — [ADR-0009](../adr/0009-circle-pinned-to-one-server.md), enforced
by a `BEFORE UPDATE` trigger — so a circle cannot be moved to another server after it is created
and the choice of server is the one field on it that is permanent.

**That is not one circle per server.** The limit runs one way only:

| | |
|---|---|
| circle → server | many-to-one, **immutable**. A circle is on exactly one server, for good |
| person → circle | many-to-many, **no per-server limit**. `membership` has no `server` column, and `ux_membership_identity` is unique on `(circle_id, identity_id)` — the circle, not the server |
| what is unique per server | the **name**. `ux_circle_name_norm_server`, on `(name_norm, server)` where `deleted_at IS NULL`. The same name may repeat across servers |

So a raider can hold their guild's Blue circle, an alliance Blue circle and a Green circle: three
memberships, two of them on Blue. The console's switcher shows the name and the server on every
row for exactly that reason, and says so when two of them share a server — a badge reading `BLUE`
settles nothing on its own.

Two ways to make one, and **they are not equivalent**:

| | `tod-serve circle create` | **Console → circle switcher → Create a circle** |
|---|---|---|
| Writes the circle | Yes | Yes |
| Mints the one-time owner code | **Yes** | **No** |
| Needs | the database, no credential | `instance.circle.create` and a re-authenticated session |

`POST /circles` writes the circle row and nothing else: no membership, no owner, no invite. The
one-time owner code that gives a circle its first owner is minted only by `tod-serve circle create`,
and only as part of creating — there is no operation, in the console or in the API, that mints one
for a circle that already exists. **So a circle created from the console has no way in yet**, and
with nobody in it holding `circle.delete` it cannot be removed either. The form says so before the
button rather than after it.

Use the CLI for a circle somebody is meant to join:

```bash
docker compose -f deploy/compose.local.yaml run --rm tod-serve \
  circle create --name "Kittens Who Say Meep — Green" --server green
```

Then hand the printed code to whoever is going to own it, exactly as in
[A9](#a9-redeem-the-owner-code). Switching between circles you belong to is the same control: a
session belongs to one circle, so the switcher signs you in again rather than flipping a filter.

---

## Starting over

For an instance that is half-configured, misconfigured, or that you want to rebuild from zero.

### Take a snapshot first, and check it opens

**Do this even when you are sure the data is worthless.** It costs one command and it is the only
undo that exists — migrations are forward-only, and there is no other copy of the report log.

```bash
# Path A, at home:
mkdir -p backups
docker compose -f deploy/compose.local.yaml run --rm --volume "$PWD/backups:/backups" \
  tod-serve backup --to "/backups/before-wipe-$(date -u +%Y%m%d%H%M).db"
```

```bash
# Path B, on the server, against the running container:
cd /opt/tod-serve
docker compose exec -T tod-serve /usr/local/bin/tod-serve backup \
  --to "/backups/before-wipe-$(date -u +%Y%m%d%H%M).db"
```

```
/data/tod.db backed up to /backups/before-wipe-202608291912.db
```

Then **open it**. A backup nobody has opened is a backup nobody knows restores — and
`tod-serve backup` against an empty volume writes a valid 4 KB database and exits 0, which looks
exactly like success:

```bash
docker compose -f deploy/compose.local.yaml run --rm --volume "$PWD/backups:/backups" \
  tod-serve doctor --db "/backups/before-wipe-<STAMP>.db"
```

```
  ok       schema version 8
  ok       migrations are up to date
  ok       integrity check passed
  ok       foreign key check passed
```

Those four lines are what you need. `doctor` may report the instance row or the administrator count
as a problem on a copy of a half-configured instance; that is about the instance, not the copy.

**Then move it somewhere the wipe cannot reach.** A snapshot inside the volume you are about to
destroy is not a snapshot.

> **The shortcut, and when it applies.** If you are certain this database holds nothing worth
> keeping — a failed first attempt, an instance nobody has ever reported a ToD to — you can skip
> straight to the wipe below. **Only do that when you have checked, not when you assume.** For
> anybody else reading this page, that volume is the only copy of a circle's report log, and a ToD
> history nobody has is a circle starting over.

### Wipe it

```bash
# Path A, at home. --volumes is what destroys the database.
docker compose -f deploy/compose.local.yaml --profile tls down --volumes --remove-orphans
docker volume ls --filter name=tod-serve-local
```

```
 Volume tod-serve-local_tod-data     Removed
 Volume tod-serve-local_caddy-data   Removed
 Volume tod-serve-local_caddy-config Removed
 Network tod-serve-local_default     Removed
```

An empty listing afterwards means the database is gone.

```bash
# Path B, on the server.  unverified on a droplet
cd /opt/tod-serve
docker compose down --volumes --remove-orphans
docker volume ls --filter name=tod-serve
```

Then clear the configuration, so the rebuild starts from the shipped example rather than from
whatever half-state you were in:

```bash
rm -f deploy/.env          # Path A
rm -f /opt/tod-serve/.env  # Path B
```

**If this host has been deployed by the GitHub Actions pipeline before**, also remove the install
marker, or the next deploy will refuse — correctly, because "the volume is gone from a host that has
been deployed before" is normally data loss rather than a fresh start:

```bash
rm -f /opt/tod-serve/.tod-serve-installed   # only if you mean "start over with an empty database"
```

Now re-enter at [A2](#a2-create-env-next-to-the-compose-file) or
[B3](#b3-fill-in-env). **Generate fresh secrets** — a pepper from a database you just destroyed
protects nothing, and reusing a `TOD_SETUP_TOKEN` that has been sitting in a shell history is not a
saving worth having.

### If nobody can sign in

You do not need to wipe for this. `tod-serve init`, `tod-serve circle create` and
`tod-serve instance grant` all still exist, need the database and no credential, and are the
recovery path — an instance whose last administrator was revoked is still administrable from here:

```bash
docker compose -f deploy/compose.local.yaml run --rm tod-serve instance identities
docker compose -f deploy/compose.local.yaml run --rm tod-serve instance grant \
  --identity <AN_IDENTITY_ID> --permission instance.owner
```

Alternatively: revoking every administrator **re-opens the wizard**, because availability is derived
from the grant ledger rather than stored. Set `TOD_SETUP_TOKEN` again, restart, and re-run `/setup`.
That is the recovery path working as designed, and it is also a way to hand an instance away by
accident — which is the honest reason to delete the token when you are done with it.

---

## Verification checklist

Run this when you think you are finished. Every line has a right answer.

```bash
# 1. The process is up.
curl -k -fsS <YOUR_BASE_URL>/healthz
#    -> {"status":"ok", ...}

# 2. The database answers and the migrations are current.
curl -k -fsS <YOUR_BASE_URL>/readyz
#    -> {"status":"ready","schema_version":8, ...}

# 3. The instance knows itself, and the wizard is CLOSED.
curl -k -fsS <YOUR_BASE_URL>/api/v1/meta
#    -> "configured":true  AND  "setup_available":false
```

```bash
# 4. Somebody can administer it, the public URLs agree, and nothing is broken.
docker compose -f deploy/compose.local.yaml run --rm tod-serve doctor
#    -> ok  1 identity can administer this instance
#    -> ok  N copies of the public URL, all on <YOUR_BASE_URL>
#    -> no problems found
```

```bash
# 5. THE SETUP DOOR IS SHUT. Both halves, and the second needs the token.
grep -c '^TOD_SETUP_TOKEN=' <YOUR_ENV_FILE>
#    -> 0            the line is gone from .env

curl -k -s -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer <THE_TOKEN_YOU_REMOVED>" <YOUR_BASE_URL>/api/v1/setup
#    -> 404          finished; the binary no longer holds that token
#    -> 409          THE STACK WAS NOT RESTARTED. The token is still live in the running
#                    process. Re-run `up -d`, then check again.
#    -> 200          STOP. Live token, no administrator: anyone reaching this can take it over.
```

**Send the `Authorization` header.** Without it this route answers `404` on every instance in every
state — including a wide-open one — so a bare `curl` reports the door shut at the moment it is
widest open. That refusal is deliberate (a missing token and a wrong one must be indistinguishable
to a stranger); it just makes an unauthenticated check worthless as *verification*.

**`404` is the finished state, and it is the one line in this checklist worth re-reading.**
`TOD_SETUP_TOKEN` is a takeover credential; the window it protects re-opens if every administrator
is ever revoked, so a token left in `.env` months from now is a live one again.

```bash
# 6. A backup exists, and it opens.
docker compose -f deploy/compose.local.yaml run --rm --volume "$PWD/backups:/backups" \
  tod-serve doctor --db /backups/<YOUR_BACKUP>.db
#    -> integrity check passed
```

And in a browser, signed in: you can see your circle, and the board lists raid targets with
`no_timer` (correct until you load timer data).

Production only, and not runnable from anywhere but the host:

```bash
# 7. The certificate is real, not Traefik's default.
echo | openssl s_client -connect <YOUR_DOMAIN>:443 -servername <YOUR_DOMAIN> 2>/dev/null \
  | openssl x509 -noout -issuer
#    -> a Let's Encrypt issuer, NOT "TRAEFIK DEFAULT CERT"
```

---

## Troubleshooting

| What you see | What it is | Fix |
|---|---|---|
| `required variable TOD_TOKEN_PEPPER is missing a value` from `docker compose config` | Your `.env` is in the wrong directory. Compose resolves `.env` from the directory of the first `-f` file, so a root-level `.env` is read by nothing | Move it to `deploy/.env` — beside the compose file. [A2](#a2-create-env-next-to-the-compose-file) |
| `TOD_SESSION_KEY still holds the CHANGE_ME_ placeholder` and the container exits | You copied `env.example` and missed a secret. This refusal is deliberate: that file is in a public repository | `grep -c '^TOD_.*=CHANGE_ME' deploy/.env` must print `0`. [A3](#a3-generate-the-three-secrets) |
| `bind source path does not exist: /opt/tod-serve/backups` on `up` | The backups directory is missing. `create_host_path: false` makes this refuse rather than create a root-owned directory that fails on the day you need it | `install -d -o 65532 -g deploy -m 770 /opt/tod-serve/backups`. [B1](#b1-the-deploy-user-and-the-directories) |
| `unable to open database: /backups/pre-….db` when a snapshot runs | The backups directory exists but is root-owned. The container is uid 65532 | `chown 65532 /opt/tod-serve/backups`. Does not apply on Docker Desktop |
| Browser shows a TLS error; `openssl x509 -noout -issuer` says **`TRAEFIK DEFAULT CERT`** after deploying | `TRAEFIK_CERTRESOLVER` names a resolver Traefik's static config does not define. **This is silent** — Traefik routes the host perfectly and simply never asks ACME for anything | Check the name against Traefik's static configuration, fix `.env`, `docker compose up -d`. [B7](#b7-prove-the-certificate--this-is-the-part-that-fails-silently) |
| **404** from Traefik | No router matched the host. Either the container is not up, or its labels are wrong. Before you deploy anything this is the *correct* answer and proves the name reaches Traefik | `docker compose ps`; check `TOD_DEPLOY_HOST` and `TRAEFIK_NETWORK` |
| **503** from Traefik | A router matched and every health-checked server behind it is unhealthy | `docker compose logs tod-serve`; check `/readyz` |
| **502** from Traefik | A router matched, the container is up, nothing is listening on the port | Check `TOD_ADDR` and the loadbalancer port label are both `8080` |
| `https://localhost:8443` refuses the connection | Caddy is serving the name in `TOD_DEPLOY_HOST`, which `env.example` ships as somebody else's hostname | `TOD_DEPLOY_HOST=localhost` in `deploy/.env`, then `up -d`. [A4](#a4-decide-the-origin-now-not-later) |
| The console loads, sign-in completes, **you are still signed out** | Plain HTTP. The session cookie is `__Host-` prefixed; Chrome and Safari refuse to store it over `http://`, and no browser stores it on a LAN address | Use the `tls` profile. [A4](#a4-decide-the-origin-now-not-later) |
| `doctor` says *the public URL is written down 2 times and they do not agree* | `TOD_PUBLIC_URL` and the instance row name different origins — usually because the origin changed after the wizard ran | Set `TOD_PUBLIC_URL` back, or change the instance's public URL in the console so all copies agree |
| Sign-in with Discord fails at the *authorization* step with `invalid_request` | The redirect URI registered with Discord does not match what the server sends, character for character | [discord-app.md](discord-app.md). Scheme, host, path, no trailing slash |
| `/readyz` says *the database is behind the migrations this binary embeds* | `migrate` did not run, or an image was upgraded without it | Run `migrate` as its own step. `/healthz` stays green on purpose so a recoverable problem does not restart-loop the container |
| `/api/v1/setup` answers **404** and you sent no token | **This proves nothing.** Unauthenticated, this route answers `404` in every state, wide open included. Re-run the check with `-H "Authorization: Bearer <TOKEN>"` | [A10](#a10-delete-tod_setup_token--do-not-skip-this) |
| `/api/v1/setup` answers **404** with a token you believe is right | Either the token is wrong or it is unset. **These answer identically on purpose** — a refusal that told them apart would tell a stranger which instances are worth guessing at | Check the value in `.env` and that the container was restarted after you set it |
| `/api/v1/setup` answers **409** after you deleted `TOD_SETUP_TOKEN` | **The stack was not restarted.** The running process still holds the token from before the edit | `docker compose … up -d`, then re-check. [A10](#a10-delete-tod_setup_token--do-not-skip-this) |
| `/api/v1/setup` answers **409** with the token still set | The token is right and setup is over: somebody already administers this instance | Expected. Delete the token anyway. If nobody can sign in, [the recovery path](#if-nobody-can-sign-in) |
| Anything about the Traefik layer specifically | **Nothing in CI exercises Traefik, ACME or public DNS** — `deploy/smoke.sh` runs `compose.local.yaml` and only ever *parses* `compose.yaml`. The router, the certificate resolver and the HSTS middleware are verified by the deploy workflow's checks against a real host and by nothing else | Treat the prose in [Path B](#path-b--behind-traefik) as unverified guidance, and check each step's real output. [What is deliberately not here](deployment.md#what-is-deliberately-not-here) |
| The board shows targets but every status is `no_timer` | **Correct.** Timer data is not bundled | [A11](#a11-optional-load-timer-data) |
| A backup "succeeds" and the file is nowhere on your disk | Docker Desktop: a bind source outside its shared-paths list is not an error — it mounts an empty directory inside its VM | Use a path under your home directory |

---

## Where to go next

| | |
|---|---|
| [Backups and restoring](backup.md) | The only undo there is. Read it before you need it |
| [Deployment runbook](deployment.md) | The GitHub Actions pipeline: approved deploys, snapshots, rollbacks, and what is deliberately not covered |
| [Registering the Discord application](discord-app.md) | Your own app, the three scopes, and what a removed role does not do |
| [Permissions](../reference/permissions.md) | Roles, scopes, and what a personal access token can reach |
| [Invariants](../concepts/invariants.md) | Every rule in this project and the mechanism that enforces it |
