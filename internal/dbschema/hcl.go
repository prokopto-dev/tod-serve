package dbschema

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SchemaHCLPath is the declarative schema truth, relative to the repository root.
const SchemaHCLPath = "db/schema.hcl"

// tableDecl and columnDecl match the two block openings that carry a name. Atlas writes and reads
// this file, so its shape is regular; `columns = [column.id]` inside an index or a foreign key is
// a reference rather than a declaration and does not match, because it has no quoted name.
var (
	tableDecl  = regexp.MustCompile(`^table "([a-z0-9_]+)" \{`)
	columnDecl = regexp.MustCompile(`^\s+column "([a-z0-9_]+)" \{`)
)

// HCLTables reads the table and column names out of db/schema.hcl.
//
// It is a small purpose-built reader rather than Atlas-the-library: the one question it answers is
// "does the declared shape still match the shape a migrated database has", and answering it needs
// names, not types. Types, constraints and tenancy are checked against the APPLIED schema by the
// tests in internal/store, which read what SQLite actually enforces — a stronger source than any
// parse of this file.
//
// What it therefore does NOT catch on its own: a type or a default changed in db/schema.hcl with
// no migration. `make gen` catches that by re-running the Atlas diff, and it is the reason that
// step exists.
func HCLTables(src string) (map[string][]string, error) {
	out := map[string][]string{}
	current := ""

	for i, line := range strings.Split(src, "\n") {
		switch {
		case strings.HasPrefix(line, "//"):
			continue
		case tableDecl.MatchString(line):
			current = tableDecl.FindStringSubmatch(line)[1]
			if _, seen := out[current]; seen {
				return nil, fmt.Errorf("read %s: table %q is declared twice at line %d",
					SchemaHCLPath, current, i+1)
			}
			out[current] = nil
		case line == "}":
			current = ""
		case columnDecl.MatchString(line):
			if current == "" {
				return nil, fmt.Errorf("read %s: column at line %d is outside any table",
					SchemaHCLPath, i+1)
			}
			out[current] = append(out[current], columnDecl.FindStringSubmatch(line)[1])
		}
	}

	if len(out) == 0 {
		// A comparison against nothing passes, which is exactly the shape of failure this
		// repository gates against everywhere else.
		return nil, fmt.Errorf("read %s: no tables parsed", SchemaHCLPath)
	}
	for name, columns := range out {
		if len(columns) == 0 {
			return nil, fmt.Errorf("read %s: table %q has no columns", SchemaHCLPath, name)
		}
		sort.Strings(columns)
	}
	return out, nil
}
