# Roadmap

**Status: pre-1.0, design phase.** For what is *implemented*, run `make status` — it derives the list
from `notyet` call sites in the Makefile, so it cannot drift from reality. This file is what is
*planned*.

## Release blockers: none

Both of the blockers this project carried — Discord's developer terms, and cross-instance
access-token replay — were consequences of a single assumption: **one project-wide Discord
application shared by every instance.**
[ADR-0011](docs/adr/0011-operator-registered-discord-application.md) removes that assumption. Each
operator registers their own Discord application, so there is no shared app to violate anyone's
terms. Replay closes too, but by an explicit **audience check** the per-instance `client_id` makes
possible — `GET /oauth2/@me` must report our own application — because registration on its own
does not close it. Operational health is per-instance for the same reason.

What it cost is recorded in the ADR and in
[04-identity §7](docs/design/04-identity-and-revocation.md): operator setup friction, a
`client_secret` at rest, and a Discord role removal that does not revoke an already-issued PAT.

## Phase 0 — Scaffold *(this commit)*

The design, the roadmap, and the contract implementation follows. No working software.

- [x] Repository hygiene, licence, DCO, CI skeleton
- [x] [Canonical conventions](docs/design/00-canonical-conventions.md) — the tie-breaker
- [x] [Domain model](docs/design/01-domain-model.md), [API design](docs/design/02-api-design.md), [consensus](docs/design/03-consensus.md), [identity](docs/design/04-identity-and-revocation.md)
- [x] ADRs 0001–0011
- [x] [Invariants](docs/concepts/invariants.md) — every rule with the mechanism that enforces it
- [x] `openapi/openapi.json`, generated from the handlers and checked in; `oasdiff` fails an `operationId` rename

## Phase 1 — Circles, identity, membership

The tenancy and trust machinery, and every gate that guards it. Nothing about ToDs yet, deliberately:
the isolation test has to exist before there is data worth isolating.

- `db/schema.hcl`, Atlas → goose, the append-only triggers
- `internal/authz/catalogue.go` — the single source for permissions, scopes and roles
- `internal/auth` — opaque PATs, `HMAC-SHA256(pepper, secret)`, sessions, step-up
- `internal/identity` — the provider registry and **all three** implementations: `local`, `oidc`,
  `discord`. The operator-registered OAuth flow (`createAuthorizationURL`, `completeAuthorization`,
  `auth_flow`, `credential_ticket`), the per-circle guild gate, and `identity.blocked_at`
- `internal/circle`, `internal/membership` — invites, redemption, revocation, reinstatement
- **The tenancy gates**: schema allowlist, `TEN001`, `TestTenancy_CrossCircle_EveryOperationDenies`
- `tod-serve circle create` and first-run owner invite

**All three providers land together.** Splitting them was only ever a hedge against blocker 1, and
with that closed, shipping `discord` late would mean shipping the browser flow twice — `oidc` uses
the same `provider_ticket` path.

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

## Phase 4 — The web console

- The embedded SPA: the board, invites, the member list, and the admin console for providers, the
  per-circle guild gate and instance-wide blocks. API-first — a test replays the UI's exact requests
  using a scoped token and fails the build if any capability is browser-only
- The join page: reads the invite code from `location.hash`, POSTs `previewInvite`, and clears the
  hash immediately
- Docker image, `goreleaser` binaries, systemd unit

**The board polls.** `listTargetStates` is `GET /tods` with an `ETag` and a `304`, which is cheap
enough at this size that a console with no realtime layer is a complete product rather than a
degraded one. Everything in `internal/events` therefore moves to Phase 6 — see below.

## Phase 5 — The nParse+ plugin

A separate repository, built against the SDK. Holds a list of destinations, ticks which ones a kill
reports to, renders the board as an overlay.

Two things it forces on the server, both already designed for:

- `createTodReport` accepts `target_name` and runs the resolve ladder, so the plugin sends the parsed
  name and **never has to hold a catalogue**.
- Network I/O happens inside `ctx.submit`, and the plugin's HTTP client will trip nParse+'s advisory
  static scan for "HTTP outside the provided clients". That is expected; the scan is a heads-up, not
  a verdict, and the plugin's source is public for exactly this reason.

## Phase 6 — Realtime

Moved out of Phase 4 deliberately. Until a circle is large enough that polling `GET /tods` with an
`ETag` is visibly worse than a push, this subsystem is machinery with no user behind it — and it is
not small machinery: an outbox, a global sequence, fan-out, reconnection, replay and a
goroutine-leak story.

- `internal/events` — `event_outbox`, `event_seq`, SSE fan-out, `goleak.VerifyTestMain`
- `subscribeCircleEvents`, `replayCircleEvents`, the `events:subscribe` scope
- The SPA switches from polling to SSE behind a feature flag, and keeps the polling path as the
  fallback rather than deleting it

The design does **not** move: [ADR-0010](docs/adr/0010-sse-over-websockets.md) stands,
`event_outbox` stays in the schema from Phase 1, and `events:subscribe` stays in the scope
catalogue. Only the implementation is later.

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
| **Continuous Discord role re-checking** | The guild gate is evaluated at join and at re-auth only, so losing a role does not revoke an already-issued PAT — see [04-identity §8](docs/design/04-identity-and-revocation.md). Continuous enforcement needs a bot polling guild membership: a background job, a second set of Discord rate limits, a new failure mode when Discord is unreachable, and a policy decision about what to do when it is. `revokeMember` is the tool that works on the very next request |
| **The loopback CLI login flow** | A headless client cannot use the browser `provider_ticket` path, so `tod-serve login` would need a loopback listener, its own redirect URI registered with the provider, and a story for machines with no browser at all. Non-browser clients use `bearer_token` or `id_token` until then |
