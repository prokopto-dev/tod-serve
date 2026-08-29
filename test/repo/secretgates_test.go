package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// TestSECRET001_NoSecret_IsComparedByValue makes "compare a secret in constant time" a mechanism
// rather than a habit.
//
// `core.Secret.Equal` is `subtle.ConstantTimeCompare`, and every bearer credential this binary
// checks at the edge goes through it: `TOD_METRICS_TOKEN` on every scrape, and `TOD_SETUP_TOKEN` on
// a route that hands somebody the whole instance. Neither is rate-limited — the metrics listener
// has no bucket and first-run setup has no principal to key one on — so the comparison is the
// entire defence, and `==` on a string returns as soon as two bytes differ.
//
// **The failure it catches is invisible in every other way.** `x.Reveal() == y` passes the unit
// test, passes review by looking like a comparison, and answers exactly the same status codes. It
// was written deliberately during this gate's own mutation run and no existing test noticed.
//
// It is an AST walk rather than a grep because `Reveal()` legitimately appears all over — a
// client secret going into a form body, a pepper going into an HMAC. What is banned is narrower:
// a revealed secret on either side of `==` or `!=`. That is a comparison, and a comparison of a
// secret is the one thing [core.Secret] exists to route through one function.
func TestSECRET001_NoSecret_IsComparedByValue(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	files := 0
	comparisons := 0
	for _, dir := range []string{"internal", "cmd"} {
		walkErr := filepath.WalkDir(filepath.Join(root, dir),
			func(path string, d fs.DirEntry, err error) error {
				switch {
				case err != nil:
					return err
				case d.IsDir() || !strings.HasSuffix(path, ".go"):
					return nil
				// Test files are excluded for one reason only: a test asserting that two secrets
				// differ is not a credential check, and there is no timing to leak to a test.
				case strings.HasSuffix(path, "_test.go"):
					return nil
				}
				fset := token.NewFileSet()
				file, parseErr := parser.ParseFile(fset, path, nil, 0)
				if parseErr != nil {
					return parseErr
				}
				files++
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				ast.Inspect(file, func(n ast.Node) bool {
					binary, ok := n.(*ast.BinaryExpr)
					if !ok || (binary.Op != token.EQL && binary.Op != token.NEQ) {
						return true
					}
					if !revealsASecret(binary.X) && !revealsASecret(binary.Y) {
						return true
					}
					comparisons++
					t.Errorf("%s:%d compares a revealed secret with %s. Use "+
						"core.Secret.Equal, which is subtle.ConstantTimeCompare: a byte-by-byte "+
						"comparison of a bearer credential returns early on the first difference, "+
						"and neither the metrics token nor the setup token has a rate limit in "+
						"front of it",
						rel, fset.Position(binary.Pos()).Line, binary.Op)
					return true
				})
				return nil
			})
		require.NoError(t, walkErr)
	}

	// The vacuity guard. This walks two directories that a moved tree would empty, and a gate
	// reporting success over nothing is what this repository is built against.
	require.Positive(t, files, "no production Go was parsed; the walk is wrong")
	require.Zero(t, comparisons)
}

// revealsASecret reports whether an expression is a call to `.Reveal()`.
//
// It matches on the METHOD NAME rather than on a resolved type, and that is a deliberate
// over-match: `Reveal` is named in `internal/core` precisely so `grep -rn Reveal()` lists every
// place a secret is handled, so anything else spelling it is either a second secret type — which
// this rule wants to cover too — or a name worth not having.
func revealsASecret(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Reveal"
}
