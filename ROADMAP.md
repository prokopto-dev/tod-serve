# Roadmap

**Status: pre-1.0. Phases 0 to 4 are done, the instance layer that came after them is done, and
the software runs.** There is a container image, a Traefik-fronted deployment for a droplet, a path
for running it at home, and an approved deploy that snapshots before it migrates. What has not
happened is a season of real use. `make status` is empty — every declared target does real
work — which says nothing is *stubbed*, not that nothing is *missing*; the list below says what
is missing. This file is what is *planned*.

## Release blockers: none. Known gaps at first deploy: four

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

**Nothing on the list below blocks a first deploy, and every one of them is a thing an operator
would rather hear now than find.** They are written here because "release blockers: none" on its own
invites the reader to supply "and therefore nothing is missing", which is not the same sentence.

1. **Nothing sweeps expired rows.** `auth_flow`, `credential_ticket` and `idempotency_record` all
   carry an `expires_at`, `db/schema.hcl` carries indexes built for the sweep, and sqlc generates
   `DeleteExpiredAuthFlows`, `DeleteExpiredCredentialTickets` and `DeleteExpiredIdempotencyRecords`
   — **which nothing calls.** The rate limiter caps how fast these rows arrive, not how many
   accumulate. Every one is unreadable after expiry, so this is disk, not disclosure, and
   `tod-serve backup` is `VACUUM INTO`. It wants a sweep on the same schedule as `verify-states`.
2. **Traefik is never exercised.** ACME needs public DNS and there is no droplet in CI, so
   `deploy/compose.yaml` is parsed and never run — `deploy/smoke.sh` drives `compose.local.yaml`.
   The TLS front is the one layer whose first real run is on the droplet.
3. **Revoking every administrator re-arms first-run setup.** The window is derived from "nobody
   administers this instance", so emptying the ledger makes a live `TOD_SETUP_TOKEN` a takeover
   credential again. That is the recovery path working as designed and a way to hand an instance
   away by accident. [ADR-0016](docs/adr/0016-first-run-setup-is-an-env-token-and-a-derived-window.md)
   states it as a cost. **Unset `TOD_SETUP_TOKEN` once setup is done.**
4. **No realtime.** The board polls. `subscribeCircleEvents` and `replayCircleEvents` are in the
   route registry and are served by nothing — they are absent from `openapi/openapi.json`, so no
   generated client can call them, and they are the 2 of 27 the tenancy gate reports uncovered.

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

- [x] `db/schema.hcl`, Atlas → goose, the append-only triggers
- [x] `internal/authz/catalogue.go` — the single source for permissions, scopes and roles
- [x] `internal/auth` — opaque PATs, `HMAC-SHA256(pepper, secret)`, sessions, step-up
- [x] `internal/identity` — the provider registry and **all three** implementations: `local`,
  `oidc`, `discord`. The operator-registered OAuth flow (`createAuthorizationURL`,
  `completeAuthorization`, `auth_flow`, `credential_ticket`), the per-circle guild gate, and
  `identity.blocked_at`
- [x] `internal/circle`, `internal/membership` — invites, redemption, revocation, reinstatement
- [x] **The tenancy gates**: schema allowlist, `TEN001`,
      `TestTenancy_CrossCircle_EveryOperationDenies`
- [x] `tod-serve circle create` and first-run owner invite

**All three providers land together.** Splitting them was only ever a hedge against blocker 1, and
with that closed, shipping `discord` late would mean shipping the browser flow twice — `oidc` uses
the same `provider_ticket` path.

## Phase 2 — Reports, consensus, windows

- [x] `internal/tod` — report and quake ingest, retraction, the append-only writes
- [x] `internal/consensus` — the pure derivation, and `test/golden/consensus/*.json`
- [x] `internal/projection` — `target_state_cache`, invalidation, rebuild, the nightly verify job
- [x] `POST /tod-reports`, `GET /tods`, `GET /tods/{id}`, `POST /quakes`

**The golden corpus is authored before the projection layer**, because it is the first executable
check that the design in [03-consensus.md](docs/design/03-consensus.md) is coherent.

## Phase 3 — Catalogue

- [x] `raid_target`, aliases, per-server timers, the resolve ladder
- [x] Embedded target identity — names, zones, expansions — as our own literals
- [x] `tod-serve seed timers --file`, reading the separate `tod-serve-p99-seed` repository
- [x] An unseeded instance must report `no_timer` everywhere and still record ToDs correctly

## Phase 4 — The web console

- [x] The embedded SPA: the board, target detail, members, invites, timer overrides, the audit log,
      device tokens, and the instance console for identity providers and the per-circle guild gate.
      React + Vite + TypeScript + Tailwind, built to `web/dist`, staged into `internal/ui/dist` and
      `go:embed`ed — one binary, no CDN, and a strict deployment needs no outbound network
- [x] API-first, with a gate: `TestAPIParity_EveryConsoleRequest_IsReachableWithAScopedToken`
      reads every `api.<operationId>(` out of `web/src` and drives each one over HTTP with a scoped
      personal access token. Anything refused for a reason that is not the capability floor is a
      browser-only capability and a red build
- [x] The join page: reads the invite code from `location.hash`, POSTs `previewInvite`, clears the
      hash immediately, and shows `revocation_strength` before anybody commits
- [x] `listIdentityProviders`, `createAuthorizationURL` and `completeAuthorization` — the three
      public operations a browser needs before it holds anything. They were in the route registry
      and served by nothing, so there was no way into the instance through a browser at all
- [x] The deployable instance: a `FROM scratch` multi-arch image on GHCR, a Traefik-fronted
      `compose.yaml` for the droplet and a `compose.local.yaml` for everywhere else, `tod-serve
      backup` over `VACUUM INTO`, `tod-serve healthcheck` for an image with no shell in it, the
      console's own CSP, and an approved deploy that snapshots, then pulls, then migrates —
      because a container restart must not. `deploy/smoke.sh` drives the whole first deploy against
      the built image on every CI build, and the runbook names it as the executed version of its
      own walkthrough.

      **`goreleaser` binaries and a systemd unit are deliberately deferred, not done.** Docker is
      the one supported deployment today. They are in the table below with what each would drag in

**The board polls, and that is the whole realtime story for now.** The console revalidates
`listTargetStates` every fifteen seconds with `If-None-Match` and leaves the rendered rows alone on
a `304` — the point of revalidating is to NOT re-render. At a hundred-odd rows that is cheap enough
that a console with no realtime layer is a complete product rather than a degraded one, so
everything in `internal/events` moves to Phase 6 — see below.

## Phase 4b — The instance layer *(landed after this file was first written)*

Not a planned phase. It is here because it shipped, and a roadmap that stops describing the software
two milestones before the deploy is worse than no roadmap. Five ADRs, all already carrying their
mechanisms in [invariants](docs/concepts/invariants.md).

- [x] **The instance-permission ledger** — `instance_grant`, append-only and hash-chained, keyed
      on an **identity** rather than a membership because what it answers outlives any one circle.
      `tod-serve instance grant|revoke|grants|identities` administers it from the console, which is
      where the first one has to come from: on a fresh database nobody holds a credential.
      [ADR-0012](docs/adr/0012-instance-grants-are-a-capability-ledger.md)
- [x] **`instance.owner` implies the instance realm** rather than being enumerated beside it, so a
      permission added to the realm is one the owner already holds and no list has to be widened.
      [ADR-0015](docs/adr/0015-instance-owner-implies-the-instance-realm.md)
- [x] **First-run setup in the browser**, behind `TOD_SETUP_TOKEN`. The window is **derived** —
      open while no identity administers the instance, never a stored flag — and every step is
      create-if-absent so a half-finished run resumes rather than restarting. An unset token and a
      wrong one are the same refusal.
      [ADR-0016](docs/adr/0016-first-run-setup-is-an-env-token-and-a-derived-window.md)
- [x] **The timer invalidation joins the writing transaction**, so a moved window and the boards
      recomputed from it commit together and a failed push rolls the write back.
      [ADR-0013](docs/adr/0013-the-timer-invalidation-joins-the-writing-transaction.md)
- [x] **A deferred read pool for multi-read renders**, so a board never pairs a window with a
      `died_at` from a different timer.
      [ADR-0014](docs/adr/0014-a-deferred-read-pool-for-multi-read-renders.md)

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
| **`goreleaser` binaries** | A downloadable binary per platform is a second supported artefact: signing, checksums, a release-notes pipeline, and a support surface where "it does not start" has no container to reproduce it in. The image is one artefact, built one way, and `deploy/smoke.sh` drives it end to end — a second artefact needs its own equivalent before it is honest to publish |
| **A systemd unit** | It is the sensible non-Docker deployment and it drags in a whole second operational story: a service user, a data directory somebody has to create with the right ownership, log rotation, and a migration step that is deliberate rather than automatic — which is easy in a workflow with an approval gate and awkward in a unit file. Wanted; not free |
| **The loopback CLI login flow** | A headless client cannot use the browser `provider_ticket` path, so `tod-serve login` would need a loopback listener, its own redirect URI registered with the provider, and a story for machines with no browser at all. Non-browser clients use `bearer_token` or `id_token` until then |
