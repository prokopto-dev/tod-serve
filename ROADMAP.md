# Roadmap

**Status: pre-1.0, design phase.** For what is *implemented*, run `make status` — it derives the list
from `notyet` call sites in the Makefile, so it cannot drift from reality. This file is what is
*planned*.

## Two release blockers

Neither is code. Both must be resolved by a human before the relevant piece ships.

| # | Blocker | Blocks |
|---|---|---|
| 1 | **Discord ToS.** One project-wide application, used by arbitrary third-party self-hosted servers, receiving end-user access tokens, may not be within Discord's developer terms. Somebody has to read them. | The `discord` provider. If it is not permitted, `oidc` becomes the recommended default and nothing else in the design moves — which is the payoff for [ADR-0003](docs/adr/0003-pluggable-identity-providers.md) |
| 2 | **Cross-instance token replay accept.** The shared Discord app makes a user's token valid at *every* instance, so a hostile instance can replay it against another. The 60-second freshness rule shrinks the window without closing it. This needs an explicit written accept, not a silent inheritance. | The `discord` provider |

Also unowned: nobody is responsible for the shared Discord application's operational health. A join
storm across many instances makes it a heavily rate-limited client, and a ban hits every instance at
once.

## Phase 0 — Scaffold *(this commit)*

The design, the roadmap, and the contract implementation follows. No working software.

- [x] Repository hygiene, licence, DCO, CI skeleton
- [x] [Canonical conventions](docs/design/00-canonical-conventions.md) — the tie-breaker
- [x] [Domain model](docs/design/01-domain-model.md), [API design](docs/design/02-api-design.md), [consensus](docs/design/03-consensus.md), [identity](docs/design/04-identity-and-revocation.md)
- [x] ADRs 0001–0010
- [x] [Invariants](docs/concepts/invariants.md) — every rule with the mechanism that enforces it
- [ ] `openapi/openapi.json`, generated from the handlers once they exist

## Phase 1 — Circles, identity, membership

The tenancy and trust machinery, and every gate that guards it. Nothing about ToDs yet, deliberately:
the isolation test has to exist before there is data worth isolating.

- `db/schema.hcl`, Atlas → goose, the append-only triggers
- `internal/authz/catalogue.go` — the single source for permissions, scopes and roles
- `internal/auth` — opaque PATs, `HMAC-SHA256(pepper, secret)`, sessions, step-up
- `internal/identity` — the provider registry, `oidc` and `local` implementations
- `internal/circle`, `internal/membership` — invites, redemption, revocation, reinstatement
- **The tenancy gates**: schema allowlist, `TEN001`, `TestTenancy_CrossCircle_EveryOperationDenies`
- `tod-serve circle create` and first-run owner invite

The `discord` provider lands here **only if** blocker 1 is resolved.

## Phase 2 — Reports, consensus, windows

- `internal/tod` — report and quake ingest, retraction, the append-only writes
- `internal/consensus` — the pure derivation, and `test/golden/consensus/*.json`
- `internal/projection` — `target_state_cache`, invalidation, rebuild, the nightly verify job
- `POST /tod-reports`, `GET /tods`, `GET /tods/{id}`, `POST /quakes`

**The golden corpus is authored before the projection layer**, because it is the first executable
check that the design in [03-consensus.md](docs/design/03-consensus.md) is coherent.

## Phase 3 — Catalogue

- `raid_target`, aliases, per-server timers, the resolve ladder
- Embedded target identity — names, zones, expansions — as our own literals
- `tod-serve seed timers --file`, reading the separate `tod-serve-p99-seed` repository
- An unseeded instance must report `no_timer` everywhere and still record ToDs correctly

## Phase 4 — Realtime and the web UI

- `internal/events` — outbox, `event_seq`, SSE fan-out, `goleak`
- The embedded SPA. API-first: a test replays the UI's exact requests using a scoped token and fails
  the build if any capability is browser-only
- Docker image, `goreleaser` binaries, systemd unit

## Phase 5 — The nParse+ plugin

A separate repository, built against the SDK. Holds a list of destinations, ticks which ones a kill
reports to, renders the board as an overlay.

Two things it forces on the server, both already designed for:

- `createTodReport` accepts `target_name` and runs the resolve ladder, so the plugin sends the parsed
  name and **never has to hold a catalogue**.
- Network I/O happens inside `ctx.submit`, and the plugin's HTTP client will trip nParse+'s advisory
  static scan for "HTTP outside the provided clients". That is expected; the scan is a heads-up, not
  a verdict, and the plugin's source is public for exactly this reason.

## Explicitly deferred

Named here so they arrive as projects rather than as small additions.

| Deferred | Why, and what it would drag in |
|---|---|
| **Rotation** | "Our circle claims the 14:00–02:00 Trak window" is the natural next ask. It needs scheduling, conflict resolution, and cross-circle negotiation — a product, not a field on `target_state` |
| **`spawn_sighting`** | A tracker seeing a target already up is real intel, and a false "it's up" is as damaging as a false ToD. It needs its own trust story. When it lands it is a separate append-only table, **not** a nullable `seen_at` on `tod_report` |
| **Per-reporter clock-skew correction** | The most likely source of real-world bad data. See [consensus §9](docs/design/03-consensus.md#9-known-weaknesses). Worth designing before 1.0 |
| **A probability curve over the variance band** | [Rejected outright](docs/adr/0008-windows-are-offsets.md), not merely deferred. We have no distribution data and a rendered curve is read as fact |
| **Postgres** | SQLite is sufficient at a few hundred rows a week. Revisit only with a real use case |
| **Cross-circle federation** | The multi-destination client already lets a person fan out to circles they belong to. Server-to-server sharing is a different trust problem |
