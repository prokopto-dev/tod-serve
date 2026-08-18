package canondoc

import (
	"fmt"
	"strings"
)

// Table is one Markdown pipe table: its header cells and its body rows, each cell trimmed and with
// the surrounding backticks left in place. Callers that want an identifier out of a cell use
// [Unquote], because a cell can legitimately hold prose next to the identifier.
type Table struct {
	// Heading is the full text of the nearest preceding heading, without its leading hashes.
	Heading string
	// Line is the 1-indexed line of the header row, so a failure can be clicked.
	Line int
	// Header is the header cells.
	Header []string
	// Rows are the body rows. Every row has the same length as Header; a row with a different
	// cell count is a malformed table and is an error rather than a silently padded row.
	Rows [][]string
}

// Column returns the values in the named column, in row order. The name is matched exactly,
// because a fuzzy match is how a test starts reading a column other than the one it names.
func (t Table) Column(header string) ([]string, error) {
	for i, h := range t.Header {
		if h != header {
			continue
		}
		out := make([]string, 0, len(t.Rows))
		for _, row := range t.Rows {
			out = append(out, row[i])
		}
		return out, nil
	}
	return nil, fmt.Errorf("column %q in the table under %q: %w", header, t.Heading, ErrNotFound)
}

// Unquote strips one pair of surrounding backticks, so `tod_report` in a document cell becomes the
// table name a schema test can look up. A cell with no backticks is returned unchanged.
func Unquote(cell string) string {
	if len(cell) >= 2 && strings.HasPrefix(cell, "`") && strings.HasSuffix(cell, "`") {
		return cell[1 : len(cell)-1]
	}
	return cell
}

// TablesUnder returns every Markdown table whose heading contains the given substring, in document
// order. It matches headings the same way [Doc.BlocksUnder] does, and for the same reason.
func (d *Doc) TablesUnder(heading string) ([]Table, error) {
	matched := map[string]bool{}
	var out []Table
	for _, t := range d.tables {
		if strings.Contains(t.Heading, heading) {
			matched[t.Heading] = true
			out = append(out, t)
		}
	}
	switch {
	case len(matched) == 0:
		return nil, fmt.Errorf("tables under %q in %s: %w", heading, d.Path, ErrNotFound)
	case len(matched) > 1:
		return nil, fmt.Errorf("tables under %q in %s matched %d headings: %w",
			heading, d.Path, len(matched), ErrNotFound)
	}
	return out, nil
}

// TableUnder returns the nth (0-indexed) Markdown table under the matching heading.
func (d *Doc) TableUnder(heading string, n int) (Table, error) {
	tables, err := d.TablesUnder(heading)
	if err != nil {
		return Table{}, err
	}
	if n < 0 || n >= len(tables) {
		return Table{}, fmt.Errorf("table %d under %q in %s: %w", n, heading, d.Path, ErrNotFound)
	}
	return tables[n], nil
}

// BacktickedListAfter returns the backticked identifiers in the sentence that begins with prefix.
//
// Some rules in these documents are stated in a sentence rather than in a fenced block — canonical
// §9 writes the instance-scoped allowlist as prose ending in a full stop — and a test that reads
// the sentence is still enormously better than a test that repeats the list. The sentence ends at
// the first `. ` or end of line after the prefix, so the following sentence's backticks are not
// swept in.
func (d *Doc) BacktickedListAfter(prefix string) ([]string, error) {
	idx := strings.Index(d.raw, prefix)
	if idx < 0 {
		return nil, fmt.Errorf("sentence beginning %q in %s: %w", prefix, d.Path, ErrNotFound)
	}
	rest := d.raw[idx+len(prefix):]

	// The document wraps at 100 columns, so the sentence may span lines; it may not span a blank
	// line, which is the paragraph break.
	if para := strings.Index(rest, "\n\n"); para >= 0 {
		rest = rest[:para]
	}
	if stop := strings.Index(rest, ". "); stop >= 0 {
		rest = rest[:stop]
	}
	if stop := strings.Index(rest, ".\n"); stop >= 0 {
		rest = rest[:stop]
	}

	var out []string
	for chunk := range strings.SplitSeq(rest, "`") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" || strings.ContainsAny(chunk, " ,;:") {
			continue // separator text between two quoted identifiers
		}
		out = append(out, chunk)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("backticked identifiers after %q in %s: %w",
			prefix, d.Path, ErrNotFound)
	}
	return out, nil
}

// instanceScopedPrefix is the sentence in canonical §9 that carries the allowlist. It is a
// constant so that a test naming it and this function cannot disagree about which sentence is the
// authority.
const instanceScopedPrefix = "The instance-scoped allowlist is explicit and short:"

// InstanceScopedTables returns the instance-scoped allowlist from canonical §9 — the tables that
// carry no `circle_id`.
//
// This is the authority. `INSTANCE_SCOPED` in scripts/repo-gates.sh is a copy of it that
// TestInstanceScopedAllowlist_MatchesRepoGates compares in both directions, and the schema test
// derives circle-scoped from it by subtraction, so adding a table to the schema without deciding
// its tenancy is a red test rather than an unchecked table.
func InstanceScopedTables() ([]string, error) {
	doc, err := LoadCanonical()
	if err != nil {
		return nil, err
	}
	return doc.BacktickedListAfter(instanceScopedPrefix)
}

// parseTables walks the already-split lines of a document and collects its pipe tables. It is
// called from [Load] with the heading tracking already done, so a table knows the section it is in.
//
// The parser is as small as the fence parser above and for the same reason: these documents use
// header-delimiter-body pipe tables and nothing else, and a Markdown library would be a runtime
// dependency bought for a test helper.
func parseTables(path string, lines []string, headings []string) ([]Table, error) {
	var out []Table
	for i := 0; i < len(lines); i++ {
		if !isTableRow(lines[i]) || i+1 >= len(lines) || !isDelimiterRow(lines[i+1]) {
			continue
		}
		header := splitRow(lines[i])
		table := Table{Heading: headings[i], Line: i + 1, Header: header}
		i += 2
		for ; i < len(lines) && isTableRow(lines[i]); i++ {
			row := splitRow(lines[i])
			if len(row) != len(header) {
				return nil, fmt.Errorf("read %s: table row at line %d has %d cells, header has %d",
					path, i+1, len(row), len(header))
			}
			table.Rows = append(table.Rows, row)
		}
		i-- // the loop's own increment steps past the row that ended the table
		out = append(out, table)
	}
	return out, nil
}

func isTableRow(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "|") }

// isDelimiterRow reports whether the line is the `|---|---|` separator. Without this check a
// paragraph that merely starts with a pipe would be read as a table header.
func isDelimiterRow(line string) bool {
	if !isTableRow(line) {
		return false
	}
	for _, cell := range splitRow(line) {
		if strings.Trim(cell, "-: ") != "" {
			return false
		}
	}
	return true
}

// escapedPipe is the placeholder splitRow swaps a `\|` for while splitting. An escaped pipe is
// how these documents write an enum inside a cell, as in the state column of the domain model,
// so treating one as a cell boundary would turn a two-value enum into a malformed row.
const escapedPipe = "\x00"

// splitRow splits a pipe row into trimmed cells, dropping the empty cells the leading and trailing
// pipes produce.
func splitRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.ReplaceAll(trimmed, `\|`, escapedPipe)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	cells := strings.Split(trimmed, "|")
	out := make([]string, 0, len(cells))
	for _, c := range cells {
		out = append(out, strings.TrimSpace(strings.ReplaceAll(c, escapedPipe, "|")))
	}
	return out
}
