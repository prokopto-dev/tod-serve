package consensus_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoFloat_Package_ContainsNoFloatingPointAtAll is the authority behind `NOFLOAT001`.
//
// The gate in scripts/repo-gates.sh greps for the words `float32` and `float64`, which is the
// right pre-check for the CI job with no Go toolchain and is not the whole rule: `x := 1.5` is a
// float64 that never spells the word, and a ratio written that way is exactly how a window
// boundary stops being bit-identical across platforms. The parser sees it.
func TestNoFloat_Package_ContainsNoFloatingPointAtAll(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		scanned++

		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind == token.FLOAT || node.Kind == token.IMAG {
					t.Errorf("%s: %s is a floating-point literal; ratios are basis points",
						fset.Position(node.Pos()), node.Value)
				}
			case *ast.Ident:
				if node.Name == "float32" || node.Name == "float64" || node.Name == "complex128" {
					t.Errorf("%s: %s; see canonical conventions §3",
						fset.Position(node.Pos()), node.Name)
				}
			}
			return true
		})
	}
	// A gate reporting success over an empty search space is what this repository is built
	// against, so the gate is asked how much it looked at.
	require.Positive(t, scanned, "no non-test files were parsed; the rule is checking nothing")
}
