// Package instancegrant is the ledger of instance-level authorization decisions.
//
// It answers one question for the authorization path — "which instance-realm permissions does this
// identity hold" — and records the two decisions that change the answer. ADR-0012 is the argument;
// what follows is what a reader has to know to change this package safely.
//
//   - **A row is a decision, not a state.** `granted` and `revoked` are both rows, the table is
//     append-only by trigger, and each row names the row it supersedes. The row that took a
//     permission away is as durable as the one that gave it.
//   - **It is its own audit record.** Handing somebody the instance's identity providers is exactly
//     the event an audit log exists for, and `audit_log.circle_id` is NOT NULL, so `internal/audit`
//     cannot hold an instance-level event at all. The chain is [audit.ChainHash] rather than a
//     second implementation of it.
//   - **Nothing here is ordered by id, and that is load-bearing.** A ULID is monotonic within one
//     generator and each console invocation builds its own, so two inside a millisecond can mint
//     out of order. Which decision is current is therefore a constraint —
//     `ux_instance_grant_supersedes` and `ux_instance_grant_head` make each (identity, permission)
//     pair one chain with exactly one tail — and the hash chain's own tail is the row whose hash
//     no other row names, never the greatest id. Selecting it by id made the ledger stop accepting
//     appends entirely after two console invocations landed in one millisecond. If two rows ever
//     satisfy either tail query, that is a forked chain and this package says so rather than
//     picking one.
//   - **The grant is on an IDENTITY, not a membership.** A membership is in one circle and an
//     instance permission is about the whole instance. That is also why a personal access token
//     never reaches one: a token is bound to a membership (ADR-0005).
//   - **A decision with no decider is the console.** `tod-serve instance grant` holds the database
//     and precedes every identity on a fresh instance, so `decided_by_identity_id` is nullable and
//     NULL reads as "the operator at the console" — a different fact from a person having decided.
package instancegrant
