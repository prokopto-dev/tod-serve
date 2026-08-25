# Deploying to the DigitalOcean droplet

The target is a droplet already running Traefik, which owns `:80`/`:443` and issues certificates.
tod-serve is one more service on the network Traefik watches; it publishes no ports of its own.

For a machine that is **not** that droplet — a laptop, a home server, a NAS — skip to
[Running it at home](#running-it-at-home). Everything in this project is one binary and one SQLite
file, and none of what follows is a prerequisite for that.

The pipeline is two workflows, split deliberately:

| Workflow | Trigger | What it does | Approval |
|---|---|---|---|
| `Release` | push to `main`, tag `v*` | Builds a multi-arch image, pushes to GHCR | none |
| `Deploy` | successful `Release`, or manual | SSHes to the droplet, ships `compose.yaml`, **snapshots the database**, pulls, **migrates**, `up -d`, verifies | `production` environment |

Building is safe and automatic. Applying an image to the machine holding the only copy of a circle's
report log is neither, which is why it is a separate, approvable step — and why the migration runs
*there* rather than at container start.

> **`serve` does not migrate, and that is load-bearing.** A server that upgrades its schema on boot
> applies a half-tested migration whenever Docker feels like restarting it. Migrations are
> forward-only ([ADR-0006](../adr/0006-atlas-authors-goose-applies.md)), so there is no way back
> from one that ran at 3am with nobody watching. The deploy workflow runs
> `docker compose run --rm tod-serve migrate` as its own step, after a human approved the deploy —
> which is the deliberate act that rule asks for. Do **not** add a one-shot migrate service with
> `service_completed_successfully` to `compose.yaml`: that runs on every `up`, including a restart,
> which is the exact failure the rule exists for.

---

## What you need to set up

### 1. On the droplet — a deploy user and the directories

```bash
# As root on the droplet.
adduser --disabled-password --gecos "" deploy
usermod -aG docker deploy          # needed to run `docker compose`; it is root-equivalent, so this
                                   # user should do nothing else

install -d -o deploy -g deploy -m 750 /opt/tod-serve

# The backups directory is written by the CONTAINER, which runs as uid 65532 and matches nothing on
# the host — so it is owned by that uid, with the deploy user reaching it through the group.
install -d -o 65532 -g deploy -m 770 /opt/tod-serve/backups
```

> **That ownership is load-bearing, and it was checked both ways.** `deploy/compose.yaml`
> bind-mounts `./backups` into the container as `/backups`, which is where `tod-serve backup`
> writes — a root-owned directory makes the snapshot fail with
> `unable to open database: /backups/pre-….db`, and a `65532`-owned one works. The mount uses
> `create_host_path: false`, so a *missing* directory fails `compose up` outright rather than
> letting Docker helpfully create a root-owned one in its place: the default would turn a missing
> directory into a snapshot that fails on the day it is needed.

Copy two files from this repository into `/opt/tod-serve/`:

```bash
# From your workstation, in a checkout of this repository. The hostname is TOD_DEPLOY_HOST — one
# name for SSH and for HTTPS.
scp deploy/compose.yaml  deploy@tod.example.com:/opt/tod-serve/compose.yaml
scp deploy/env.example   deploy@tod.example.com:/opt/tod-serve/.env
```

> **This is the only time you copy `compose.yaml` by hand.** Every deploy ships the version
> that came with the release, overwriting what is there and keeping the previous one as
> `compose.yaml.prev`. It is here at all so you can bring the stack up before the first deploy.
>
> **So a hand-edit to `/opt/tod-serve/compose.yaml` survives until the next deploy and no longer.**
> Change `deploy/compose.yaml` in the repository instead — that is the copy that wins, and it is the
> one anybody reviewing this service will read.
>
> `.env` is the opposite and always will be: it holds secrets, so it stays on the host and no
> pipeline ever writes it — except the one line naming the image. When a release needs a new
> variable, the deploy fails on it *before* swapping anything, naming the variable.

Then, **on the droplet**, fill in `/opt/tod-serve/.env` and lock it down:

```bash
chmod 600 /opt/tod-serve/.env
openssl rand -base64 48     # -> TOD_TOKEN_PEPPER
openssl rand -base64 48     # -> TOD_SESSION_KEY
```

Set `TOD_DEPLOY_HOST` to the name in DNS, and `TRAEFIK_NETWORK` to whatever `docker network ls`
shows Traefik on.

> **`serve` refuses to start on the placeholders.** `deploy/env.example` ships
> `CHANGE_ME_generate_with_openssl_rand_base64_48` for both secrets, and the binary rejects any
> secret beginning with `CHANGE_ME_`, naming `openssl rand -base64 48` in the error. That file is
> published in a public repository, and a working instance running on a value everybody can read is
> the worst thing it could produce.
>
> **The pepper is effectively permanent.** Every credential is stored as
> `HMAC-SHA256(pepper, secret)`, so rotating it invalidates every personal access token at once and
> signs everybody out. Generate it once, back it up somewhere you will still have in a year, and
> change it only if it has leaked. `TOD_SESSION_KEY` is the milder one: rotating it signs everybody
> out of the console and affects no token.

**The secrets stay on the host. They never go through GitHub Actions.** A secret routed through a
pipeline is a secret in that pipeline's logs, its cache, and the context of every pull request from
a fork. CI ships an image; the host holds the credentials.

### 2. On the droplet — access to the image

If the GHCR package is public, nothing to do. If it is private:

```bash
# As the deploy user. Use a PAT with read:packages ONLY.
echo "<github-pat>" | docker login ghcr.io -u <your-github-username> --password-stdin
```

### 3. In this repository — secrets, scoped to the `production` environment

**Create the environment first (step 5), then add these as _environment_ secrets on it** —
`Settings → Environments → production → Environment secrets`.

Not repository secrets. A repository secret is readable by any job in any workflow on the default
branch; an environment secret is readable only by a job that declares `environment: production`,
which is the job that cannot start until you approve it. The SSH key that reaches the droplet should
be gated by the same approval as the deploy itself, otherwise the approval gate protects the
*action* while leaving the *credential* available to anything else that runs.

| Secret | Value | How to get it |
|---|---|---|
| `DEPLOY_SSH_KEY` | The **private** half of a keypair made for this and nothing else | `ssh-keygen -t ed25519 -C "tod-serve-deploy" -f ./tod-serve-deploy -N ""` — paste the contents of `tod-serve-deploy`, then append `tod-serve-deploy.pub` to `/home/deploy/.ssh/authorized_keys` on the droplet |
| `DEPLOY_KNOWN_HOSTS` | The droplet's host key | `ssh-keyscan -t ed25519 tod.example.com` — the **exact string** in `TOD_DEPLOY_HOST`, scanned **once, from a network you trust**; paste the output |

There is no `DEPLOY_HOST`. The deploy SSHes to `vars.TOD_DEPLOY_HOST` (step 4), the same name it
then fetches over HTTPS — one record, one string to keep in step with `DEPLOY_KNOWN_HOSTS`. That
name is a repository **variable** and not a secret: it is in `deploy/env.example`, in this file and
in public DNS, so secrecy would buy nothing, while the log masking a secret brings renders
`ssh: Could not resolve hostname ***` and hides which name was wrong. The credential and the
host-key pin are still secrets, so the posture is unchanged: knowing the hostname gets you as far as
knowing the hostname.

#### Use a hostname, and keyscan that same hostname

A droplet's public IP is not guaranteed stable — a rebuild or a resize can change it. Point an A
record at the droplet and put **that** in `TOD_DEPLOY_HOST`.

The part that bites: `known_hosts` entries are keyed by the exact name used to connect. If you
`ssh-keyscan` the IP and then connect to a hostname, the entry does not match, and the deploy fails
with a host-key error that reads like a man-in-the-middle rather than a configuration mistake — so
the natural reaction is to disable the check, which is the one thing that must not happen. Keyscan
the same string you put in `TOD_DEPLOY_HOST`.

`DEPLOY_KNOWN_HOSTS` is pinned rather than scanned at deploy time on purpose: scanning trusts
whatever answers, which makes every deploy a free chance for a man-in-the-middle. If the droplet is
ever rebuilt it gets a **new host key**, and every deploy will fail on a mismatch until you re-run
`ssh-keyscan`. That failure is correct behaviour: a changed host key is indistinguishable from an
interception, and the only safe response is to re-verify out of band.

### 4. In this repository — variables, at the repository level

`Settings → Secrets and variables → Actions → Variables` — **repository** variables, not
environment ones:

| Variable | Value |
|---|---|
| `DEPLOY_USER` | `deploy` |
| `DEPLOY_PATH` | `/opt/tod-serve` |
| `TOD_DEPLOY_HOST` | `tod.example.com` — the SSH target, the HTTPS host and the environment URL |

These are repository-level for two reasons. None of them is sensitive — a username, a path and a
public hostname — so environment scoping buys nothing. And `TOD_DEPLOY_HOST` is referenced in the
job's `environment.url`, which GitHub evaluates as part of resolving the environment; a variable
defined *on* that environment is not reliably available at that point, so scoping it there can
render the deployment URL blank.

### 5. In this repository — the production environment

`Settings → Environments → New environment → production`.

Add yourself as a **required reviewer**. This is what turns a merge into a build and a deliberate
click into a deploy — and it is what makes the migrate step a decision rather than an accident.
Optionally restrict the environment to the `main` branch and tags.

### 6. First deploy

The image has never run against this volume, so the first time through has two steps the workflow
does not do: creating the instance, and loading the catalogue. Both are one-off, and both are
commands rather than API calls because on a fresh database nobody holds a credential and there is no
principal any HTTP route could authorise.

```bash
gh workflow run Deploy -f image_tag=edge -f source_ref=main
```

Then, **on the droplet, as the deploy user, in `/opt/tod-serve`**:

```bash
# The raid-target catalogue: names, zones, expansions. Embedded in the binary.
docker compose run --rm tod-serve seed targets

# The instance singleton and the first circle. It prints a ONE-TIME owner code.
docker compose run --rm tod-serve init \
  --name "Your Instance" --public-url "https://tod.example.com" \
  --circle "Your Guild" --server blue
```

`init` prints a `TODI-…` code, once, and never stores it. Redeem it at
`https://tod.example.com/join#TODI-…` — that link is built from `--public-url`, which must be the
same origin `TOD_PUBLIC_URL` names, or the join page is somewhere else. Redeeming it makes you the
circle's owner and creates an **identity**; the last bootstrap step hangs off that identity rather
than off the owner role, and `init` prints it too:

```bash
docker compose run --rm tod-serve instance identities
docker compose run --rm tod-serve instance grant --identity <id> --permission instance.owner
```

That grant is what makes the instance administrable over the API, adding the Discord provider
included — see [ADR-0012](../adr/0012-instance-grants-are-a-capability-ledger.md) and
[the Discord walkthrough](discord-app.md).

**Timers are not bundled and never will be.** Respawn and variance numbers are community-derived and
disputed; an instance without them reports `no_timer` everywhere and still records every ToD
correctly, which is the honest degradation. Load them when the seed repository exists:

```bash
docker compose run --rm tod-serve seed timers --file /path/to/seed.json
```

Then check it — all three, in this order, because each failure means something different:

```bash
curl -fsS https://tod.example.com/healthz          # the process is up
curl -fsS https://tod.example.com/readyz           # the database answers, migrations current
curl -fsS https://tod.example.com/api/v1/meta      # the API is serving
```

`/readyz` says *why* when it is not ready. "the database is behind the migrations this binary
embeds" means the migrate step did not run; "the database is not reachable" means the `/data` volume
is missing or unwritable, and the reason is in `docker compose logs`. Neither is fatal to the
process on purpose: `/healthz` stays green so a brief volume problem does not restart-loop a
container that would recover.

The image ships `/data` owned by uid `65532`, and Docker applies that ownership when it initialises
an empty named volume — so the first boot can create the database. **A bind mount gets none of
that**: Docker never changes the ownership of a host directory, so `chown 65532:65532` it yourself
before starting, or the log reads `permission denied` on `/data/tod.db`.

---

## How an update reaches production

1. Merge to `main`. `Release` builds and pushes `:edge` and `:sha-<full>`.
2. `Deploy` is queued and waits for your approval on the `production` environment.
3. On approval it checks out **the commit that release was built from**, copies its
   `deploy/compose.yaml` to the droplet, validates it against the host's `.env`
   (`docker compose config -q`, which fails on a malformed file *and* on any required variable the
   `.env` is missing), and adopts it — keeping `compose.yaml.prev`.
4. **It snapshots the database**, with `tod-serve backup`, into
   `/opt/tod-serve/backups/pre-<timestamp>.db`. If that fails and a volume exists, **the deploy
   stops**. Only "no volume at all — first deploy" proceeds without one.
5. It pins `TOD_DEPLOY_IMAGE` in `.env` to the exact image, pulls, runs `migrate`, and restarts.
6. It polls `/healthz`, `/readyz`, `/api/v1/meta` and the console — **every check retries on a
   bounded budget** — and then asserts the running build is the one just deployed.

Steps 3 and 5 are what stop a release and the stack running it from drifting apart. The compose file
used to be copied once, at setup, and never again — so a release that added a required variable
started against a compose file that had never heard of it.

Step 4 comes **before** the pull for the reason the whole workflow exists: after `compose up` the
old container is gone, and migrations are forward-only, so a failure past that point is a failure
with nothing to fall back on.

Step 6's last check is not ceremony. Without it, a `pull` that silently changed nothing looks
exactly like a successful deploy — every other check passes against the container already there.

Tagged releases (`v1.2.0`) publish `:1.2.0`, `:1.2` and `:latest`. To deploy one:

```bash
gh workflow run Deploy -f image_tag=1.2.0 -f source_ref=v1.2.0
```

### Pairing the image with its compose file

The image and the compose file must come from one commit. The workflow checks out `source_ref` and
ships **that** commit's `deploy/compose.yaml`, rather than the default branch's — because a deploy
waits for a human, `main` keeps moving while it waits, and staging a newer compose file against an
older image would give you a container running with variables from a release it was never built for.
Nothing about that failure looks wrong in the run log.

| `image_tag` | `source_ref` | Verified? |
|---|---|---|
| `sha-<full>` | `<full>` | Yes — the workflow fails if they disagree, and the verify step checks the running version |
| `1.2.0` | `v1.2.0` | Partly — the pairing is not commit-derived, but the running version is checked |
| `edge` | `main` | No, and `edge` moves. Prefer `sha-<full>` for anything reproducible |

Where it cannot verify the pairing, the run says so rather than implying a guarantee it never made.

## Rolling back

Deploys pin an exact image, so a rollback is a deploy of the previous one:

```bash
gh workflow run Deploy -f image_tag=sha-<previous-full-sha> -f source_ref=<previous-full-sha>
```

**Rolling back restores that release's `compose.yaml` too** — the deploy ships the compose file from
`source_ref`, so naming the old commit rolls back both together. The file being replaced is kept as
`compose.yaml.prev`.

**If the bad release included a migration, the image alone is not enough.** Migrations are
forward-only, and an older binary **refuses to start** against a newer schema rather than serving
against columns it does not know about — `/readyz` says exactly that. That refusal is deliberate,
and it is the signal to restore the snapshot the deploy took just before the migration ran. See
[the backup runbook](backup.md#restoring).

## Downtime, and why it is acceptable

`compose up -d` stops the old container before starting the new one, so there are a few seconds of
502. There is no rolling restart, and there should not be: the service is one SQLite writer
([ADR-0001](../adr/0001-go-single-binary-and-sqlite.md)), and two containers sharing that volume is
precisely the situation the design assumes cannot happen.

A few seconds is genuinely fine here. The console revalidates the board every fifteen seconds and
leaves the rendered rows alone on a failure; the nParse+ plugin reports on a kill and retries.
Nobody loses work, and nothing is written that a retry cannot write again.

---

## Running it at home

`deploy/compose.local.yaml` is the same image with no Traefik, no DNS and no ACME: it publishes a
port and that is the whole difference. The hardening is identical.

```bash
cp deploy/env.example .env        # then generate the two secrets; `serve` refuses the placeholders
docker compose -f deploy/compose.local.yaml run --rm tod-serve migrate
docker compose -f deploy/compose.local.yaml run --rm tod-serve seed targets
docker compose -f deploy/compose.local.yaml run --rm tod-serve init \
  --name "Home" --public-url "http://localhost:8080" --circle "Us" --server blue
docker compose -f deploy/compose.local.yaml up -d
```

`deploy/smoke.sh` runs exactly this on every CI build, against the image that would ship. If you
want to know whether these instructions work, that script is the answer — it is this walkthrough,
executed.

### The console needs TLS, and this was measured

The browser session cookie is `__Host-tod_session`: `__Host-` prefixed, `Path=/`, `HttpOnly`, and
unconditionally `Secure` (`internal/auth/session.go`). Whether a browser will *store* that over
plain HTTP is a claim about browsers, so it was tested rather than reasoned about — a page served
over plain HTTP setting three cookies, and a second request reporting which came back:

Measured on **2026-08-25**, on macOS, against a page that set all three at once and a follow-up
request that reported which came back. Browsers change; this is an observation with a date on it,
not a specification. The engines are named by the build strings actually recorded, because those
are what was seen:

| Origin | plain cookie | `Secure` | `__Host-` + `Secure` |
|---|---|---|---|
| `http://localhost:PORT` — Chromium, `AppleWebKit/537.36` (Blink) | stored | stored | **not stored** |
| `http://localhost:PORT` — Safari, `AppleWebKit/605.1.15` (WebKit) | stored | **not stored** | **not stored** |
| `http://localhost:PORT` — Firefox 154 (Gecko) | stored | stored | **stored** |
| `http://127.0.0.1:PORT` — Chromium | stored | stored | **not stored** |
| `http://127.0.0.1:PORT` — Safari | stored | **not stored** | **not stored** |
| `http://<LAN address>:PORT` — Chromium | stored | **not stored** | **not stored** |
| `http://<LAN address>:PORT` — Firefox 154 | stored | **not stored** | **not stored** |

Read that as: **over plain HTTP the console cannot be signed into in Chrome or Safari, and on a LAN
address it cannot be signed into in any of them.** Firefox on `localhost` is the single case that
works, because `__Host-` requires a cryptographically secure scheme and `localhost` is only
*trustworthy*, which is a weaker thing.

The failure is quiet, which is why it is written down here: the console loads, the sign-in
completes, and the browser is still signed out.

Two consequences, and neither is a workaround:

- **The API is unaffected.** A personal access token is an `Authorization` header. The nParse+
  plugin, `curl`, and every script work perfectly well against the plain HTTP port.
- **For the console, use the `tls` profile**, which puts Caddy in front with a certificate from its
  own CA:

  ```bash
  docker compose -f deploy/compose.local.yaml --profile tls up -d
  # then https://localhost:8443 — and set TOD_PUBLIC_URL to that origin
  ```

  Your browser will not trust that certificate until you install the root, which Caddy writes to the
  `caddy-data` volume at `/data/caddy/pki/authorities/local/root.crt`. Clicking through the warning
  also works: the cookie prefix cares that the scheme is `https`, not that the certificate is
  trusted. **The volume must survive restarts** — a regenerated root is a new certificate authority,
  and every browser that trusted the old one then shows a warning that reads exactly like an
  interception.

The local TLS front deliberately does **not** send HSTS. Pinning `localhost` to HTTPS for a year, in
your browser, from a certificate nothing trusts, would break every other project that ever serves on
it.

---

## What is deliberately not here

**Traefik is not tested anywhere.** `deploy/smoke.sh` runs `compose.local.yaml` end to end on every
CI build, and it only ever *parses* `compose.yaml` (`docker compose config -q`). There is no
droplet in CI and ACME needs public DNS, so the routers, the certificate resolver and the HSTS
middleware are verified by the deploy workflow's checks against the real host and by nothing else.
Said plainly
because an untested layer described as though it were covered is worse than one described honestly —
and the specific hazard is known: **naming a certresolver that does not exist is silent.** Traefik
routes the host fine, never obtains a certificate, and serves its self-signed default, so every
client sees a TLS error pointing nowhere near the cause.

**Watchtower or any unattended image updater.** `deploy/compose.yaml` sets
`com.centurylinklabs.watchtower.enable: "false"` explicitly. An automatic swap would either apply a
forward-only migration with nobody watching — except it cannot, because `serve` does not migrate —
so what it actually produces is a container that refuses to start at 3am with `/readyz` saying the
database is behind. If you run Watchtower for other services on this droplet, that label is what
keeps it away from this one. Do not remove it.

**A systemd unit, and `goreleaser` binaries.** Deliberately deferred. Docker is the one supported
deployment today; the roadmap says so rather than implying more.

**Metrics.** `TOD_METRICS_ENABLED` is a literal `"false"` in the compose file, this stack publishes
no port for the metrics listener and gives Traefik no router for it. Turning it on is a change to
`deploy/compose.yaml`, reviewed, together with whatever is meant to scrape it — not a value somebody
sets in `.env`, where it would bind a listener nothing can reach and look like it had worked.
