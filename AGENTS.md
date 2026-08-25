# Working in this repository

**Status:** normative. Read this before changing anything. `CLAUDE.md` includes this file.

Much of this codebase is written by AI agents under human review, which is why the repository is
unusually explicit about invariants and unusually aggressive about mechanical enforcement.

**The governing rule: a rule without a gate is a wish.** If you add a rule, add the test, lint rule,
CI gate or database trigger that enforces it, and name it in
[`docs/concepts/invariants.md`](docs/concepts/invariants.md). If you cannot, say so in the PR rather
than writing it down as though it were enforced.

## Where things are

| Path | Holds |
|---|---|
| `cmd/tod-serve/` | The only binary. Cobra wiring, no logic |
| `internal/api/` | Every HTTP route. Huma v2 registration, problem+json, ETag, idempotency |
| `internal/authz/` | **The** catalogue — permissions, scopes, roles, capability floor. Generates the DDL seed, the OpenAPI extensions, the scope enum and the docs page |
| `internal/auth/` | PAT mint and verify, sessions, step-up |
| `internal/identity/{,discord,oidc,local,outbound}/` | Provider registry, credential dispatch, identity and link resolution, the OAuth flow. **The only packages permitted to make outbound HTTP requests**, and `outbound` is the only one that may construct a client |
| `internal/circle/`, `membership/`, `tod/` | Domain services |
| `internal/catalogue/` | Raid-target identity, the per-server timers, the per-circle overrides. **The one resolve ladder** and the one `name_norm` matcher; target identity ships embedded here, timer data ships from nowhere |
| `internal/invite/` | Invite codes: minting, the generous parser a hand-typed code needs, **the one hash** `identitysql` is handed, and the single-use owner grant that gives a circle its first owner |
| `internal/instancegrant/` | The instance-permission ledger: append-only, hash-chained decisions keyed on an **identity**. Its own audit record, because `audit_log.circle_id` is `NOT NULL` |
| `internal/audit/` | The hash-chained append to `audit_log`. Every caller passes a transaction's query set: an audit row that survives a rollback is worse than no row, because it is believed |
| `internal/schemaenum/` | The enum catalogue — every enumerated column, and the ordering rule for the two that have one |
| `internal/dbschema/` | Binds each catalogue enum to the column that holds it, and generates `db/enums.hcl`. Enum `CHECK` lists are never hand-written |
| `internal/consensus/` | **Pure.** Clustering, cluster selection, estimate, confidence, window computation |
| `internal/projection/` | `target_state_cache` maintenance, invalidation, rebuild, nightly verify |
| `internal/store/` | The only holder of `*sql.DB`. `sqlitegen/` is generated and never hand-edited |
| `internal/core/` | `Micros`, ULID, typed ids, the `Server` enum, `Secret` |
| `internal/clock/` | The only `time.Now` |
| `internal/probe/` | The loopback liveness probe the image's `HEALTHCHECK` calls. **The one exception to law 6**, and the only package outside `internal/identity` that reaches the network at all |
| `internal/apierr/` | The closed error-code enum and the RFC 9457 problem the edge renders |
| `internal/repogate/` | The gates that need an AST rather than a grep: `CLOCK001`, `SLEEP001`, `ROUTE001`, `RAND001` |
| `internal/ui/` | The embedded admin console. `//go:embed all:dist`, staged from `web/dist` by `make build-web`. Declares no route — it returns a handler and `internal/api` decides where it sits |
| `web/` | The console: React + Vite + TypeScript + Tailwind. **Not part of the Go module** — `web/go.mod` is what says so, and what stops `./...` compiling whatever Go code the JavaScript dependency tree ships. `web/src/api/generated.ts` is generated from `openapi/openapi.json` and never hand-edited |
| `internal/canondoc/` | Reads fenced blocks out of the normative documents, so a gate compares code against the document rather than against a copy of it |
| `db/` | `schema.hcl` is the single schema truth; `enums.hcl` is generated; `queries/*.sql`; `migrations-sqlite/`, forward-only |
| `deploy/` | The image, the two compose files, `env.example`, the Caddy front for the local TLS profile, and `smoke.sh` — which **is** the first-deploy walkthrough rather than a copy of it |
| `.github/workflows/` | `ci.yml`, `release.yml` (GHCR, multi-arch, cross-compiled) and `deploy.yml` (approved, snapshots, then migrates). Every `run:` block is gated by `ACT001` and `ACT002` |
| `test/repo/` | Tests about the repository itself, not the product: they assert the gates below actually fire |

## The laws

Each has a mechanism. The mechanism is authoritative; this list is a description of it.

1. **HTTP routes are declared only in `internal/api`,** and within it only through the route
   registry: `api.Register` takes an `OperationID`, not a method and a path. `ROUTE001` is an AST
   analyser confining the framework's registration calls to `internal/api/register.go`, so a route
   that carries no permission, no scopes and no tenancy flag cannot be attached at all.
2. **`*sql.DB` is held only by `internal/store`.** Import-graph test; `SQL001`/`SQL002`.
3. **`internal/consensus` is pure** — no store, no `time.Now`, no `math/rand`, **no floats**.
   `PURE001`, `PURE002`, `CLOCK001`, `NOFLOAT001`. The float ban is a reproducibility rule, not a
   money rule: the nightly verify job diffs exact values and a cross-platform float discrepancy would
   make it cry wolf until someone turned it off.
4. **Every circle-scoped table carries `circle_id NOT NULL REFERENCES circle(id)`,** and every
   circle-scoped query names it in the `WHERE`. Schema test against the instance-scoped allowlist,
   plus `TEN001`.
5. **A principal of circle A gets `404` — never `403` — on circle B's resources.**
   `TestTenancy_CrossCircle_EveryOperationDenies`, derived from the route registry so a new uncovered
   route is a red test. **This is the load-bearing gate**; it buys back what
   [ADR-0002](docs/adr/0002-circle-is-the-tenant.md) gave up.
6. **Outbound HTTP only from `internal/identity`,** to allowlisted hosts, and only through the one
   guarded client in `internal/identity/outbound` — `discord` and `oidc` issue the requests, but
   neither may construct a client. `NET001`, plus a dialer that resolves, checks every resolved
   address against a deny list covering private, link-local, loopback and cloud-metadata ranges,
   and then dials the checked address literal so a DNS rebind has no second lookup to win.

   **There is exactly one exception, `internal/probe`, and `PROBE001` is what makes it safe to
   have.** The shipped image is `FROM scratch`: no shell, no curl, so the binary probes its own
   listener for the container `HEALTHCHECK`. That request cannot go through the guarded client,
   because the deny list covers loopback — a probe of our own listener is precisely the request an
   SSRF guard exists to refuse. What makes it safe is that the destination is not an input: the
   PORT comes from the listen address this binary was told to bind, and the HOST is a loopback
   literal the package writes itself, so no variable, flag or row can point it anywhere else.
   `NET001` allows that package by name; `PROBE001` holds it to naming no host and reading no
   configuration; `TestLivenessURL_IsAlwaysLoopback` drives the URL builder over every spelling of
   a listen address, names that resolve elsewhere included.
7. **`web/src` contains no `fetch` or `XMLHttpRequest` outside `web/src/api`.** `tod/no-network-outside-api`,
   a local ESLint rule, plus `WEB001`. Two mechanisms because they fail differently: a lint rule is
   switched off by an `eslint-disable` comment and a grep is not, and the grep runs in the CI job
   with no npm. The rule is the authority — it resolves through the scope, so a property named
   `fetch` is not the global and a shadowing local binding is not either. Beside it,
   `TestAPIParity_EveryConsoleRequest_IsReachableWithAScopedToken` drives every operation the
   console calls with a scoped token: **a capability the browser has and a token cannot reach is a
   red build.** And `WEB002` bans the browser's clock — every countdown is a signed offset from the
   response's `as_of`, because a machine four minutes fast would otherwise render a window that is
   wrong on screen and right in the database. `WEB003` requires a module holding a resource to
   render its staleness: every explicit reload here follows a write, so a refresh that fails
   silently leaves somebody looking at the state from before their own action.
8. **Migrations are forward-only and the report log is never rewritten.** `MIG001` fails a `Down`
   block containing DDL or a file out of sequence; `LOG001` fails an `UPDATE` or `DELETE` against an
   append-only table anywhere in `db/queries` or `db/migrations-sqlite`, which is every route Go has
   to the database. The triggers are the enforcement; these catch the statement before it ships.
9. **`db/queries/*.sql` is ASCII.** `SQLC001`. Not style: sqlc rewrites `sqlc.arg()` by byte offset
   while reporting positions in runes, so one em dash silently mangles every query after it.
10. **Every injected entropy source is `crypto/rand.Reader`.** `RAND001` is an AST analyser: every
    constructor that mints a secret takes its randomness and refuses a nil one, which makes a weak
    source a construction error rather than a review habit — but only makes the choice deliberate at
    the wiring site. The gate requires that site to name `crypto/rand.Reader` itself, not a variable
    holding it and not a wrapper returning it, because "some non-nil reader" is what a nil check
    already bought.

11. **The deployment is described once, and the description is checked.** `deploy/Dockerfile` runs
    `make build` rather than re-spelling it, so the image cannot drift from what CI and a developer
    produce. `ENV001` compares the `TOD_*` constants in `cmd/tod-serve/root.go` against
    `deploy/env.example` and the compose files in both directions — two independent hand-written
    lists, which is what stops it being a tautology — and `IMG001` applies `PIN001`'s reasoning to
    images. `ACT001` and `ACT002` cover the workflow shell that nothing else compiles or runs.
    `deploy/smoke.sh` boots the built image and drives a whole first deploy, and the runbook names
    it as the executed version of its own walkthrough.

    **A container restart must never migrate.** `serve` refuses to start against a database behind
    the migrations it embeds rather than upgrading it, and `/readyz` says which half failed. The
    deploy workflow runs `migrate` as its own step, after a required reviewer approved it. Do not
    add a one-shot migrate service with `service_completed_successfully` — that runs on every `up`,
    restarts included, which is the exact failure the rule exists for.

## Non-negotiable invariants

- **The report log is append-only.** Never `UPDATE` or `DELETE` `tod_report`, `quake_event`,
  `invite_redemption`, `identity_link`, `audit_log` or `event_outbox` — in Go, in SQL, or in a
  migration. Corrections are new rows.
- **Derived state is never authority.** `target_state_cache` is droppable. If you find yourself
  reading it to make a decision the derivation should make, that is the bug.
- **Time is `Micros`.** `died_at` is game truth and may be backdated; `reported_at` is system truth
  and never is. Every response carries `as_of` and every countdown is a signed offset from it.
- **A circle is pinned to one server, immutably.** There is no combined view, anywhere.
- **Revoked members' reports still count**, and their retractions still apply. Revocation controls
  access, never history.
- **`identity_provider.verifiable_subject` is a CHECK against `kind`,** not a toggle. Everything about
  revocation strength hangs off it.
- **The schema is `db/schema.hcl` and nothing else.** Atlas authors the migration from it and
  `make gen` re-runs the diff to prove the file it wrote says the same thing. The one hand-written
  migration is the trigger one, because Atlas Community cannot see triggers — which is also why a
  table rebuild drops them silently, and why `TestAppendOnly_TriggersFire_AfterAllMigrations` asserts
  an abort rather than a row in `sqlite_master`.

## Go idioms

House Go, not general Go. Inherited from Dragon Kill Party, where each rule has a mechanism.

- **Errors:** wrap with `%w` *and* context — `fmt.Errorf("derive state %s: %w", targetID, err)`.
  Context is a lowercase noun phrase, no punctuation. Sentinels live in the owning package. Compare
  with `errors.Is`/`errors.As`, never `==`. Never discard: `_ = f()` is a waiver, not a default, and
  it needs a comment saying why. Never `panic` outside `main` wiring.
- **Context:** `ctx context.Context` is the first parameter of every function that does I/O, with no
  exceptions for ones that "don't need it yet". Never store a `ctx` in a struct field.
  `context.Background()` appears only in `main`, `TestMain` and job-worker roots.
- **The clock is injected, always.** `time.Now` is banned outside `internal/clock`. Time-dependent
  tests use `testing/synctest`; `time.Sleep` is grep-banned in tests.
- **Logging:** `slog`, structured. No `fmt.Printf`, no `log.` package. Never log token secrets,
  session ids, or a Discord access token. The 8-character public token prefix is loggable and is how
  a leaked token is found; the secret never is.
- **Tests:** table-driven, `TestThing_Condition_Expectation`. `t.Parallel()` everywhere. **`require`,
  never `assert`** — `assert` continues after failure and buries the real first failure under a page
  of cascading noise. Whole-value comparisons with `go-cmp` over cherry-picked fields. No mocks of
  the database; integration tests use real SQLite in `t.TempDir()`.
- **Banned:** naked returns, package-level mutable state, `any` in domain signatures, a second type
  for the same concept, manual formatting (`gofumpt` + `goimports` win every disagreement).
- **A type that reaches the OpenAPI document needs a name that is unique across the whole
  repository.** The schema namer strips the package, so `circle.View` and `membership.View` both
  want to be `View` — and `Page[circle.View]` and `Page[membership.View]` both want to be
  `PageView`. The second registration **panics at startup**, which is the good direction for a
  failure and still an hour if you do not know why. It is why the representations are
  `circle.Circle`, `membership.Member` and `invite.Invite` rather than three types called `View`.

## House style for prose

- **Comments say why, not what.** Name the failure the line prevents. A change that removes a reason
  should replace it with a better one.
- **Say when you don't know.** The failure mode designed against throughout is a *confident mistake*,
  not a miss. An unseeded timer reports `no_timer`; a contested ToD says so; confidence is an ordered
  enum because a float would be read as a probability we cannot compute.
- **Never hide a row silently.** If a filter drops something, count it somewhere visible.
- **Write down why not, alongside why.** Every design document names what it rejected.
- 100-column wrap. Tables are fine over it.

## Domain caution

For an unverified P99 log format, add a golden fixture marked `unverified` and open an issue — **do
not invent a regex and ship it.** Guessing a format produces silently wrong ToDs, which is worse than
an error.

The same applies to timer data. Respawn and variance numbers are community-derived and disputed; they
are not bundled, they load from a separate seed repository, and an instance without them says
`no_timer` rather than guessing.

## Working on it

```bash
make help         # every target, documented
make status       # what is still stubbed — derived from notyet call sites, never hand-maintained
make check        # what CI runs
make gen          # regenerate the enum catalogue, the migration, the sqlc bindings, the spec
                  # and the console's TypeScript client
make gen-openapi  # just openapi/openapi.json, which needs neither Atlas nor sqlc
make gen-web      # just web/src/api/generated.ts, from the checked-in openapi/openapi.json
make build-web    # build the console and stage it where go:embed reads it
make lint-web     # the console's ESLint run, its own rule's unit test, and the generated client
make spec-diff    # the spec breaks no client against the base branch, renames included
make test-tenancy # cross-circle isolation over the route registry
make smoke        # build the container image and drive a whole first deploy against it
make seed         # load the embedded raid-target identity. Timers are NOT bundled:
                  # `tod-serve seed timers --file` reads tod-serve-p99-seed
```

Commits are signed off (`git commit -s`, DCO). Conventional Commits are enforced on the **PR title
only** — squash-merge makes it the commit subject. WIP commits can say anything.

Docs change in the same PR as the behaviour they describe.
