// Package core holds the handful of types every other package agrees on: the time type, the
// identifier type, the server enum and secrets. Nothing here talks to a database, a clock or a
// network, so everything above it can.
//
// # Identifiers
//
// Ids are a generic [ID] parameterised by an entity marker — [CircleID] is `ID[Circle]` — rather
// than a family of named string types.
//
// The alternative was one `type CircleID string` per table. Both give the compiler what matters:
// passing a membership id where a circle id belongs is a build failure, which is the failure this
// prevents, and it is not a hypothetical one — the tenant is the circle and a mixed-up id is
// exactly how a circle-scoped read reaches the wrong tenant.
//
// The generic form wins on the second question, which is where the validation lives. With named
// string types, either every id gets its own copy of "26 characters, Crockford base32, uppercase"
// or ids get constructed by conversion from an unvalidated string, and the second is what actually
// happens under deadline. Here there is one parser, one encoder and one JSON round-trip for every
// id in the system, and [ParseID] is the only way to get an ID out of a string.
//
// The markers (Circle, Membership, …) are empty structs that exist only to keep those types apart.
// They are never values in the domain, and their `entity()` method returns the owning table's
// name, which is what error messages say and what
// TestEntityMarkers_EveryName_IsATableInTheDomainModel checks against the domain model document.
package core
