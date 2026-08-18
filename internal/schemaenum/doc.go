// Package schemaenum is the single Go source for every enumerated column in the schema.
//
// The wire value IS the database value — lowercase snake_case, no translation layer — so one
// catalogue can drive the SQL CHECK constraint, the OpenAPI enum and the Go constants. Three
// hand-maintained copies of "the values a column may hold" is three chances to disagree, and the
// disagreement surfaces as a CHECK violation at write time, which is a 500 for the user and a
// puzzle for whoever is on call.
//
// The catalogue is a function rather than a package-level slice because a slice is mutable, and a
// caller that appends to a shared catalogue changes what every other caller sees. Every accessor
// returns freshly built values.
//
// Ordering lives here too, in exactly one place: [Enum.Order] says whether Values is listed
// ascending, descending, or has no order at all. [Enum.Rank] is the only thing that knows how to
// turn a value into a position, and [Enum.OrderBySQL] is the only thing that writes the
// ORDER BY CASE. An enum with no order refuses to invent one rather than returning a rank that
// looks meaningful.
//
// The catalogue is normative in docs/design/00-canonical-conventions.md §5;
// TestAll_Catalogue_MatchesCanonicalConventions parses that document and compares both
// directions, so neither this file nor the document can drift.
package schemaenum
