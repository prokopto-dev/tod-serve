# Invariants

**Status:** design phase. Every mechanism below lands with the subsystem it guards; the **Lands in**
column says when. A row with no phase is enforceable today.

A rule without a mechanism is a wish. Every rule on this page names the thing that enforces it — a
database trigger, a lint rule, a test, or a CI gate — because a rule enforced only by review is a
rule that survives until the first tired Friday.

## Data invariants

| Invariant | Enforced by | Lands in |
|---|---|---|
| `tod_report`, `quake_event`, `invite_redemption`, `identity_link`, `audit_log` and `event_outbox` are never updated or deleted | `BEFORE UPDATE OR DELETE … RAISE(ABORT)` triggers, **plus** `TestAppendOnly_TriggersFire_AfterAllMigrations` — table rebuilds drop triggers, and that test is how you find out | Phase 1 |
| A report may be retracted at most once | A unique index on `retracts_report_id` | Phase 2 |
| Every table is `STRICT` | Atlas generates the DDL from `db/schema.hcl`; `PRAGMA integrity_check` then verifies column content types | Phase 1 |
| Every circle-scoped table carries `circle_id NOT NULL REFERENCES circle(id)` | A schema test enumerating tables against the explicit instance-scoped allowlist in [canonical §9](../design/00-canonical-conventions.md#9-tenancy--this-project-diverges-from-dkp) | Phase 1 |
| Every circle-scoped query names `circle_id` in its `WHERE` | `TEN001` over `db/queries/*.sql`, same allowlist | Phase 1 |
| The instance-scoped allowlist in [canonical §9](../design/00-canonical-conventions.md#9-tenancy--this-project-diverges-from-dkp) and `INSTANCE_SCOPED` in `scripts/repo-gates.sh` are the same list | `TestInstanceScopedAllowlist_MatchesRepoGates`, which parses both and compares them in **each** direction. Two hand-maintained copies of one fact is exactly the drift this repository gates against everywhere else, and the copy that silently grows is the one that stops a table being tenancy-checked | Phase 1 |
| A `credential_ticket` is redeemable **once**, at either `/join` or `/sessions` | A `consumed_at` write in the same transaction as the redemption, on a unique index; `TestCredentialTicket_SecondRedemption_Refused` — `401 auth_ticket_invalid` | Phase 1 |
| A `credential_ticket` expires 120 seconds after it is minted | `expires_at` checked against the injected clock at redemption; `TestCredentialTicket_After120s_Refused` under `testing/synctest` — `401 auth_ticket_expired` | Phase 1 |
| An identity with `blocked_at` set cannot join **any** circle on the instance | `TestJoin_BlockedIdentity_Refused` — checked at join *and* at ticket redemption, so a second circle is not a second door. `403 identity_blocked` | Phase 1 |
| `identity_provider.client_id` is present exactly when `kind = 'discord'` | `CHECK ((kind = 'discord') = (client_id IS NOT NULL))` — a Discord provider row with no operator application is unrepresentable, not merely invalid | Phase 1 |
| `circle.server` never changes after creation | A `BEFORE UPDATE` trigger, plus `422 field_immutable` at the edge | Phase 1 |
| A revoked identity cannot rejoin the same circle | The partial unique index `ux_membership_identity`, which makes a second membership row unrepresentable — there is no delete-membership operation at all | Phase 1 |
| An `identity_link` participant has `verifiable_subject = 1` | A DB trigger plus `TestIdentityLink_LocalProvider_Rejected` | Phase 1 |
| `identity_provider.verifiable_subject` matches its `kind` | A `CHECK` constraint — it is not an operator toggle | Phase 1 |
| Enum values are identical in the SQL `CHECK`, the JSON and the OpenAPI | One Go catalogue in `internal/schemaenum`; `make gen` writes all three; a test asserts the copies agree | Phase 1 |
| A migration that has shipped in a tagged release is never edited | CI compares migration checksums against the previous release | Phase 1 |
| Migrations are forward-only | Every `Down` block contains `RAISE(ABORT, …)`; `lint / repo` fails any migration whose `Down` contains DDL | Phase 1 |

## Consensus invariants

| Invariant | Means | Enforced by |
|---|---|---|
| `Pure` | `internal/consensus` imports no store, no `time.Now`, no `math/rand` | `PURE001`, `PURE002`, `CLOCK001` |
| `NoFloat` | No `float32`/`float64` anywhere in the window computation, because a boundary computed in float is not bit-identical across platforms and the nightly verify job diffs exact values | `NOFLOAT001` plus a `golangci-lint` rule on the package |
| `Deterministic` | The same inputs produce byte-identical output on every platform | The golden corpus, run on every CI platform |
| `ClusterSpanBounded` | No cluster spans more than 2ε, so reports cannot chain into a fictitious multi-hour kill | A property test over generated report sequences |
| `LatestDiedAtWins` | The current cluster is the one with the latest `died_at`, never the most recently reported | Golden fixture `backfilled_older_report.json` |
| `ObservationNeverVetoed` | A physically implausible `died_at` is flagged, never rejected — derived state must not veto an observation | Golden fixture `implausible_ordering.json` |
| `RevokedReportsCount` | A revoked member's reports still contribute, and their retractions still apply | Golden fixture `revoked_reporter.json` |
| `CacheIsNotAuthority` | A nightly job recomputes every state from the reports and diffs; the recomputation wins and an alert fires | `internal/jobs`, plus an integration test that corrupts a cache row and asserts the job repairs it |

## Architectural laws

| Law | Enforced by | Lands in |
|---|---|---|
| HTTP routes are declared only in `internal/api` | An architectural test walking the route registry | Phase 1 |
| `*sql.DB` is held only by `internal/store` | An import-graph test; `SQL001`/`SQL002` | Phase 1 |
| `internal/consensus` is pure | See the consensus table above | Phase 2 |
| Outbound HTTP originates only from `internal/identity/discord` and `internal/identity/oidc`, to allowlisted hosts | `NET001` grepping `http.Get`, `http.Client` and `net.Dial` outside those packages, plus a unit test on the dialer's deny list — which must deny link-local and cloud-metadata addresses, not merely RFC1918 | Phase 1 |
| `web/src` contains no `fetch` or `XMLHttpRequest` outside `web/src/api` | An ESLint rule plus a CI grep | Phase 4 |
| `time.Now` appears only in `internal/clock` | `CLOCK001`, an AST analyser, so an aliased import does not defeat it | Phase 1 |

## API invariants

| Invariant | Enforced by | Lands in |
|---|---|---|
| A principal of circle A gets `404` — never `403` — on every circle-scoped operation against circle B | `TestTenancy_CrossCircle_EveryOperationDenies`, derived from the **route registry** rather than a hand-written list, so a new uncovered route is a red test | Phase 1 |
| Every operation declares `Security` and `x-tod-permission` | A spec lint plus an architectural test asserting the declared scope set matches what the middleware checks | Phase 1 |
| Every operation has an explicit `operationId` in `lowerCamelCase`, never renamed | Spec lint for presence; `oasdiff` fails a rename as a breaking change | Phase 1 |
| Every POST that creates domain state requires `Idempotency-Key` | An architectural test over the route registry | Phase 1 |
| Errors are RFC 9457 with a `code` from a closed enum, and never HTTP 200 with an error body | Response-validation middleware run across the whole integration suite | Phase 1 |
| Every error code has a documentation page | A Go test enumerating the enum against the docs tree | Phase 1 |
| The capability floor cannot drift from the canonical conventions | `TestCapabilityFloor_MatchesCanonicalConventions` parses the fenced block in [canonical §6](../design/00-canonical-conventions.md#6-permissions-and-scopes--one-catalogue-generated) and compares element by element, both directions | Phase 1 |
| No token appears in a URL | Query-string tokens are rejected with `401`, **with no exception** — asserted by a test | Phase 1 |
| An invite code never reaches the server in a URL path or query | The link carries it in the **fragment** (`/join#TODI-…`), which no browser transmits; `previewInvite` and `redeemInvite` take it in a POST body. A route-registry test asserts no operation declares an invite code as a path or query parameter | Phase 1 |
| The Discord guild gate is evaluated on **both** `/join` and `/sessions` | `TestGuildGate_EvaluatedOnJoinAndSessions`. Evaluated against the facts on the `credential_ticket`, never a cached copy and never a client-supplied claim. A gate on join alone would let `/sessions` mint a fresh PAT for someone who has left the guild | Phase 1 |
| A guild gate with no facts to evaluate **rejects**, never skips | `TestGuildGate_MissingRoleFacts_Refused`. Role ids come from `GET /users/@me/guilds/{guild.id}/member` under `guilds.members.read`; `GET /users/@me/guilds` returns partial guilds and no roles. Reading an absent role list as an empty one would disable the gate for every user while appearing to enforce it | Phase 1 |
| A Discord access token is accepted only if it was minted for **this instance's** application | `TestDiscord_ForeignApplicationToken_Refused`. Verification calls `GET /oauth2/@me` before anything else and rejects unless `application.id` equals the configured `client_id` — `401 credential_audience_mismatch`. This is what closes cross-instance replay; per-instance registration alone does not, because `GET /users/@me` honours any valid bearer token whichever application minted it | Phase 1 |
| A `provider_ticket` never appears in a URL query | It reaches the SPA in the redirect **fragment** (`<spa>/join#ticket=…`), which no browser transmits, and is cleared immediately. Failures use `#error=<code>` in the same fragment, so the redirect has one rule rather than one per outcome. `TestNoTokenInURL_CallbackRedirectUsesFragment` | Phase 1 |
| **No public route resolves a caller-supplied `circle_id`** | `TestPublicRoutes_ResolveNoCircleFromCallerSuppliedId`, over the route registry. A pre-authentication route that answers differently for a real circle than an unknown one confirms a circle's existence — including via which OAuth scopes the returned URL requests — which is what [canonical §7](../design/00-canonical-conventions.md#cross-circle-access-returns-404-never-403) hides. A circle comes from a **secret the caller was given** (an invite code) or from a **verified identity's own memberships**, never from an identifier they could guess | Phase 1 |
| Every public route accepting an invite code draws on **one shared rate-limit bucket** | `TestInviteOracle_PreviewAndAuthorizationURL_ShareOneBucket`. `previewInvite` and `createAuthorizationURL` both reveal whether a code is live, so metering them separately would hand an attacker twice the guessing budget. A third such route joins the bucket rather than minting another | Phase 1 |
| `createAuthorizationURL` reveals no more about an invite code than `previewInvite` does | `TestCreateAuthorizationURL_RevealsNoMoreThanPreviewInvite` — the older endpoint's disclosure is the ceiling, so the newer one cannot be reasoned about separately and drift wider | Phase 1 |
| A rate-limited caller creates no `auth_flow` rows | `TestAuthFlow_RateLimitedCaller_CreatesNoRows`. Rows are written only past the limit, are capped per caller, and are swept on expiry, so an unauthenticated flood cannot grow the table | Phase 1 |
| The circle recorded on an `auth_flow` is advisory; **redemption re-derives it** | `TestAuthFlow_CircleIsAdvisory_RedemptionReDerives`. The flow resolves an invite's circle early so it can pick OAuth scopes and the guild to check, but `/join` re-reads the invite and decides. A 120-second-old snapshot must never outrank the live row — the same reason `target_state_cache` is not authority | Phase 1 |

## Test-integrity invariants

The ones that exist because the fastest route to a green build is to change the test.

| Invariant | Enforced by |
|---|---|
| The consensus golden corpus is not rewritten to go green | `test/golden/` is CODEOWNERS-protected; `-update` is refused when `CI=true`; a test asserts the fixture count is non-decreasing |
| Tests are not skipped or weakened to land a change | Review, plus a coverage floor per package |
| No goroutine leaks in the event package | `goleak.VerifyTestMain` in `TestMain` |

## Operational invariants

| Invariant | Enforced by |
|---|---|
| The container health check never touches the database | The Dockerfile calls `/healthz`, which does not. A DB-touching healthcheck lets Docker kill the container mid-migration |
| `/metrics` is not exposed by default | Default `TOD_METRICS_ENABLED=false`; when enabled it binds a separate listener and requires a token. Never gated by a PAT scope |
| No secret is ever logged | `type Secret` renders as `***` in `String`, `MarshalJSON` and `LogValue`; a test marshals the whole config and asserts no known secret value appears |
| A Discord access token is never persisted | Read and discarded inside the OAuth callback; only the derived subject, display name, guild ids and role ids reach `credential_ticket`. `TestDiscord_AccessToken_NeverPersisted` asserts no store call receives it |
| `identity_provider.client_secret` is never serialised or logged | It is a `core.Secret` — `***` in `String`, `MarshalJSON` and `LogValue` — and `listIdentityProviders` returns `client_id` but never the secret. Covered by the no-secret-is-ever-logged test above |

## Licence invariants

| Invariant | Enforced by |
|---|---|
| No timer data is bundled | Respawn and variance numbers load from the separate, optional `tod-serve-p99-seed` repository. A CI grep firewall, matching the one DKP puts around `dkp-p99-seed` |
| No copyleft or source-available runtime dependency | `scripts/licence-gate.sh`, classifying every module in `go list -deps ./...` and failing closed |
| No game assets are bundled | Reviewed at release |

## What to do when two rules conflict

The invariant wins, and the conflict is a bug worth reporting. Say so rather than picking one
silently. [Canonical conventions](../design/00-canonical-conventions.md) is the tie-breaker between
any two documents; if a document disagrees with it, the document is stale.
