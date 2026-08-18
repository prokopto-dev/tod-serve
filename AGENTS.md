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
| `internal/identity/{,discord,oidc}/` | Provider registry, credential dispatch, identity and link resolution. **The only packages permitted to make outbound HTTP requests** |
| `internal/circle/`, `membership/`, `catalogue/`, `tod/` | Domain services |
| `internal/schemaenum/` | The enum catalogue — every enumerated column, and the ordering rule for the two that have one |
| `internal/consensus/` | **Pure.** Clustering, cluster selection, estimate, confidence, window computation |
| `internal/projection/` | `target_state_cache` maintenance, invalidation, rebuild, nightly verify |
| `internal/store/` | The only holder of `*sql.DB`. `sqlitegen/` is generated and never hand-edited |
| `internal/core/` | `Micros`, ULID, typed ids, the `Server` enum, `Secret` |
| `internal/clock/` | The only `time.Now` |
| `internal/repogate/` | The gates that need an AST rather than a grep: `CLOCK001`, `SLEEP001` |
| `internal/canondoc/` | Reads fenced blocks out of the normative documents, so a gate compares code against the document rather than against a copy of it |
| `db/` | `schema.hcl` is the single schema truth; `queries/*.sql`; `migrations-sqlite/` |
| `test/repo/` | Tests about the repository itself, not the product: they assert the gates below actually fire |

## The laws

Each has a mechanism. The mechanism is authoritative; this list is a description of it.

1. **HTTP routes are declared only in `internal/api`.** Route-registry architectural test.
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
6. **Outbound HTTP only from `internal/identity/discord` and `internal/identity/oidc`,** to
   allowlisted hosts. `NET001`, plus a dialer denying private, link-local, loopback and
   cloud-metadata addresses.
7. **`web/src` contains no `fetch` outside `web/src/api`.** ESLint rule plus a CI grep.

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
make help      # every target, documented
make status    # what is still stubbed — derived from notyet call sites, never hand-maintained
make check     # what CI runs
make gen       # regenerate the enum catalogue, OpenAPI and sqlc bindings
```

Commits are signed off (`git commit -s`, DCO). Conventional Commits are enforced on the **PR title
only** — squash-merge makes it the commit subject. WIP commits can say anything.

Docs change in the same PR as the behaviour they describe.
