# ADR-0016 — First-run setup is an env token, open only while no administrator exists

**Status:** accepted · **Date:** 2026-08-28 · **Deciders:** Courtney Caldwell

## Context and problem statement

Standing an instance up takes four `docker compose run` round trips with a browser step in the
middle: on a fresh database nobody holds a credential and no circle exists, so no HTTP route can
authorise anything — which is why [ADR-0012](0012-instance-grants-are-a-capability-ledger.md) put
the first `instance_grant` at the console. Something has to write the first rows. The open question
is what authorises it and when that stops; get the second half wrong and the first stranger to load
the page owns the instance.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Keep the CLI bootstrap only | Holding the database *is* the authorisation, so nothing new is exposed | The console cannot set up the instance it ships inside, and each step is a shell on a droplet |
| B — A first-run code the server prints to its log at boot | No new configuration, unguessable, rotates every restart | The log is the delivery channel: an aggregator copies it off the host, and an operator who scrolled past it must restart production to get another |
| **C — `TOD_SETUP_TOKEN`, with availability derived from the absence of an ADMINISTRATOR** | The operator already writes `.env`; the token sits beside the pepper and the session key. The window closes on a fact nothing can desynchronise | A takeover surface on a public port, and a token left set stays armed |
| D — C, but the window closes when the `instance` row exists | One indexed read, no ledger | An instance row with no administrator behind it locks the operator out of the instance *and* out of the wizard that would fix it |

## Decision outcome

**Chosen: C.** B and C differ only in where the secret comes from, and B's answer is the log —
the one place a secret must not be. D is C with the cheap derivation: `configured` is a fact about
a row, and the fact that matters is whether anybody can administer this instance.

**Availability is derived, never stored.** Setup is open exactly while **no identity both holds
`instance.security.manage` and has a live membership to present it with**. The permission is asked
through `authz.ExpandInstance`, so `instance.owner` closes the window with no second list; the
membership through `instancegrant.CanAuthenticate`, which `tod-serve doctor` also calls — a grant
outlives the membership carrying it, and closing on it alone would shut the browser door on an
instance doctor calls unadministrable. There is no `setup_complete` flag: a stored flag gets out of
step with reality.

**The route is a registry row, not an exception.** `Auth: AuthSetupToken` joins `AuthMetricsToken`
as a kind the middleware handles before a principal is resolved, so `ROUTE001` still confines
registration and the document publishes a `setupToken` scheme. `api.SetupRoutes()` is what the
three refusal tests are derived from —
`TestSetupRoutes_TokenUnset_EveryOperationRefuses`,
`TestSetupRoutes_WrongToken_IsTheSameRefusalAsUnset` and
`TestSetupRoutes_AnAdministratorExists_EveryOperationRefuses` — so a second setup route cannot be
added uncovered. Before a principal, not before the edge's rules: a query-string token is refused
and `Idempotency-Key` required for every auth kind, because both once lived inside `authorize` —
which the principal-less kinds never call.

**Unset and wrong are one refusal, on one code path.** `SetupConfig.authorises` runs
`subtle.ConstantTimeCompare` through `core.Secret.Equal` *before* consulting whether anything is
configured, so both answer a byte-identical `404`. `SECRET001` keeps the comparison constant-time.

**The first administrator arrives by redeeming an owner GRANT** — the wizard's, or the one
`tod-serve init` prints: the rule is about the instance's state, not who minted the code. Redeeming
one while nobody administers the instance appends `instance.owner` inside the join's own
transaction, so the check and the append cannot straddle a commit, and SQLite's single writer makes
a second redemption see the first. A grant, never an invite: `invite` carries
`CHECK (role <> 'owner')`, so a leaked invite cannot reach this branch.

**Setup is a sequence, not one transaction.** `circle.Service.Create`, `SetProviders`,
`MintOwnerGrant` and `SeedTargets` each own one; composing them means rewriting four packages and
holding SQLite's only write lock across the catalogue seed. Instead the steps are ordered so
**every prefix is a state the wizard resumes from**, each create-if-absent. What a transaction
would have bought, the derived window buys:
`TestSetupState_AHalfFinishedSetup_IsReportedAndResumable` drives it. The first circle cannot be
create-if-absent — a second is a circle nobody asked for — so it is claimed rather than read:
`circle.CreateFirst` counts and inserts in one transaction, and two runs cannot both find none.

### Consequences

- Good, because an operator who loses the owner code re-runs the wizard rather than shelling in.

- Good, because `instance grant`, `init` and `circle create` still work: the console is the way
  back when nobody can sign in, which makes `instance.owner` safe to grant.
- Good, because the window closes on the expansion the middleware asks, so it cannot disagree with
  who can administer the instance.
- **Bad, because `/meta` now says `setup_available` on a public route**, telling a stranger the
  instance is mid-setup. True and already observable — nobody can sign in — and false for good at
  the first administrator, but a disclosure nobody asked for.
- **Bad, because revoking every administrator re-arms both doors.** A live setup token becomes a
  takeover credential again, and the next owner code redeemed makes its holder the administrator.
  That is the recovery path working, and a way to hand an instance away by accident.
- **Bad, because a partial run leaves rows behind** — a circle, a provider — reported, not
  cleaned up.
- **Bad, because deriving the window is a ledger read**, not one indexed lookup.

### Reversal cost

Low. Delete the two routes, the `AuthSetupToken` kind and the `setup_available` field; what it
wrote is ordinary rows the console already reads. The bootstrap branch in
`membership.Service.Join` is the only piece with a data consequence, and wrote nothing
`instance grant` could not.
