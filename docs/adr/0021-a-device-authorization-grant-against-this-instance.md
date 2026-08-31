# ADR-0021 — A device authorization grant against this instance, not against Discord

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** Courtney Caldwell

## Context and problem statement

A PAT is born in three places: `POST /join` on an invite,
`POST /circles/{circle_id}/service-members` behind the `token.mint` floor, and `POST /sessions`,
which re-authenticates an existing membership on a new device with no invite. `/sessions` is the
one that matters, and it cannot serve nParse+: it takes **one** `circle_id`, 404s where the identity
holds no membership there, and needs a **provider credential** — for Discord the single-use ticket
the browser flow mints, obtainable by a desktop binary only as the public per-install app this ADR
rejects. The console lives with that by remembering circles in browser storage
(`web/src/screens/SignIn.tsx`), which a plugin cannot.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Discord OAuth inside the plugin | No new flow in tod-serve | A desktop binary holds no client secret, so it needs a **public, per-install** app — the shared-app model [ADR-0011](0011-operator-registered-discord-application.md) removed to close cross-instance replay — and it puts a Discord token on a player's disk |
| B — Loopback redirect (RFC 8252) | Standard; no polling endpoint | Every operator must register a per-instance redirect URI, and it fails when the browser is on another machine — a second OAuth surface for a client that only wanted a token |
| C — RFC 8628 device grant against **tod-serve** | The plugin never touches Discord: tod-serve is already the relying party, so no secret ships and ADR-0011's per-instance app and `GET /oauth2/@me` audience check are untouched | Three new public routes, one of them polled, plus a table, a sweep target and a console screen |

## Decision outcome

**Chosen: C**, because the plugin never speaks to Discord: the user code goes to a browser that
signs in as it does today, while the plugin polls. Nothing about ADR-0011 moves.

**Scopes.** A device may request `tod:read`, `tod:report` and `catalogue:read`, nothing else.
`catalogue:read` is not optional — without it the plugin cannot resolve a parsed mob name to a raid
target, so it cannot turn a log line into a report. `circle:read`, `member:read`, `invite:read` and
`invite:create` are refused: a log parser must not enumerate members or mint invites.

**`tod:retract` is excluded.** For it: a correction is a new row, not a deletion
([ADR-0004](0004-append-only-reports-derived-consensus.md)), so retraction is arguably reporting.
Against, and decisive: `ScopeTodRetract` grants `tod.retract` **and** `tod.retract.any`, so there is
no narrow own-reports scope to hand over — it would let an unattended desktop process invalidate
another member's ToD.
The plugin corrects by reporting again; consensus is derived.

**The quake is excluded, and there is no scope to include.** `OpReportQuake` carries
`PermissionTodQuakeReport` and declares **no scopes at all**, so no PAT reaches it and the catalogue
has no quake scope to name. Its summary reads "a false one wipes the whole board"; minting
`tod:quake` for a log parser is a bigger decision than this one.

**`events:subscribe` belongs later, not now.** It grants only `tod.read`, which a device already
holds, so adding it once Phase 6 ships a handler widens the transport, not the rows. Today it would
grant nothing yet still need reviewing as if it did.

**What an approval yields is [ADR-0022](0022-a-device-grant-is-identity-scoped.md)'s question** —
an identity-scoped grant exchanging for per-membership tokens, and the ceiling that one Discord
login cannot span instances.

**The new public surface.** `/device/authorize`, `/device/approve` and `/device/token` are all
`AuthPublic`; approval takes a `provider_ticket`, not a cookie
([ADR-0022](0022-a-device-grant-is-identity-scoped.md)). The two that accept a code join the
invite-oracle bucket via `Route.InviteOracle`, whose comment already says a code-taking route joins
it rather than minting another: two buckets hand a guesser twice the budget. `/device/authorize`
reveals no code and is limited separately.
`TestInviteOracle_TheMeteredSet_IsEveryPublicRouteThatTakesACode` pins that set and stays red until
it names them. The user code uses `internal/invite`'s Crockford alphabet at no less than
`invite.CodeBits`, single-use, expiring in minutes.

**Law 5 needs no new rule.** What reaches a route is a membership-bound token, so it 404s on circle
B by the path `TestTenancy_CrossCircle_EveryOperationDenies` drives.

**Approval is a grant, and `audit_log.circle_id` is `NOT NULL`.** An approval is on an identity and
may precede every membership ([ADR-0022](0022-a-device-grant-is-identity-scoped.md)), so
`internal/audit` cannot hold it — the wall `instance_grant` already hit. `device_grant` is its own
append-only, hash-chained record on `audit.ChainHash`, so a memberless approval is one row rather
than none anywhere. The circle-scoped `audit_log` row belongs to the exchange that mints into a
circle. ADR-0011's guarantee that an audit names a responsible person holds on both.

**The device code is litter; the grant is history.** `device_authorization` carries `expires_at`
and is swept by `tod-serve sweep` past `sweep.Grace`, beside `auth_flow` and `credential_ticket`;
`device_grant` is append-only and never swept.

**It does not replace `/sessions`; it reaches where `/sessions` cannot.** A member who lost a token
still re-authenticates in the console. The device flow adds what that route cannot do: no provider
credential in the plugin, and no circle named in advance.

### Consequences

- Good, because no client secret ships in a desktop binary and ADR-0011 is untouched.
- Good, because the credential a route sees is an ordinary PAT: revocation, listing and authz
  are unchanged.
- **Bad, because a compromised Discord account now yields durable tokens, not just a session.** The
  scope set bounds what they reach, not how long they last.
- **Bad, because the bucket is keyed per caller,** so a distributed guesser gets a budget per
  address. Entropy and short expiry defend this, not the limiter.
- **Bad, because a client holds one token per circle** (ADR-0022), which every client author will
  get wrong once.
- **Bad, because `authorization_pending` must not become a timing oracle** for whether a code
  exists — nothing in the tree tests that today.

### Reversal cost

A release: drop three routes, the `device_authorization` table and a console screen — never
`device_grant`, which is a ledger ([ADR-0022](0022-a-device-grant-is-identity-scoped.md)). Minted
tokens are ordinary PATs and keep working, so nothing is re-issued.
