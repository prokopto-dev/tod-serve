# Architecture decision records

Why things are the way they are, including the downsides. Use
[`0000-template.md`](0000-template.md); budget one screen, about 900 words, 1000 is the ceiling.

**An ADR with no negative consequences is rejected in review.** Six months from now the person
re-litigating a decision needs the costs stated plainly by the people who accepted them.

Never edit an accepted ADR's decision — write a new one and mark the old one superseded, both
directions linked.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-go-single-binary-and-sqlite.md) | One Go binary and one SQLite file | accepted |
| [0002](0002-circle-is-the-tenant.md) | The circle is the tenant, `circle_id` on every scoped row | accepted |
| [0003](0003-pluggable-identity-providers.md) | Identity is a `(provider, subject)` pair | accepted |
| [0004](0004-append-only-reports-derived-consensus.md) | Store every report; derive the answer | accepted |
| [0005](0005-pats-bound-to-memberships.md) | PATs bind to memberships, not service accounts | accepted |
| [0006](0006-atlas-authors-goose-applies.md) | Atlas authors migrations, goose applies them | accepted |
| [0007](0007-one-join-endpoint.md) | One join endpoint, dispatching on provider | accepted |
| [0008](0008-windows-are-offsets.md) | Windows are two offsets; no probability curve | accepted |
| [0009](0009-circle-pinned-to-one-server.md) | A circle is pinned to one server, permanently | accepted |
| [0010](0010-sse-over-websockets.md) | SSE, not WebSockets | accepted |
| [0011](0011-operator-registered-discord-application.md) | Each operator registers their own Discord application | accepted |
| [0012](0012-instance-grants-are-a-capability-ledger.md) | Instance permissions are a capability ledger on an identity | accepted; no-implication half superseded by [0015](0015-instance-owner-implies-the-instance-realm.md) |
| [0013](0013-the-timer-invalidation-joins-the-writing-transaction.md) | The timer invalidation joins the writing transaction | accepted |
| [0014](0014-a-deferred-read-pool-for-multi-read-renders.md) | A second, deferred pool for multi-read renders | accepted |
| [0015](0015-instance-owner-implies-the-instance-realm.md) | `instance.owner` implies the instance realm | accepted |

## Where this project diverges from Dragon Kill Party

Both projects serve the same officers and are driven by the same plugin, so a divergence is a thing
to justify rather than a thing to notice later.

| Divergence | DKP rule | Here | ADR |
|---|---|---|---|
| Tenancy | No `guild_id` column anywhere | `circle_id` on every scoped row | [0002](0002-circle-is-the-tenant.md) |
| Token binding | Tokens belong to service accounts | Tokens belong to memberships | [0005](0005-pats-bound-to-memberships.md) |
| Retention | `parse_line` pruned at 90 days | Reports never pruned | [canonical §11](../design/00-canonical-conventions.md#11-retention) |
| Query-string tokens | Accepted on the compat shim | Rejected with no exception | [canonical §7](../design/00-canonical-conventions.md#7-http-conventions) |
