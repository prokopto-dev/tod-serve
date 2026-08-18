package dbschema

import (
	"fmt"
	"strings"
)

// ConstraintName is the name the enum CHECK carries in the DDL.
//
// It shadows [Binding.LocalName] deliberately — `ck_` in SQL, `check_` in Atlas — so that a
// constraint and the local that generated it are findable from each other, and so a test can
// compare the applied constraint against the catalogue by name rather than by searching the DDL
// for a string that might appear twice.
func (b Binding) ConstraintName() string {
	return fmt.Sprintf("ck_%s_%s", b.Table, b.Column)
}

// CheckConstraints extracts the named CHECK constraints from a stored CREATE TABLE statement,
// returning constraint name to expression with the outer parentheses removed.
//
// SQLite stores the DDL verbatim, so this reads what the database actually enforces rather than
// what the migration was supposed to say. Parentheses are balanced rather than matched to the
// first `)`, because every predicate here contains at least one — `kind IN ('a', 'b')`.
//
// Unnamed CHECK constraints are skipped: they cannot be compared to anything, which is the reason
// db/schema.hcl names every one of them.
func CheckConstraints(ddl string) map[string]string {
	const marker = "CONSTRAINT "
	out := map[string]string{}

	for i := 0; ; {
		start := strings.Index(ddl[i:], marker)
		if start < 0 {
			return out
		}
		i += start + len(marker)

		name, rest, found := strings.Cut(ddl[i:], " ")
		if !found {
			return out
		}
		if !strings.HasPrefix(rest, "CHECK (") {
			continue // a named FOREIGN KEY or UNIQUE, not our business
		}
		expr, width, ok := balanced(rest[len("CHECK "):])
		if !ok {
			return out
		}
		out[name] = expr
		i += len(name) + 1 + len("CHECK ") + width
	}
}

// balanced reads a parenthesised expression from the front of s, returning its contents without
// the outer pair and how many bytes it consumed. Quoted string literals are skipped, so a value
// containing a parenthesis cannot end the expression early.
func balanced(s string) (expr string, width int, ok bool) {
	if !strings.HasPrefix(s, "(") {
		return "", 0, false
	}
	depth, inQuote := 0, false
	for i, r := range s {
		switch {
		case inQuote:
			if r == '\'' {
				inQuote = false
			}
		case r == '\'':
			inQuote = true
		case r == '(':
			depth++
		case r == ')':
			depth--
			if depth == 0 {
				return s[1:i], i + 1, true
			}
		}
	}
	return "", 0, false
}
