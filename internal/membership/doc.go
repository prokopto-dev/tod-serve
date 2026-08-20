// Package membership owns who is in a circle, at what role, and whether they still are.
//
// # There is no delete
//
// Not in this package, not in `db/queries`, not at any permission level. The partial unique index
// `ux_membership_identity` makes a second row for one identity unrepresentable, and that index IS
// the revocation mechanism: a revoked person redeeming a fresh invite hits the existing row, sees
// `revoked_at IS NOT NULL`, and gets `403 membership_revoked`. A delete-then-insert path would
// hand them a clean row, so there is no such path to reach for under deadline.
//
// Reinstatement is the only way back in, and it is explicit, audited and gated on `member.revoke`.
//
// # Revocation is checked on every request, not cascaded
//
// [ADR-0005] binds a PAT to a membership, and `internal/auth` re-reads that membership on every
// request. Revoking therefore takes effect on the revoked member's very NEXT request, with no
// token list to walk and nothing to forget. This package writes `revoked_at` and stops.
//
// # Joining is one endpoint
//
// [ADR-0007]: `/join` and `/sessions` take the same credential union and dispatch on provider
// through [identity.Service.Verify]. Both evaluate the Discord guild gate through the one
// evaluator, [identity.EvaluateGuildGate] — a gate checked only at join is a gate somebody walks
// around by re-authing on a new device.
//
// [ADR-0005]: docs/adr/0005-pats-bound-to-memberships.md
// [ADR-0007]: docs/adr/0007-one-join-endpoint.md
package membership
