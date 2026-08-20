// Package circle owns the tenant: the `circle` row, and the set of identity providers it accepts.
//
// # `server` is immutable, and that is structural
//
// [ADR-0009] pins a circle to one server permanently, so there is no row in this schema where a
// Blue fact and a Green fact can meet. `updateCircle` therefore answers `422 field_immutable`
// rather than writing, and `db/queries/circle.sql` has no query that sets `server` at all — a
// BEFORE UPDATE trigger is the third copy of the same rule, and it is the one that holds against
// a hand-written `UPDATE`.
//
// # Revocation strength is derived, never stored
//
// `circle.revocation_strength` has no column. It is [identity.CircleStrength] over the circle's
// accepted, instance-enabled providers, computed on every read. Storing it would let it drift the
// moment a provider is added to the instance — and drift in the SAFE-LOOKING direction, because
// the stored value would still say `durable` while the new provider quietly made it false. The
// dangerous outcome of a weak revocation is not the re-entry; it is the officers' belief that
// revocation worked.
//
// # Removing a provider does not revoke anybody
//
// [Service.SetProviders] stops NEW joins through a provider it drops. It revokes no membership.
// Mass-revoke-on-removal is a footgun that eventually deletes a guild's whole roster with one
// click, and the tool for getting somebody out now is `revokeMember`, which takes effect on their
// very next request.
//
// [ADR-0009]: docs/adr/0009-circle-pinned-to-one-server.md
package circle
