// Package canondoc reads fenced blocks out of the normative design documents.
//
// It exists so a test can compare code against the document itself rather than against a list
// hand-copied out of it. A hand-copied list makes the test agree with the copy, which is the one
// thing nobody needed checking: the pair that drifts is the code and the document. Two tests
// depend on this — the enum catalogue in internal/schemaenum and the capability floor in
// internal/authz — and both would be theatre without it.
//
// The parser is deliberately small. It understands ATX headings and triple-backtick fences,
// because that is all the documents use, and a Markdown library would be a runtime dependency
// bought for a test helper.
package canondoc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalPath is the tie-breaker document, relative to the repository root.
const CanonicalPath = "docs/design/00-canonical-conventions.md"

// DomainModelPath is the domain model, relative to the repository root.
const DomainModelPath = "docs/design/01-domain-model.md"

// ErrNotFound is returned when a heading matches no block, or matches more than one heading.
var ErrNotFound = errors.New("no unique matching heading")

// Block is one fenced code block and the heading it sits under.
type Block struct {
	// Heading is the full text of the nearest preceding heading, without its leading hashes.
	Heading string
	// Language is the fence's info string, empty when the fence carries none.
	Language string
	// Line is the 1-indexed line of the opening fence, so a failure can be clicked.
	Line int
	// Body is the block's content, fences excluded.
	Body string
}

// Fields returns the block's whitespace-separated tokens. The permission and scope blocks are
// written as words across several lines purely for readability, so the layout carries no meaning
// and flattening it is the honest reading.
func (b Block) Fields() []string { return strings.Fields(b.Body) }

// Doc is a parsed Markdown document.
type Doc struct {
	// Path is where the document was read from, for error messages.
	Path   string
	raw    string
	blocks []Block
	tables []Table
}

// Raw returns the whole document. Some rules are stated in prose rather than in a fenced block —
// the enum ordering is written `observer < member < officer < owner` in a sentence — and a test
// that reads them out of the document is still better than a test that repeats them.
func (d *Doc) Raw() string { return d.raw }

// Load parses the document at path.
//
// The whole file is read into memory: these documents are a few hundred lines and the alternative
// is a deferred Close whose error nobody can act on.
func Load(path string) (*Doc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	doc := &Doc{Path: path, raw: string(raw)}
	var (
		heading string
		open    *Block
		body    strings.Builder
	)
	// Lines inside a fence are blanked in this copy, so a fenced block containing pipes cannot be
	// read as a Markdown table by parseTables below. headings[i] is the section line i sits in.
	source := strings.Split(string(raw), "\n")
	outside := make([]string, len(source))
	headings := make([]string, len(source))
	for i, line := range source {
		lineNo := i + 1
		headings[i] = heading
		switch {
		case open != nil && strings.HasPrefix(line, "```"):
			open.Body = body.String()
			doc.blocks = append(doc.blocks, *open)
			open, body = nil, strings.Builder{}
		case open != nil:
			body.WriteString(line)
			body.WriteString("\n")
		case strings.HasPrefix(line, "```"):
			open = &Block{
				Heading:  heading,
				Language: strings.TrimSpace(strings.TrimPrefix(line, "```")),
				Line:     lineNo,
			}
		case strings.HasPrefix(line, "#"):
			heading = strings.TrimSpace(strings.TrimLeft(line, "#"))
			headings[i] = heading
		default:
			outside[i] = line
		}
	}
	if open != nil {
		return nil, fmt.Errorf("read %s: unterminated fence opened at line %d", path, open.Line)
	}

	tables, err := parseTables(path, outside, headings)
	if err != nil {
		return nil, err
	}
	doc.tables = tables
	return doc, nil
}

// LoadCanonical parses the canonical conventions document, found relative to the repository root
// so a test does not depend on the directory it was invoked from.
func LoadCanonical() (*Doc, error) { return loadFromRoot(CanonicalPath) }

// LoadDomainModel parses the domain model document.
func LoadDomainModel() (*Doc, error) { return loadFromRoot(DomainModelPath) }

// TenancyADRPath is ADR-0002, which repeats canonical §9's instance-scoped allowlist in its own
// prose. It is loadable here so a test can diff the two rather than trusting that whoever last
// added a table remembered there were two copies — which, for three tables, nobody did.
const TenancyADRPath = "docs/adr/0002-circle-is-the-tenant.md"

// LoadTenancyADR reads ADR-0002.
func LoadTenancyADR() (*Doc, error) { return loadFromRoot(TenancyADRPath) }

func loadFromRoot(rel string) (*Doc, error) {
	root, err := RepoRoot()
	if err != nil {
		return nil, err
	}
	return Load(filepath.Join(root, rel))
}

// BlocksUnder returns every fenced block whose heading contains the given substring, in document
// order. Matching on a substring keeps a test from breaking when a heading gains a section number
// or an em dash; matching more than one heading is an error rather than a merge, because a silent
// merge is how a test starts asserting something other than what it says.
func (d *Doc) BlocksUnder(heading string) ([]Block, error) {
	matched := map[string]bool{}
	var out []Block
	for _, b := range d.blocks {
		if strings.Contains(b.Heading, heading) {
			matched[b.Heading] = true
			out = append(out, b)
		}
	}
	switch {
	case len(matched) == 0:
		return nil, fmt.Errorf("blocks under %q in %s: %w", heading, d.Path, ErrNotFound)
	case len(matched) > 1:
		return nil, fmt.Errorf("blocks under %q in %s matched %d headings: %w",
			heading, d.Path, len(matched), ErrNotFound)
	}
	return out, nil
}

// BlockUnder returns the nth (0-indexed) fenced block under the matching heading. An index outside
// the blocks that exist — in either direction — is [ErrNotFound] rather than a panic: this runs
// inside the gates, and a gate that crashes reads as a broken build rather than as a finding.
func (d *Doc) BlockUnder(heading string, n int) (Block, error) {
	blocks, err := d.BlocksUnder(heading)
	if err != nil {
		return Block{}, err
	}
	if n < 0 || n >= len(blocks) {
		return Block{}, fmt.Errorf("block %d under %q in %s: %w", n, heading, d.Path, ErrNotFound)
	}
	return blocks[n], nil
}

// RepoRoot walks up from the working directory to the module root — the directory holding go.mod.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repository root above %s: %w", dir, os.ErrNotExist)
		}
		dir = parent
	}
}
