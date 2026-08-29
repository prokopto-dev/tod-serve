# tod-serve

Time-of-death tracking for [Project 1999](https://www.project1999.com/) EverQuest raid targets.
One Go binary, one SQLite file, an embedded web UI, and an API the
[nParse+](https://github.com/prokopto-dev/nparse-plus) plugin drives as a first-class client.

> **Status: pre-1.0. It runs, and nobody has run a raid week on it yet.**
> The API, all three identity providers, circles and membership, ToD ingest, consensus, the
> projection, the catalogue and the embedded web console are implemented, and there is a container
> image and a deployment you can stand up — see [Quickstart](#quickstart). What has not happened is
> a season of real use: no circle has yet found out what this gets wrong on a Tuesday. Treat it as
> software that works and has not been weathered.
>
> For what is *implemented*, run `make status` — it is derived from the Makefile itself, so it
> cannot drift. For what is *planned*, read [ROADMAP.md](ROADMAP.md).

## The problem

P99 raid targets do not spawn on a timer. They spawn in a **window** — a base respawn plus a
randomised variance band — which is why a raid session is six hours of waiting punctuated by a kill.
The single most valuable thing a raiding group shares is therefore not a countdown but an answer to
two questions: *when did it die*, and *where are we in its window now*.

Nothing currently holds that answer.

| | What it does | Why it is not this |
|---|---|---|
| nParse+ respawn timers | Starts a countdown when **you** kill something; shares it group-wide over the PigParse network | Per-character, per-group, and gone when you close it. Not a persistent registry your guild reads tomorrow |
| A pinned Discord message | Someone types `Trak 14:32` | No window maths, no history, no attribution, and it is wrong the moment two people disagree |
| A shared spreadsheet | Actually works, briefly | Last-write-wins. One fat-fingered cell silently overwrites a good ToD and nobody can tell |
| [Dragon Kill Party](https://github.com/prokopto-dev/dragonkillparty) | DKP, attendance, loot, guild ops | Deliberately ships no ToD tracking. It encodes the vocabulary — variance, trackers, race lines, FTE — as context for why raid nights look the way they do |

## What it does

**Records ToDs as an append-only log.** Every report is an immutable row naming who reported it and
where it came from — a parsed log line, a hand-typed correction, a bot. Nothing is ever overwritten.
A correction is a new row; a retraction is a new row. The log *is* the audit trail.

**Derives the answer instead of storing it.** The current ToD for a target is computed from the
reports, not written by whoever spoke last. Reports minutes apart are clustered into one kill; a
log-line timestamp outranks a typed one; a report arriving now about a three-day-old kill does not
supersede yesterday's. Where reporters genuinely disagree, the disagreement is **shown** —
`contested`, with the alternative windows — rather than resolved silently.

**Says how much it knows.** Every answer carries its evidence: how many reports, how many distinct
reporters, how many were log lines, how far apart they were. Confidence is an ordered enum, never a
score, because we cannot compute a probability and a number would be read as one. The failure mode
designed against throughout is a *confident mistake*, not a miss.

**Computes windows, not countdowns.** Given a ToD and a target's respawn definition you get
`open_at`, `close_at`, and where now sits between them — plus a `status` that distinguishes
"we have no ToD" from "we have a ToD but no timer data" from "it is overdue, so something is wrong".
Overdue is real intel, not an error state.

**Handles quakes.** An earthquake repops every raid target on the server at once. That is one event,
not sixty kills nobody witnessed, and modelling it as sixty reports would corrupt every confidence
figure on the board for a week.

## Circles

The unit is a **circle**: any set of people who agree to pool ToDs. A guild is a circle. So are four
friends who share a Nagafen clock. One instance hosts many circles, and one person can belong to
several — the nParse+ plugin holds a list of destinations and you tick which ones a kill reports to.

A destination is `(endpoint, token, circle)`, so two destinations may be two circles on one host or
two hosts entirely. That means you can join a friend's shared instance for one circle and run your
own binary for a circle you would rather nobody else could read. ToDs are competitive intelligence;
whoever operates a host can read every circle on it, and no design at this weight class changes that.

A circle is pinned to **one server** — Blue, Green or Red — permanently. A guild raiding two servers
runs two circles. A Blue ToD says nothing about Green, and there is no row in the schema where a
Blue fact and a Green fact can meet.

## Identity and revocation

Membership binds to an `(provider, subject)` pair, so an instance can offer more than one way in:

| Provider | How you join | Revocation |
|---|---|---|
| `discord` | Browser OAuth against **the operator's own** Discord application — see [ADR-0011](docs/adr/0011-operator-registered-discord-application.md) | **Durable** — banned by Discord id, so a revoked member cannot re-redeem under a new name |
| `oidc` | Any issuer the operator configures — Authentik, Keycloak, Google, GitHub | **Durable**. Uses the same browser flow as `discord`, so the console has one code path, and its `aud` makes cross-instance replay structurally impossible |
| `local` | An invite code and a name you pick yourself | **Advisory** — a revoked person with another invite returns as a new member |

`local` ships disabled and enabling it requires saying so explicitly, because the damage from a weak
revocation is not the re-entry — it is the officers' false belief that revocation worked. A circle
publishes its `revocation_strength` and every client is expected to render it.

A circle may additionally gate on **Discord guild membership and roles** — the instance owns the
application, the circle owns the gate, so two circles on one instance can point at two different
guilds. That gate is checked when someone joins and when they re-authenticate, and **not**
continuously: removing a Discord role does not revoke a token already issued. `revokeMember` is the
thing that takes effect on the very next request.

## Quickstart

One container, one SQLite file, no CDN and no outbound network for the console. `migrate` is a
separate step on purpose: a server that upgraded its schema whenever Docker restarted it would apply
a forward-only migration to the only copy of your report log with nobody watching.

```bash
git clone https://github.com/prokopto-dev/tod-serve && cd tod-serve

# `deploy/.env`, NOT `.env` — compose resolves it from the directory of the first `-f` file.
install -m 600 /dev/null deploy/.env && cp deploy/env.example deploy/.env

# Then edit deploy/.env: three `openssl rand -base64 48` values for TOD_TOKEN_PEPPER,
# TOD_SESSION_KEY and TOD_SETUP_TOKEN, and TOD_DEPLOY_HOST=localhost. The shipped
# placeholders cannot boot, deliberately.

docker compose -f deploy/compose.local.yaml run --rm tod-serve migrate
docker compose -f deploy/compose.local.yaml --profile tls up -d
```

Then open **`https://localhost:8443/setup`** and paste your `TOD_SETUP_TOKEN`. One form creates the
instance, the identity provider, the first circle and the raid-target catalogue, and hands back a
one-time owner code; redeeming it makes you the owner and this instance's first administrator. There
is no `init` and no `seed targets` to run.

**[docs/operations/getting-started.md](docs/operations/getting-started.md) is the full version** —
every command in order, expected output for each, a verification checklist and a troubleshooting
table, for both a laptop and a server behind Traefik. Start there if any of the above surprises you.

`deploy/smoke.sh` runs this on every CI build, against the image that would ship, wizard and owner
code included, and then reports a ToD and reads the board back — so these instructions are executed
rather than asserted.

**Two things worth knowing before you go further:**

- **The console needs HTTPS**, even locally. The session cookie is `__Host-` prefixed, and two of
  three browser engines refuse to store that over plain HTTP — the measurement is in
  [the deployment runbook](docs/operations/deployment.md#the-console-needs-tls-and-this-was-measured).
  That is what `--profile tls` is for above. The API is unaffected: a token is a header.
- **Timers are not bundled.** An instance without them reports `no_timer` everywhere and records
  every ToD correctly, which is the honest degradation rather than a guessed window.

For a real deployment — a droplet, Traefik, GHCR and an approved deploy —
[docs/operations/deployment.md](docs/operations/deployment.md) is the runbook.

## Documentation

| | |
|---|---|
| [Design](docs/design/) | [Canonical conventions](docs/design/00-canonical-conventions.md) is the tie-breaker between any two documents |
| [Domain model](docs/design/01-domain-model.md) | Entities, what is append-only, and why |
| [Consensus](docs/design/03-consensus.md) | How reports become one answer, and what happens when they disagree |
| [Decisions](docs/adr/) | Why things are the way they are, including the downsides |
| [Glossary](docs/concepts/glossary.md) | P99 raiding vocabulary, for contributors who do not play |
| [Invariants](docs/concepts/invariants.md) | Every rule and the mechanism that enforces it |
| [Getting started](docs/operations/getting-started.md) | Clone to signed-in, both at home and behind Traefik. Start here |
| [Deploying](docs/operations/deployment.md) | The droplet, the two workflows, and what is deliberately not covered |
| [Backups](docs/operations/backup.md) | Taking one, checking it, and restoring — the only undo there is |
| [Discord app](docs/operations/discord-app.md) | Registering your own, and what a removed role does not do |

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md). Contributions are under the
[DCO](https://developercertificate.org/) — sign off with `git commit -s`. There is no CLA.

Much of this codebase is written by AI agents under human review, which is why the repository is
unusually explicit about invariants and unusually aggressive about mechanical enforcement. A rule
without a gate is a wish.

## Licence

Code is [Apache-2.0](LICENSE). Documentation is CC BY 4.0. The **name and logo are not licensed** —
forks must rename. See [TRADEMARK.md](TRADEMARK.md).

Timer data is **not bundled**. Respawn and variance numbers are community-derived, genuinely
disputed, and their most convenient source is a wiki whose licence we have not cleared; they load
from a separate, optional, user-run seed repository. An unseeded instance records ToDs correctly and
reports `no_timer` rather than guessing.

Not affiliated with, endorsed by, or connected to Daybreak Game Company, Darkpaw Games, or Project
1999. EverQuest is a trademark of Daybreak Game Company LLC. No game assets are bundled.
