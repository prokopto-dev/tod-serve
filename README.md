# tod-serve

Time-of-death tracking for [Project 1999](https://www.project1999.com/) EverQuest raid targets.
One Go binary, one SQLite file, an embedded web UI, and an API the
[nParse+](https://github.com/prokopto-dev/nparse-plus) plugin drives as a first-class client.

> **Status: pre-1.0, design phase. Do not run your raid week on this yet.**
> There is no working software in this repository. What exists is the design, the roadmap, and the
> contract that implementation follows. For what is *implemented*, run `make status` — it is derived
> from the Makefile itself, so it cannot drift. For what is *planned*, read [ROADMAP.md](ROADMAP.md).

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
| `discord` | Loopback OAuth against a project-wide app, `identify` scope only | **Durable** — banned by Discord id, so a revoked member cannot re-redeem under a new name |
| `oidc` | Any issuer the operator configures — Authentik, Keycloak, Google, GitHub | **Durable**, and the only provider immune to cross-instance token replay |
| `local` | An invite code and a name you pick yourself | **Advisory** — a revoked person with another invite returns as a new member |

`local` ships disabled and enabling it requires saying so explicitly, because the damage from a weak
revocation is not the re-entry — it is the officers' false belief that revocation worked. A circle
publishes its `revocation_strength` and every client is expected to render it.

## Quickstart

Nothing to start yet. When Phase 1 lands this section describes `docker run` and a downloaded
binary, both creating their own data directory and migrating themselves. See
[ROADMAP.md](ROADMAP.md).

## Documentation

| | |
|---|---|
| [Design](docs/design/) | [Canonical conventions](docs/design/00-canonical-conventions.md) is the tie-breaker between any two documents |
| [Domain model](docs/design/01-domain-model.md) | Entities, what is append-only, and why |
| [Consensus](docs/design/03-consensus.md) | How reports become one answer, and what happens when they disagree |
| [Decisions](docs/adr/) | Why things are the way they are, including the downsides |
| [Glossary](docs/concepts/glossary.md) | P99 raiding vocabulary, for contributors who do not play |
| [Invariants](docs/concepts/invariants.md) | Every rule and the mechanism that enforces it |

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
