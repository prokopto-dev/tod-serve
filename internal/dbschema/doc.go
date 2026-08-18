// Package dbschema binds the enum catalogue to the columns that hold it, and renders the part of
// the Atlas schema that a human must not write by hand.
//
// `db/schema.hcl` is the schema's shape — its tables, columns, keys and indexes — and a person
// reviews a diff of it. The enum `CHECK` constraints in it are not shape, they are a copy of
// [internal/schemaenum], and canonical conventions §5 requires one source rather than a copy that
// agrees today. So this package renders them into `db/enums.hcl` as Atlas locals, `schema.hcl`
// refers to those locals by name, and TestEnumsHCL_Generated_MatchesTheCheckedInFile fails when
// the checked-in file is stale.
//
// The binding table is also what makes the reverse check possible:
// TestEnumColumns_AppliedSchema_MatchesTheCatalogue reads the `CHECK` constraints back out of a
// migrated database and compares them against the catalogue, so a hand-edited migration that
// widens an enum is a red test rather than a value the API rejects and the database accepts.
package dbschema
